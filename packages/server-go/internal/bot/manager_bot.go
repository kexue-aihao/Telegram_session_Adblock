package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 管理机器人。
//
// 管理员可以直接在 Telegram 里跟它对话来托管其他机器人 ——
// 不必为了加一个机器人专门去开面板。
//
// ── 一条必须守住的安全边界 ──────────────────────────────────
//
// 这个机器人在 Telegram 上是**公开可私聊**的：任何人搜到用户名都能发消息。
// 而它能增删托管其他机器人、能读出每个机器人的 token 掩码与管理群 ID。
//
// 所以所有管理命令都以「发送者的 Telegram 用户 ID == 设置里的
// adminTgUserId」为前提，且未配置时一律拒绝。
//
// 刻意**不**提供「第一个发 /start 的人自动成为管理员」这类便利逻辑 ——
// 那等于把系统的初始控制权交给第一个碰巧找到它的人。

const mgrPrefix = "mgr"

func mgrCallback(action string, id int64) string {
	return fmt.Sprintf("%s:%s:%d", mgrPrefix, action, id)
}

func mgrMenuCallback(action string) string {
	return fmt.Sprintf("%s:%s:0", mgrPrefix, action)
}

// parseMgrCallback 解析管理机器人的按钮回调。
func parseMgrCallback(data string) (action string, id int64, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != mgrPrefix {
		return "", 0, false
	}
	// 第三段不是数字（例如 action 本身），当作无参动作
	n, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return parts[1], 0, true
	}
	return parts[1], n, true
}

// addFlow 是「添加机器人」的对话状态。
//
// 存在内存里而不是库里：它是一次几分钟的临时对话，重启丢掉是合理的 ——
// 用户重新 /add 一次即可，而为此建表得处理过期清理、并发等一堆问题。
type addFlow struct {
	step       string // token | group
	token      string
	botName    string
	username   string
	validation *domain.BotValidation
}

func (r *Runtime) setAddFlow(chatID int64, flow *addFlow) {
	r.mgrMu.Lock()
	defer r.mgrMu.Unlock()
	if flow == nil {
		delete(r.mgrStates, chatID)
		return
	}
	r.mgrStates[chatID] = flow
}

func (r *Runtime) getAddFlow(chatID int64) *addFlow {
	r.mgrMu.Lock()
	defer r.mgrMu.Unlock()
	return r.mgrStates[chatID]
}

// isManagerAdmin 判断这条私聊是否来自已配置的管理员。
//
// 只在管理机器人上做这个判断 —— 普通机器人的每条私聊都去查一次
// 全局设置是纯粹的浪费，而它们本来就不处理管理命令。
func (r *Runtime) isManagerAdmin(ctx context.Context, tgUserID int64) bool {
	if !r.isManager {
		return false
	}
	settings, err := r.db.GetGlobalSettings(ctx)
	if err != nil {
		r.log.Warn("读取全局设置失败，管理命令已拒绝", "err", err)
		return false
	}
	return settings.AdminTgUserID != 0 && settings.AdminTgUserID == tgUserID
}

// handleManagerPrivate 处理管理机器人的私聊。
//
// 返回 true 表示已经处理完，调用方不应再把它当中继消息。
func (r *Runtime) handleManagerPrivate(ctx context.Context, m *tgapi.Message) bool {
	if !r.isManager || m.From == nil {
		return false
	}

	// 非管理员：静默当成普通私聊走中继。
	//
	// 刻意不回「你不是管理员」—— 那等于向探测者确认「这个机器人有管理功能」。
	// 保持沉默，它在对方眼里就是一个普通机器人。
	if !r.isManagerAdmin(ctx, m.From.ID) {
		return false
	}

	text := strings.TrimSpace(m.Text)

	// 会话中的输入（token / 群 ID）优先
	if r.consumeAddFlow(ctx, m.From.ID, text) {
		return true
	}

	if cmd := parseCommand(text); cmd != "" {
		switch cmd {
		case "/start", "/menu":
			r.sendManagerMenu(ctx, m.From.ID)
		case "/help":
			r.sendManagerHelp(ctx, m.From.ID)
		case "/bots", "/list":
			r.sendBotList(ctx, m.From.ID)
		case "/status":
			r.sendManagerStatus(ctx, m.From.ID)
		case "/add":
			r.startAddFlow(ctx, m.From.ID)
		case "/cancel":
			r.setAddFlow(m.From.ID, nil)
			r.sendToUser(ctx, m.From.ID, "已取消。")
		default:
			r.sendToUser(ctx, m.From.ID, "未知命令。发 /help 看可用命令。")
		}
		return true
	}

	// 管理员发的普通文本。管理机器人**不是**客服机器人，不走中继 ——
	// 它存在的意义是管理。但也不能静默丢弃，否则管理员会以为它坏了。
	r.sendToUser(ctx, m.From.ID,
		"我是管理机器人，不处理普通消息。\n\n发 /start 打开菜单，或 /help 看命令列表。")
	return true
}

// ────────────────────────────── 菜单 ──────────────────────────────

func (r *Runtime) managerMenuKeyboard() *tgapi.InlineKeyboardMarkup {
	return tgapi.NewInlineKeyboard().
		Text("📋 机器人列表", mgrMenuCallback("list")).
		Text("➕ 添加机器人", mgrMenuCallback("add")).
		Row().
		Text("📊 系统概况", mgrMenuCallback("status")).
		Text("❓ 帮助", mgrMenuCallback("help")).
		Markup()
}

func (r *Runtime) sendManagerMenu(ctx context.Context, chatID int64) {
	bots, err := r.db.ListBots(ctx)
	if err != nil {
		r.sendToUser(ctx, chatID, "读取机器人列表失败："+err.Error())
		return
	}

	online, enabled := 0, 0
	for _, b := range bots {
		if b.IsEnabled {
			enabled++
		}
		if b.HealthStatus == domain.HealthOnline {
			online++
		}
	}

	text := fmt.Sprintf(
		"🛠️ **会话中继 · 管理台**\n\n托管机器人：**%d** 个（启用 %d · 在线 %d）\n\n选择下面的操作，或直接发命令。",
		len(bots), enabled, online)

	r.sendToUserKeyboard(ctx, chatID, text, r.managerMenuKeyboard())
}

func (r *Runtime) sendManagerHelp(ctx context.Context, chatID int64) {
	r.sendToUser(ctx, chatID, `**管理命令**

/start — 打开主菜单
/bots — 机器人列表，可启停、重载、删除
/add — 添加一个机器人（引导式）
/status — 系统概况
/help — 这份帮助
/cancel — 取消当前操作

**添加机器人的完整步骤**

1. 到 @BotFather 发 /newbot，拿到 token
2. 发 /add，把 token 粘贴给我
3. 如果提示 Privacy Mode，到 @BotFather 发 /setprivacy → 选它 → **Disable**
   （不关的话它读不到群里的普通消息，话题中继会完全失效）
4. 建一个私有超级群并**开启话题（Topics）**
5. 把机器人拉进群，**设为管理员**，至少勾选「管理话题」与「删除消息」
6. 把群 ID 发给我（形如 -1001234567890，可转发群消息给 @userinfobot 获取）

绑定群那一步可以发 /skip 跳过 —— 机器人照样能收私聊、回 /start，
只是消息不会中继进话题。之后在面板里补绑即可。`)
}

// ────────────────────────────── 机器人列表 ──────────────────────────────

func (r *Runtime) sendBotList(ctx context.Context, chatID int64) {
	bots, err := r.db.ListBots(ctx)
	if err != nil {
		r.sendToUser(ctx, chatID, "读取机器人列表失败："+err.Error())
		return
	}
	r.sendBotListTo(ctx, chatID, bots)
}

func (r *Runtime) sendBotListTo(ctx context.Context, chatID int64, bots []store.BotRow) {
	if len(bots) == 0 {
		r.sendToUserKeyboard(ctx, chatID,
			"还没有托管任何机器人。\n\n发 /add 添加第一个。",
			tgapi.NewInlineKeyboard().Text("➕ 添加机器人", mgrMenuCallback("add")).Markup())
		return
	}

	var sb strings.Builder
	sb.WriteString("📋 **已托管的机器人**\n")
	for i, b := range bots {
		sb.WriteString(fmt.Sprintf("\n%d. %s %s",
			i+1, healthEmoji(b.HealthStatus), escapeMarkdown(b.Name)))
		if b.IsManager {
			sb.WriteString("  ·管理")
		}
	}

	kb := tgapi.NewInlineKeyboard()
	for i, b := range bots {
		kb.Text(fmt.Sprintf("%d. %s", i+1, truncate(b.Name, 18)), mgrCallback("view", b.ID))
		if i%2 == 1 {
			kb.Row()
		}
	}
	kb.Row().Text("➕ 添加机器人", mgrMenuCallback("add")).Text("🏠 主菜单", mgrMenuCallback("home"))

	r.sendToUserKeyboard(ctx, chatID, sb.String(), kb.Markup())
}

func healthEmoji(status string) string {
	switch status {
	case domain.HealthOnline:
		return "🟢"
	case domain.HealthStarting:
		return "🟡"
	case domain.HealthError:
		return "🔴"
	default:
		return "⚪️"
	}
}

// sendBotDetail 单个机器人的详情与操作。
func (r *Runtime) sendBotDetail(ctx context.Context, chatID, botID int64) {
	row, err := r.db.GetBot(ctx, botID)
	if err != nil {
		r.sendToUser(ctx, chatID, "这个机器人已经不存在了。")
		r.sendBotList(ctx, chatID)
		return
	}

	group := "未绑定 ⚠️"
	if row.AdminGroupTitle != nil && *row.AdminGroupTitle != "" {
		group = *row.AdminGroupTitle
	} else if row.AdminGroupID != nil {
		group = strconv.FormatInt(*row.AdminGroupID, 10)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s **%s**\n", healthEmoji(row.HealthStatus), escapeMarkdown(row.Name))
	fmt.Fprintf(&sb, "`@%s`\n\n", escapeMarkdown(row.Username))
	fmt.Fprintf(&sb, "状态：%s", healthLabel(row.HealthStatus))
	if !row.IsEnabled {
		sb.WriteString("（已停用）")
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "管理群：%s\n", escapeMarkdown(group))
	tokenMask := row.TokenMask
	fmt.Fprintf(&sb, "Token：`%s`\n", escapeMarkdown(tokenMask))
	fmt.Fprintf(&sb, "最后轮询：%s\n", relativeTimeText(row.LastPolledAt))

	if row.IsManager {
		sb.WriteString("\n🛠️ 这是**管理机器人**\n")
	}
	if row.LastError != nil && *row.LastError != "" {
		fmt.Fprintf(&sb, "\n🔴 %s\n", escapeMarkdown(*row.LastError))
	}
	if row.LastRelayError != nil && *row.LastRelayError != "" {
		fmt.Fprintf(&sb, "\n⚠️ **消息未能中继**\n%s\n", escapeMarkdown(*row.LastRelayError))
	}

	toggleLabel := "⏸ 停用"
	if !row.IsEnabled {
		toggleLabel = "▶️ 启用"
	}

	kb := tgapi.NewInlineKeyboard().
		Text("🔄 重载", mgrCallback("reload", botID)).
		Text(toggleLabel, mgrCallback("toggle", botID)).
		Row().
		Text("🧪 群体检", mgrCallback("check", botID)).
		Text("🗑 删除", mgrCallback("del", botID)).
		Row().
		Text("⬅️ 返回列表", mgrMenuCallback("list"))

	r.sendToUserKeyboard(ctx, chatID, sb.String(), kb.Markup())
}

func healthLabel(status string) string {
	switch status {
	case domain.HealthOnline:
		return "在线"
	case domain.HealthStarting:
		return "启动中"
	case domain.HealthError:
		return "异常"
	case domain.HealthStopped:
		return "已停止"
	default:
		return "未知"
	}
}

// ────────────────────────────── 系统概况 ──────────────────────────────

func (r *Runtime) sendManagerStatus(ctx context.Context, chatID int64) {
	bots, err := r.db.ListBots(ctx)
	if err != nil {
		r.sendToUser(ctx, chatID, "读取失败："+err.Error())
		return
	}

	overview, err := r.db.ComputeOverview(ctx, r.loc, store.OverviewRules{})
	if err != nil {
		r.log.Warn("读取统计失败", "err", err)
	}

	online, disabled, errored := 0, 0, 0
	for _, b := range bots {
		switch {
		case !b.IsEnabled:
			disabled++
		case b.HealthStatus == domain.HealthOnline:
			online++
		case b.HealthStatus == domain.HealthError:
			errored++
		}
	}

	var sb strings.Builder
	sb.WriteString("📊 **系统概况**\n\n")
	fmt.Fprintf(&sb, "**机器人**\n总数 %d · 在线 %d", len(bots), online)
	if errored > 0 {
		fmt.Fprintf(&sb, " · 🔴 异常 %d", errored)
	}
	if disabled > 0 {
		fmt.Fprintf(&sb, " · 停用 %d", disabled)
	}
	sb.WriteString("\n\n")

	fmt.Fprintf(&sb, "**今日**\n")
	fmt.Fprintf(&sb, "收到消息 %d 条\n", overview.Messages.InToday)
	fmt.Fprintf(&sb, "发出消息 %d 条\n", overview.Messages.OutToday)
	fmt.Fprintf(&sb, "拦截广告 %d 条\n", overview.Ads.BlockedToday)
	fmt.Fprintf(&sb, "新增会话 %d 个\n\n", overview.Sessions.CreatedToday)

	fmt.Fprintf(&sb, "**累计**\n")
	fmt.Fprintf(&sb, "会话 %d 个（进行中 %d）\n", overview.Sessions.Open+overview.Sessions.Closed, overview.Sessions.Open)
	fmt.Fprintf(&sb, "用户 %d 人（拉黑 %d）\n", overview.Contacts.Total, overview.Contacts.Blocked)
	fmt.Fprintf(&sb, "规则 %d 条（启用 %d）\n", overview.Rules.Total, overview.Rules.Enabled)

	kb := tgapi.NewInlineKeyboard().
		Text("🔄 刷新", mgrMenuCallback("status")).
		Text("🏠 主菜单", mgrMenuCallback("home"))

	r.sendToUserKeyboard(ctx, chatID, sb.String(), kb.Markup())
}

// ────────────────────────────── 添加流程 ──────────────────────────────

func (r *Runtime) startAddFlow(ctx context.Context, chatID int64) {
	r.setAddFlow(chatID, &addFlow{step: "token"})
	r.sendToUser(ctx, chatID, `➕ **添加机器人**

请把 @BotFather 给的 **token** 发给我。

获取方式：
1. 打开 @BotFather，发 /newbot
2. 按提示输入名称与用户名
3. 复制它返回的那串 token（形如 `+"`123456789:AAH...`"+`），粘贴到这里

随时发 /cancel 取消。`)
}

// consumeAddFlow 处理添加流程中的用户输入。返回 true 表示已消费。
func (r *Runtime) consumeAddFlow(ctx context.Context, chatID int64, text string) bool {
	flow := r.getAddFlow(chatID)
	if flow == nil {
		return false
	}

	// /cancel 与 /skip 在流程中也要能用，所以在解析 token / 群 ID 之前先处理。
	// 它们会被 parseCommand 识别，但流程优先于命令分发，
	// 因此必须在这里挡掉，否则 "/cancel" 会被当成 token 或群 ID 去解析。
	switch strings.ToLower(text) {
	case "/cancel":
		r.setAddFlow(chatID, nil)
		r.sendToUser(ctx, chatID, "已取消。")
		return true
	case "/skip":
		if flow.step == "group" {
			r.finishAddFlow(ctx, chatID, flow, nil)
			return true
		}
	}

	switch flow.step {
	case "token":
		return r.addFlowToken(ctx, chatID, flow, text)
	case "group":
		return r.addFlowGroup(ctx, chatID, flow, text)
	}
	return false
}

func (r *Runtime) addFlowToken(ctx context.Context, chatID int64, flow *addFlow, text string) bool {
	if !LooksLikeBotToken(text) {
		r.sendToUser(ctx, chatID,
			"这不像 BotFather 的 token。\n\n它形如 `123456789:AAH...`，冒号前是一串数字。\n\n再发一次，或 /cancel 取消。")
		return true
	}

	validation, err := ValidateToken(ctx, text)
	if err != nil {
		r.sendToUser(ctx, chatID, "校验失败："+err.Error()+"\n\n再发一次，或 /cancel 取消。")
		return true
	}
	if !validation.OK {
		msg := "token 无效"
		if validation.Error != nil {
			msg = *validation.Error
		}
		r.sendToUser(ctx, chatID, "❌ "+msg+"\n\n再发一次，或 /cancel 取消。")
		return true
	}

	flow.step = "group"
	flow.token = text
	flow.validation = &validation
	if validation.Username != nil {
		flow.username = *validation.Username
	}
	if validation.Name != nil {
		flow.botName = *validation.Name
	}
	r.setAddFlow(chatID, flow)

	// Privacy Mode 是这条链路上最常见的一个坑：不关掉的话机器人
	// 读不到群里的普通消息，话题中继会完全失效，而现象是「什么都收不到」。
	privacyWarning := ""
	if validation.CanReadAllGroupMessages != nil && !*validation.CanReadAllGroupMessages {
		privacyWarning = "\n\n⚠️ **这个机器人开着 Privacy Mode**\n" +
			"它读不到群里的普通消息，中继会完全失效。\n" +
			"请到 @BotFather 发 /setprivacy → 选它 → **Disable**。"
	}

	r.sendToUser(ctx, chatID, fmt.Sprintf(
		"✅ 已验证：**%s**（`@%s`）%s\n\n"+
			"**接下来请准备好群：**\n"+
			"1. 建一个私有超级群，在群设置里**开启话题（Topics）**\n"+
			"2. 把机器人拉进群，**设为管理员**\n"+
			"3. 至少勾选「管理话题」与「删除消息」\n\n"+
			"然后把**群 ID** 发给我（形如 `-1001234567890`）。\n"+
			"不知道群 ID？把群里任意一条消息转发给 @userinfobot 即可看到。\n\n"+
			"也可以发 /skip 先不绑定群。",
		escapeMarkdown(flow.botName), escapeMarkdown(flow.username), privacyWarning))
	return true
}

func (r *Runtime) addFlowGroup(ctx context.Context, chatID int64, flow *addFlow, text string) bool {
	groupID, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || groupID >= 0 {
		r.sendToUser(ctx, chatID,
			"群 ID 看起来不对。\n\n超级群的 ID 是**负数**，形如 `-1001234567890`。\n"+
				"把群里任意一条消息转发给 @userinfobot 就能看到。\n\n"+
				"再发一次，或 /skip 跳过绑定。")
		return true
	}

	// 体检。不通过也允许继续 —— 管理员可能还没把机器人拉进群，
	// 而先把配置存下来、之后在面板里补权限是合理的流程。
	check, checkErr := CheckGroup(ctx, flow.token, groupID)
	if checkErr != nil {
		r.sendToUser(ctx, chatID, "体检时出错："+checkErr.Error()+"\n\n再发一次群 ID，或 /skip 跳过。")
		return true
	}

	if !check.OK {
		var sb strings.Builder
		sb.WriteString("⚠️ **这个群还有问题：**\n\n")
		for _, p := range check.Problems {
			fmt.Fprintf(&sb, "· %s\n", escapeMarkdown(p))
		}
		sb.WriteString("\n仍然可以继续绑定（之后在面板里补权限即可），发 /skip 跳过绑定，或发 /cancel 取消。")
		r.sendToUser(ctx, chatID, sb.String())
		// 记下来：管理员再发一次群 ID 就继续
		flow.step = "group"
		r.setAddFlow(chatID, flow)
		return true
	}

	r.finishAddFlow(ctx, chatID, flow, &groupID)
	return true
}

func (r *Runtime) finishAddFlow(ctx context.Context, chatID int64, flow *addFlow, groupID *int64) {
	r.setAddFlow(chatID, nil)

	created, err := r.manager.Create(ctx, CreateRequest{
		Token:        flow.token,
		AdminGroupID: groupID,
		Name:         flow.botName,
		Actor:        fmt.Sprintf("telegram:%d", chatID),
	})
	if err != nil {
		r.sendToUser(ctx, chatID, "❌ 添加失败："+escapeMarkdown(err.Error())+"\n\n可以重新 /add 再来一次。")
		return
	}

	groupNote := "未绑定管理群 —— 用户的消息暂时不会中继进话题。之后可在面板里补绑。"
	if groupID != nil {
		groupNote = fmt.Sprintf("管理群已绑定（`%d`）。", *groupID)
	}

	r.sendToUser(ctx, chatID, fmt.Sprintf(
		"✅ **已添加：%s**\n`@%s`\n\n%s\n\n现在用另一个 Telegram 账号私聊它，管理群里会自动出现一个话题。",
		escapeMarkdown(created.Name), escapeMarkdown(created.Username), groupNote))

	r.sendBotDetail(ctx, chatID, created.ID)
}

// ────────────────────────────── 按钮回调 ──────────────────────────────

// handleManagerCallback 处理管理机器人的按钮。返回 true 表示已处理。
func (r *Runtime) handleManagerCallback(ctx context.Context, q *tgapi.CallbackQuery) bool {
	if !r.isManager || q.From == nil {
		return false
	}
	action, id, ok := parseMgrCallback(q.Data)
	if !ok {
		return false
	}

	// 与命令走同一条安全边界：非管理员的点击一律忽略。
	//
	// 这一条不能省 —— 按钮消息可能被转发出去，别人点得到。
	if !r.isManagerAdmin(ctx, q.From.ID) {
		_ = r.api.AnswerCallbackQuery(ctx, q.ID, "无权操作", true)
		return true
	}

	chatID := q.From.ID

	switch action {
	case "home":
		r.sendManagerMenu(ctx, chatID)
	case "list":
		r.sendBotList(ctx, chatID)
	case "status":
		r.sendManagerStatus(ctx, chatID)
	case "help":
		r.sendManagerHelp(ctx, chatID)
	case "add":
		r.startAddFlow(ctx, chatID)

	case "view":
		r.sendBotDetail(ctx, chatID, id)
	case "reload":
		if err := r.manager.Reload(ctx, id); err != nil {
			r.sendToUser(ctx, chatID, "重载失败："+escapeMarkdown(err.Error()))
		}
		r.sendBotDetail(ctx, chatID, id)
	case "toggle":
		row, err := r.db.GetBot(ctx, id)
		if err == nil {
			next := !row.IsEnabled
			if err := r.db.UpdateBot(ctx, id, store.UpdateBotFields{IsEnabled: &next}); err == nil {
				if next {
					_ = r.manager.Start(ctx, id)
				} else {
					r.manager.Stop(ctx, id)
				}
			}
		}
		r.sendBotDetail(ctx, chatID, id)
	case "check":
		r.sendGroupCheckResult(ctx, chatID, id)
	case "del":
		r.sendDeleteConfirm(ctx, chatID, id)
	case "delok":
		row, err := r.db.GetBot(ctx, id)
		name := "该机器人"
		if err == nil {
			name = row.Name
		}
		if err := r.manager.Delete(ctx, id); err != nil {
			r.sendToUser(ctx, chatID, "删除失败："+escapeMarkdown(err.Error()))
		} else {
			r.sendToUser(ctx, chatID, fmt.Sprintf("🗑 已删除 **%s** 及其全部会话记录。", escapeMarkdown(name)))
		}
		r.sendBotList(ctx, chatID)
	}

	return true
}

func (r *Runtime) sendDeleteConfirm(ctx context.Context, chatID, botID int64) {
	row, err := r.db.GetBot(ctx, botID)
	if err != nil {
		r.sendBotList(ctx, chatID)
		return
	}

	r.sendToUserKeyboard(ctx, chatID, fmt.Sprintf(
		"🗑 **删除确认**\n\n将删除 **%s**（`@%s`）及其**全部会话、消息与命中记录**。\n\n此操作不可撤销。Telegram 侧的群与话题不会被删除。",
		escapeMarkdown(row.Name), escapeMarkdown(row.Username)),
		tgapi.NewInlineKeyboard().
			Text("✅ 确认删除", mgrCallback("delok", botID)).
			Text("⬅️ 取消", mgrCallback("view", botID)).
			Markup())
}

func (r *Runtime) sendGroupCheckResult(ctx context.Context, chatID, botID int64) {
	row, err := r.db.GetBot(ctx, botID)
	if err != nil {
		r.sendBotList(ctx, chatID)
		return
	}
	if row.AdminGroupID == nil {
		r.sendToUserKeyboard(ctx, chatID,
			"这个机器人还没有绑定管理群。\n\n在面板的「机器人」页绑定，或删除后重新 /add。",
			tgapi.NewInlineKeyboard().Text("⬅️ 返回", mgrCallback("view", botID)).Markup())
		return
	}

	// 体检的是**别的**机器人，需要它自己的 token ——
	// 解密 token 的主密钥只有 Manager 持有，所以这一步委托过去。
	check, err := r.manager.CheckGroupForBot(ctx, botID)
	if err != nil {
		r.sendToUser(ctx, chatID, "体检失败："+escapeMarkdown(err.Error()))
		return
	}

	var sb strings.Builder
	if check.OK {
		sb.WriteString("✅ **管理群配置正常**\n\n")
	} else {
		sb.WriteString("⚠️ **管理群还有问题**\n\n")
	}
	fmt.Fprintf(&sb, "群名：%s\n\n", escapeMarkdown(derefOrString(check.Title, "（读不到）")))
	for _, p := range check.Problems {
		fmt.Fprintf(&sb, "· %s\n", escapeMarkdown(p))
	}
	if check.OK {
		sb.WriteString("话题创建与消息删除都已就绪。")
	}

	r.sendToUserKeyboard(ctx, chatID, sb.String(),
		tgapi.NewInlineKeyboard().Text("⬅️ 返回", mgrCallback("view", botID)).Markup())
}

// ────────────────────────────── 小工具 ──────────────────────────────

func relativeTimeText(ts *int64) string {
	if ts == nil {
		return "从未"
	}
	return timeSince(*ts)
}

func derefOrString(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

// escapeMarkdown 转义 Telegram Markdown(V1) 的特殊字符。
//
// 管理台的消息里混着大量来自用户与 Telegram 的文本（机器人名字、
// 群名、错误信息），不转义会让消息直接发送失败（400 can't parse entities），
// 或者更糟 —— 把别人可控的内容渲染成我们没打算给的格式。
func escapeMarkdown(s string) string {
	return rules.EscapeMarkdown(s)
}

// timeSince 把时间戳说成人话。
//
// 与前端那份 relativeTime 是同一套语义，但刻意不复用 ——
// 一个跑在浏览器里、一个跑在 Go 里，没有共享的可能，
// 而各自只有十几行。
func timeSince(ts int64) string {
	d := time.Since(time.UnixMilli(ts))
	switch {
	case d < 0:
		return "刚刚"
	case d < 30*time.Second:
		return "刚刚"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒前", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	}
}
