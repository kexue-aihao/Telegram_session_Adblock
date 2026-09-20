// Package bot 是机器人运行时：长轮询、中继管线、命令与规则执行。
package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 话题图标颜色。Bot API 只接受这六个固定值，传别的会直接报错，
// 所以从面板拿到的数字必须在这里收窄一次。
var allowedIconColors = map[int]bool{
	0x6FB9F0: true, // 蓝
	0xFFD67E: true, // 黄
	0xCB86DB: true, // 紫
	0x8EEE98: true, // 绿
	0xFF93B2: true, // 粉
	0xFB6F5F: true, // 红
}

func resolveIconColor(v int) int {
	if allowedIconColors[v] {
		return v
	}
	return 0x6FB9F0
}

// callbackPrefix 用在置顶消息的按钮上，避免与其他 bot 的回调撞车。
const callbackPrefix = "tgs"

func topicCallback(action string, topicID int64) string {
	return fmt.Sprintf("%s:%s:%d", callbackPrefix, action, topicID)
}

// ParsedCallback 是解析出的按钮回调。
type ParsedCallback struct {
	Action  string
	TopicID int64
}

// parseCallback 解析按钮回调数据。
func parseCallback(data string) (ParsedCallback, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != callbackPrefix {
		return ParsedCallback{}, false
	}

	var topicID int64
	if _, err := fmt.Sscanf(parts[2], "%d", &topicID); err != nil {
		return ParsedCallback{}, false
	}
	if topicID <= 0 {
		return ParsedCallback{}, false
	}
	return ParsedCallback{Action: parts[1], TopicID: topicID}, true
}

// ────────────────────────────── 话题标题 ──────────────────────────────

// buildTopicTitle 按模板渲染话题标题。
func buildTopicTitle(tmpl string, contact store.ContactRow, botName string) string {
	vars := map[string]string{
		"name":      firstNonEmpty(deref(contact.FirstName), deref(contact.Username), "未知用户"),
		"username":  atPrefix(deref(contact.Username)),
		"id":        itoa(contact.TgUserID),
		"firstName": deref(contact.FirstName),
		"lastName":  deref(contact.LastName),
		"botName":   botName,
	}

	title := renderTemplate(tmpl, vars)
	// 模板本身不含 Markdown 语义，所以把转义还原成普通字符 ——
	// 话题标题不接受 Markdown，带反斜杠反而会让管理员看到 `\_`
	title = unescapeMarkdown(title)

	// Telegram 的话题标题上限是 128 字符。按 rune 截断而不是 byte，
	// 否则中文标题会在半路被切成乱码。
	runes := []rune(title)
	if len(runes) > 128 {
		title = string(runes[:128])
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Sprintf("用户 %d", contact.TgUserID)
	}
	return title
}

// ────────────────────────────── 头部消息 ──────────────────────────────

// headerKeyboard 构造置顶消息的内联键盘。
//
// 「关闭话题 / 拉黑 / 重置违规分」是管理员在话题里最常用的三个动作，
// 放在置顶消息上比记命令更现实 —— 管理员未必记得住 /reset 这类命令，
// 但一定点得到按钮。
func headerKeyboard(topicID int64, contact store.ContactRow, status string) *tgapi.InlineKeyboardMarkup {
	kb := tgapi.NewInlineKeyboard()

	if status == domain.TopicOpen {
		kb.Text("🔒 关闭话题", topicCallback("close", topicID))
	} else {
		kb.Text("🔓 重新打开", topicCallback("reopen", topicID))
	}

	if contact.IsBlocked {
		kb.Text("♻️ 解除拉黑", topicCallback("unban", topicID))
	} else {
		kb.Text("🚫 拉黑", topicCallback("ban", topicID))
	}

	kb.Row().Text("🧹 重置违规分", topicCallback("reset", topicID))
	return kb.Markup()
}

// ensureTopic 取（不存在则在管理群中创建）该用户的话题。
//
// 并发安全：同一用户的两条消息可能同时在两个协程里走到这里
// （runner 是并发处理更新的）。依赖 topics_bot_contact_uniq 唯一索引 ——
// 第二个写入者拿到 ErrDuplicate 后重新查一次即可，
// 代价远低于为它加一把跨协程的锁。
func (r *Runtime) ensureTopic(
	ctx context.Context,
	settings store.BotSettings,
	contact store.ContactRow,
) (result store.TopicRow, created bool, resultErr error) {
	// Every entry point that creates a conversation must notify the WebUI.
	defer func() {
		if resultErr == nil && created {
			_ = r.bumpStats(ctx, store.StatDelta{TopicsCreated: 1})
			r.publishSessionEvent(ctx, result.ID, bus.EventSessionCreated)
		}
	}()

	// 没绑管理群就别往下走了。
	//
	// 不加这个判断的话，会拿 chat_id=0 去调 createForumTopic，
	// Telegram 回一句「Bad Request: chat not found」—— 那句话完全不提
	// 「你还没绑群」，是本项目最容易让人排查半天的一种报错。
	if r.adminGroupID == 0 {
		return store.TopicRow{}, false, errors.New(
			"尚未绑定管理群，用户的消息无法中继进话题。请在面板的「机器人」页绑定")
	}

	existing, err := r.db.GetTopicByContact(ctx, r.botID, contact.ID)
	if err == nil {
		switch existing.Status {
		case domain.TopicOpen:
			return existing, false, nil

		case domain.TopicClosed:
			// 用户又来消息了 —— 自动重开比让消息石沉大海更符合预期
			if err := r.reopenTopic(ctx, existing); err != nil {
				r.log.Warn("自动重开话题失败", "topicId", existing.ID, "err", err)
			}
			existing.Status = domain.TopicOpen
			existing.ClosedAt = nil
			return existing, false, nil

		case domain.TopicDeleted:
			// 话题被管理员手动删掉了，重新建一个，复用同一行记录
			return r.recreateTopic(ctx, settings, contact, existing)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.TopicRow{}, false, err
	}

	title := buildTopicTitle(settings.TopicNameTemplate, contact, r.botName)
	iconColor := resolveIconColor(settings.TopicIconColor)

	forumTopic, err := r.api.CreateForumTopic(ctx, r.adminGroupID, title, iconColor)
	if err != nil {
		// 把 Telegram 的英文原文换成人能照着修的说法。
		//
		// 这一步失败的三个原因占了绝大多数：机器人不是群管理员、
		// 群没开 Topics、或者绑错了群 ID。原文一个都不提。
		return store.TopicRow{}, false, fmt.Errorf(
			"创建话题失败：%s\n请依次检查：① 机器人是管理群的管理员 ② 该群开启了「话题（Topics）」③ 绑定的群 ID 正确",
			DescribeTelegramError(err))
	}

	topic, err := r.db.CreateTopic(ctx, r.botID, contact.ID, forumTopic.MessageThreadID, title, iconColor)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			// 竞态：另一个协程抢先建好了。用它的记录，把我们多建的话题删掉。
			r.log.Debug("话题创建竞态，复用已有记录", "contactId", contact.ID)
			_ = r.api.DeleteForumTopic(ctx, r.adminGroupID, forumTopic.MessageThreadID)

			raced, getErr := r.db.GetTopicByContact(ctx, r.botID, contact.ID)
			if getErr != nil {
				return store.TopicRow{}, false, getErr
			}
			return raced, false, nil
		}
		return store.TopicRow{}, false, err
	}

	// 置顶头部消息失败不影响话题可用性
	withHeader, err := r.pinHeader(ctx, settings, topic, contact)
	if err != nil {
		r.log.Warn("创建头部消息失败，话题仍可正常使用", "topicId", topic.ID, "err", err)
		return topic, true, nil
	}
	return withHeader, true, nil
}

// recreateTopic 在话题被删除后重建，复用同一行记录。
func (r *Runtime) recreateTopic(
	ctx context.Context,
	settings store.BotSettings,
	contact store.ContactRow,
	stale store.TopicRow,
) (store.TopicRow, bool, error) {
	title := buildTopicTitle(settings.TopicNameTemplate, contact, r.botName)
	iconColor := resolveIconColor(settings.TopicIconColor)

	created, err := r.api.CreateForumTopic(ctx, r.adminGroupID, title, iconColor)
	if err != nil {
		return store.TopicRow{}, false, fmt.Errorf("重建话题: %w", err)
	}

	if err := r.db.SetTopicThread(ctx, stale.ID, created.MessageThreadID, title); err != nil {
		return store.TopicRow{}, false, err
	}

	fresh := stale
	fresh.MessageThreadID = created.MessageThreadID
	fresh.Title = title
	fresh.IconColor = iconColor
	fresh.Status = domain.TopicOpen
	fresh.ClosedAt = nil
	fresh.PinnedHeaderID = nil

	withHeader, err := r.pinHeader(ctx, settings, fresh, contact)
	if err != nil {
		return fresh, true, nil
	}
	r.log.Info("话题已重建", "topicId", stale.ID, "threadId", created.MessageThreadID)
	return withHeader, true, nil
}

// pinHeader 置顶头部消息。
func (r *Runtime) pinHeader(
	ctx context.Context,
	settings store.BotSettings,
	topic store.TopicRow,
	contact store.ContactRow,
) (store.TopicRow, error) {
	if !settings.PinTopicHeader {
		return topic, nil
	}

	text := renderTemplate(settings.TopicHeaderTemplate, map[string]string{
		"name":      contact.DisplayName(),
		"username":  orDefault(atPrefix(deref(contact.Username)), "（无用户名）"),
		"id":        itoa(contact.TgUserID),
		"firstName": deref(contact.FirstName),
		"lastName":  deref(contact.LastName),
		"botName":   r.botName,
	})

	messageID, err := r.sendToTopic(ctx, topic.MessageThreadID, text, headerKeyboard(topic.ID, contact, topic.Status), true)
	if err != nil || messageID == 0 {
		return topic, err
	}

	if err := r.api.PinChatMessage(ctx, r.adminGroupID, messageID, true); err != nil {
		// 缺少 can_pin_messages 权限时这里会失败，但不该影响话题可用性
		r.log.Warn("置顶头部消息失败（通常是缺少 can_pin_messages 权限）", "topicId", topic.ID)
	}

	if err := r.db.SetTopicHeader(ctx, topic.ID, messageID); err != nil {
		return topic, err
	}
	topic.PinnedHeaderID = &messageID
	return topic, nil
}

// closeTopic 关闭话题。
func (r *Runtime) closeTopic(ctx context.Context, topicID, threadID int64) error {
	if err := r.api.CloseForumTopic(ctx, r.adminGroupID, threadID); err != nil {
		// 可能已被手动关闭 —— 记日志但不阻断
		r.log.Warn("关闭话题失败", "topicId", topicID, "err", err)
	}
	return r.db.UpdateTopicStatus(ctx, topicID, domain.TopicClosed)
}

// reopenTopic 重新打开话题。
func (r *Runtime) reopenTopic(ctx context.Context, topic store.TopicRow) error {
	if err := r.api.ReopenForumTopic(ctx, r.adminGroupID, topic.MessageThreadID); err != nil {
		r.log.Warn("重开话题失败", "topicId", topic.ID, "err", err)
	}
	return r.db.UpdateTopicStatus(ctx, topic.ID, domain.TopicOpen)
}

// refreshTopicIdentity 在用户改名后同步话题标题。
func (r *Runtime) refreshTopicIdentity(
	ctx context.Context,
	settings store.BotSettings,
	topic store.TopicRow,
	contact store.ContactRow,
) {
	title := buildTopicTitle(settings.TopicNameTemplate, contact, r.botName)
	if title == topic.Title {
		return
	}
	if err := r.api.EditForumTopic(ctx, r.adminGroupID, topic.MessageThreadID, title); err != nil {
		r.log.Debug("重命名话题失败", "topicId", topic.ID, "err", err)
		return
	}
	_ = r.db.SetTopicTitle(ctx, topic.ID, title)
}

// ────────────────────────────── 发送 ──────────────────────────────

// sendToTopic 往话题里发一条 Markdown 消息，失败时降级为纯文本重发。
//
// 降级是必要的：文案模板里的占位符已经被转义过，但管理员自己写的模板
// 完全可能带一个没用反斜杠转义的 `*`。一条文案写错不该让整条中继链路失败。
func (r *Runtime) sendToTopic(
	ctx context.Context,
	threadID int64,
	text string,
	keyboard *tgapi.InlineKeyboardMarkup,
	silent bool,
) (int64, error) {
	if text == "" {
		return 0, nil
	}

	msg, err := r.api.SendMessage(ctx, r.adminGroupID, text, tgapi.SendMessageOptions{
		ThreadID:            threadID,
		Markdown:            true,
		Keyboard:            keyboard,
		DisableNotification: silent,
		DisableLinkPreview:  true,
	})
	if err == nil {
		return msgID(msg), nil
	}

	r.log.Warn("Markdown 发送失败，降级为纯文本重发", "threadId", threadID)

	msg, retryErr := r.api.SendMessage(ctx, r.adminGroupID, text, tgapi.SendMessageOptions{
		ThreadID:            threadID,
		Keyboard:            keyboard,
		DisableNotification: silent,
		DisableLinkPreview:  true,
	})
	if retryErr != nil {
		return 0, retryErr
	}
	return msgID(msg), nil
}

// sendToUser 发给用户私聊。
//
// 403 意味着「用户屏蔽了机器人」—— 那是一个需要被记录的业务状态，
// 不是异常。因此返回 sent=false 而不是 error，让调用方区分处理。
func (r *Runtime) sendToUser(ctx context.Context, tgUserID int64, text string) (sent bool, messageID int64) {
	if text == "" {
		return false, 0
	}

	msg, err := r.api.SendMessage(ctx, tgUserID, text, tgapi.SendMessageOptions{
		Markdown:           true,
		DisableLinkPreview: true,
	})
	if err == nil {
		return true, msgID(msg)
	}

	// 先降级重试一次纯文本
	msg, retryErr := r.api.SendMessage(ctx, tgUserID, text, tgapi.SendMessageOptions{
		DisableLinkPreview: true,
	})
	if retryErr == nil {
		return true, msgID(msg)
	}

	var apiErr *tgapi.APIError
	if errors.As(retryErr, &apiErr) && apiErr.IsBlockedByUser() {
		if err := r.db.SetContactUnreachable(ctx, r.botID, tgUserID, true); err != nil {
			r.log.Warn("标记联系人不可达失败", "err", err)
		}
		return false, 0
	}

	r.log.Error("发送私聊消息失败", "tgUserId", tgUserID, "err", retryErr)
	return false, 0
}

// sendToUserKeyboard 与 sendToUser 相同，但带内联键盘。
//
// 单独一个方法而不是给 sendToUser 加可选参数：Go 没有默认参数，
// 而"加一个 *InlineKeyboardMarkup 参数"会让既有的十几处调用点
// 全部要传 nil —— 那种改动没有任何信息量。
func (r *Runtime) sendToUserKeyboard(
	ctx context.Context,
	tgUserID int64,
	text string,
	keyboard *tgapi.InlineKeyboardMarkup,
) (sent bool, messageID int64) {
	if text == "" {
		return false, 0
	}

	msg, err := r.api.SendMessage(ctx, tgUserID, text, tgapi.SendMessageOptions{
		Markdown:           true,
		Keyboard:           keyboard,
		DisableLinkPreview: true,
	})
	if err == nil {
		return true, msgID(msg)
	}

	// 与 sendToUser 同样的降级：管理员自己配的文案里可能有个没转义的
	// 星号，那不该让整条消息发不出去。
	msg, retryErr := r.api.SendMessage(ctx, tgUserID, text, tgapi.SendMessageOptions{
		Keyboard:           keyboard,
		DisableLinkPreview: true,
	})
	if retryErr == nil {
		return true, msgID(msg)
	}

	var apiErr *tgapi.APIError
	if errors.As(retryErr, &apiErr) && apiErr.IsBlockedByUser() {
		if err := r.db.SetContactUnreachable(ctx, r.botID, tgUserID, true); err != nil {
			r.log.Warn("标记联系人不可达失败", "err", err)
		}
		return false, 0
	}

	r.log.Error("发送私聊消息失败", "tgUserId", tgUserID, "err", retryErr)
	return false, 0
}

// ────────────────────────────── 模板 ──────────────────────────────

// renderTemplate 替换 `{name}` 这类占位符。
//
// 变量值一律走 Markdown 转义：`{name}` 里塞什么完全由用户决定，
// 昵称里的 `**` / 反引号 / `[` 不转义会让消息解析成意料之外的格式，
// 更常见的是直接 400 发送失败。
//
// 模板本身**不转义** —— 它就是写给 Markdown 看的。
func renderTemplate(tmpl string, vars map[string]string) string {
	var b strings.Builder
	b.Grow(len(tmpl) + 64)

	i := 0
	for i < len(tmpl) {
		open := strings.IndexByte(tmpl[i:], '{')
		if open < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		open += i

		closeIdx := strings.IndexByte(tmpl[open:], '}')
		if closeIdx < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		closeIdx += open

		b.WriteString(tmpl[i:open])

		key := tmpl[open+1 : closeIdx]
		if value, ok := vars[key]; ok {
			b.WriteString(rules.EscapeMarkdown(value))
		}
		// 未提供的变量渲染成空串而不是原样保留 `{foo}`，
		// 避免把内部变量名泄露给终端用户

		i = closeIdx + 1
	}
	return b.String()
}

// unescapeMarkdown 去掉模板渲染时加上的转义反斜杠。
// 话题标题不接受 Markdown，带反斜杠会让管理员看到 `\_` 这种字面量。
func unescapeMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == '_' || next == '*' || next == '`' || next == '[' || next == ']' {
				continue // 跳过反斜杠，保留被转义的字符
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ────────────────────────────── 小工具 ──────────────────────────────

func msgID(m *tgapi.Message) int64 {
	if m == nil {
		return 0
	}
	return m.MessageID
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func atPrefix(username string) string {
	if username == "" {
		return ""
	}
	return "@" + username
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func itoa(v int64) string {
	return fmt.Sprintf("%d", v)
}

// formatUntil 格式化解禁时间。
func formatUntil(expiresAt *int64, loc *time.Location) string {
	if expiresAt == nil {
		return "永久"
	}
	return time.UnixMilli(*expiresAt).In(loc).Format("2006-01-02 15:04")
}
