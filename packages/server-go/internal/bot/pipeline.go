package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 用户私聊 → 管理群话题 的中继管线。
//
// 这是整个产品的主干道，每条用户消息都要走一遍，所以分支顺序是按
// 「拦截成本从低到高」排的：拉黑 → 处罚状态 → 刷屏 → 规则引擎 → 转发。
// 把最贵的规则匹配放在最前面，会让被封禁用户的垃圾消息也吃满 CPU。

const floodWindow = 3 * time.Second

// processUserMessage 处理一条（或一个已聚合的相册）用户消息。
func (r *Runtime) processUserMessage(ctx context.Context, messages []*tgapi.Message) {
	first := messages[0]
	if first == nil || first.From == nil {
		return
	}

	settings, err := r.db.GetBotSettings(ctx, r.botID)
	if err != nil {
		r.log.Error("读取机器人设置失败", "botId", r.botID, "err", err)
		return
	}

	contact, err := r.db.UpsertContact(ctx, r.botID, profileOf(first.From))
	if err != nil {
		r.log.Error("更新联系人失败", "err", err)
		return
	}

	// 1. 被管理员拉黑：机器人不再响应，连「你被拉黑了」都不回
	if contact.IsBlocked {
		return
	}

	// 2. 阶梯处罚的阻断态
	state, err := r.sanction.Blocking(ctx, contact.ID)
	if err != nil {
		r.log.Error("查询处罚状态失败", "contactId", contact.ID, "err", err)
		state = sanction.BlockingState{}
	}
	if state.Blocked {
		r.handleBlocked(ctx, settings, contact, state)
		return
	}

	// 3. 刷屏判定。命中后走 flood 原因的处罚，但**不删消息** ——
	//    刷屏判定比关键词规则粗糙得多，误判的代价必须小。
	if r.detectFlood(contact.ID, settings.FloodThreshold) {
		r.handleFlood(ctx, settings, contact)
		return
	}

	// 4. 规则引擎。命中即拦截 —— 先判后转，避免「先发出去再撤回」
	//    留下一个能被截图的时间窗。
	raw, normalized := buildHaystacks(first)
	hits, err := r.engine.Evaluate(ctx, rulesContext(r.botID, "user", raw, normalized), settings.RulesEnabled)
	if err != nil {
		r.log.Error("规则匹配失败", "err", err)
		hits = nil
	}

	var topic store.TopicRow
	if len(hits) > 0 {
		// 触发规则时先建话题：告警卡片要落在话题里，否则管理员在一个
		// 空话题群里看到一条孤零零的告警，连上下文都没有。
		topic, _, err = r.ensureTopic(ctx, settings, contact)
		if err != nil {
			r.log.Error("创建话题失败", "contactId", contact.ID, "err", err)
			r.setRelayError(ctx, err)
			return
		}

		report := r.applyRuleHits(ctx, hitContext{
			settings:        settings,
			contact:         contact,
			scope:           "user",
			sourceChatID:    first.Chat.ID,
			sourceMessageID: first.MessageID,
			topicID:         &topic.ID,
			threadID:        &topic.MessageThreadID,
			text:            rawText(first),
		}, hits)

		_ = r.bumpStats(ctx, store.StatDelta{AdsBlocked: 1})

		if report.Blocked {
			// 静默 / 禁言 / 拉黑：到此为止，消息不进话题
			_ = r.db.TouchTopic(ctx, topic.ID)
			return
		}
	}

	// 5. 正常中继
	if topic.ID == 0 {
		topic, _, err = r.ensureTopic(ctx, settings, contact)
		if err != nil {
			r.log.Error("创建话题失败", "contactId", contact.ID, "err", err)
			r.setRelayError(ctx, err)
			return
		}
	}
	// 用户改名后同步话题标题；失败不影响中继
	r.refreshTopicIdentity(ctx, settings, topic, contact)

	if relayErr := r.relayToTopic(ctx, topic, messages); relayErr != nil {
		r.log.Error("转发失败", "topicId", topic.ID, "err", relayErr)
		r.notifyRelayFailure(ctx, topic, relayErr)
		r.setRelayError(ctx, relayErr)
	} else {
		// 成功才清标记：错误表示的是「当前状态」而不是「历史事件」
		r.clearRelayError(ctx)
	}

	_ = r.db.TouchTopic(ctx, topic.ID)
	_ = r.bumpStats(ctx, store.StatDelta{MessagesIn: 1})
	r.publishSession(ctx, topic.ID)
}

// handleBlocked 处理静默 / 禁言 / 拉黑期间收到的消息。
//
// 三种阻断型处罚对用户的**可见性**完全不同，这里必须区分：
//   - silence：什么都不做，消息石沉大海（这是它的全部意义 —— 一旦回复，
//     就等于告诉对方「你被静默了，换个号再来」）
//   - mute：回一次禁言提示与解禁时间，之后不再重复
//   - ban：机器人完全不再响应
func (r *Runtime) handleBlocked(
	ctx context.Context,
	settings store.BotSettings,
	contact store.ContactRow,
	state sanction.BlockingState,
) {
	if state.Type != domain.SanctionMute {
		return
	}

	// 记的是「那一次禁言的解禁时刻」：同一个人第二次被禁言时应当再提示一次，
	// 用布尔标记会把第二次提示吞掉。
	r.muteMu.Lock()
	notified, seen := r.mutedNotified[contact.ID]
	alreadySent := seen && sameExpiry(notified, state.ExpiresAt)
	if !alreadySent {
		r.mutedNotified[contact.ID] = state.ExpiresAt
	}
	r.muteMu.Unlock()

	if alreadySent {
		return
	}

	text := renderTemplate(settings.MuteTemplate, map[string]string{
		"name":     contact.DisplayName(),
		"username": deref(contact.Username),
		"id":       itoa(contact.TgUserID),
		"until":    formatUntil(state.ExpiresAt, r.loc),
		"score":    itoa(int64(contact.ViolationScore)),
		"botName":  r.botName,
	})
	r.sendToUser(ctx, contact.TgUserID, text)
}

// detectFlood 判断是否在短时间内连发。
//
// 用滑动窗口而不是固定计数：固定计数会在一分钟边界上误判 ——
// 用户在 59 秒和 61 秒各发一条，不该算作「连发两条」。
func (r *Runtime) detectFlood(contactID int64, threshold int) bool {
	now := time.Now()

	r.floodMu.Lock()
	defer r.floodMu.Unlock()

	window := r.floodWindow[contactID]
	// 丢掉窗口外的时间戳
	cut := 0
	for cut < len(window) && now.Sub(window[cut]) > floodWindow {
		cut++
	}
	window = append(window[cut:], now)
	r.floodWindow[contactID] = window

	return len(window) > threshold
}

// handleFlood 处理刷屏。
func (r *Runtime) handleFlood(ctx context.Context, settings store.BotSettings, contact store.ContactRow) {
	// 清空窗口，否则接下来每一条消息都会被判为刷屏
	r.floodMu.Lock()
	delete(r.floodWindow, contact.ID)
	r.floodMu.Unlock()

	outcome, err := r.sanction.Apply(ctx, sanction.ViolationInput{
		BotID:     r.botID,
		ContactID: contact.ID,
		Severity:  1,
		Reason:    domain.ReasonFlood,
	})
	if err != nil {
		r.log.Error("处理刷屏处罚失败", "contactId", contact.ID, "err", err)
		return
	}

	r.log.Warn("检测到刷屏", "contactId", contact.ID, "escalated", outcome.Escalated)

	if !settings.NotifyAdmins {
		return
	}
	topic, _, err := r.ensureTopic(ctx, settings, contact)
	if err != nil {
		return
	}
	text := fmt.Sprintf(
		"🌊 **疑似刷屏**\n\n用户 %s（`#%d`）在 3 秒内连续发送了超过 %d 条消息。\n当前违规分：**%d**",
		contact.DisplayName(), contact.TgUserID, settings.FloodThreshold, outcome.Score)
	_, _ = r.sendToTopic(ctx, topic.MessageThreadID, text, nil, false)
}

// relayToTopic 把消息转发进话题。
func (r *Runtime) relayToTopic(ctx context.Context, topic store.TopicRow, messages []*tgapi.Message) error {
	if len(messages) > 1 {
		return r.relayAlbum(ctx, topic, messages)
	}
	return r.relaySingle(ctx, topic, messages[0])
}

// relaySingle 转发单条消息。
//
// 用 copyMessage 而不是重新 sendMessage：它保留了原始媒体与格式，
// 也会带上转发来源，且不需要我们经手文件内容。
func (r *Runtime) relaySingle(ctx context.Context, topic store.TopicRow, m *tgapi.Message) error {
	copied, err := r.api.CopyMessage(ctx, r.adminGroupID, m.Chat.ID, m.MessageID, topic.MessageThreadID)
	if err != nil {
		return err
	}

	row, err := r.db.RecordMessage(ctx, store.RecordMessageInput{
		BotID:            r.botID,
		TopicID:          topic.ID,
		Direction:        domain.DirUserToAdmin,
		SourceChatID:     m.Chat.ID,
		TgMessageID:      m.MessageID,
		DestChatID:       &r.adminGroupID,
		RelayedMessageID: &copied.MessageID,
		Content:          extractContent(m),
	})
	if err != nil {
		return err
	}

	r.publishMessage(ctx, row, bus.EventMessageNew)
	return nil
}

// relayAlbum 转发一个相册（媒体组）。
//
// copyMessages 一次最多 100 条，且**必须**整组一起发才能保持相册形态 ——
// 逐条 copyMessage 会被 Telegram 渲染成 N 条独立消息，视觉上完全走样。
func (r *Runtime) relayAlbum(ctx context.Context, topic store.TopicRow, messages []*tgapi.Message) error {
	if len(messages) > 100 {
		messages = messages[:100]
	}

	ids := make([]int64, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.MessageID)
	}

	copied, err := r.api.CopyMessages(ctx, r.adminGroupID, messages[0].Chat.ID, ids, topic.MessageThreadID)
	if err != nil {
		return err
	}

	for i, m := range messages {
		var relayedID *int64
		if i < len(copied) {
			id := copied[i].MessageID
			relayedID = &id
		}

		destChat := r.adminGroupID
		row, err := r.db.RecordMessage(ctx, store.RecordMessageInput{
			BotID:            r.botID,
			TopicID:          topic.ID,
			Direction:        domain.DirUserToAdmin,
			SourceChatID:     m.Chat.ID,
			TgMessageID:      m.MessageID,
			DestChatID:       &destChat,
			RelayedMessageID: relayedID,
			Content:          extractContent(m),
		})
		if err != nil {
			r.log.Warn("记录相册消息失败", "err", err)
			continue
		}
		r.publishMessage(ctx, row, bus.EventMessageNew)
	}
	return nil
}

// notifyRelayFailure 转发失败时告诉管理员，而不是让消息凭空消失。
func (r *Runtime) notifyRelayFailure(ctx context.Context, topic store.TopicRow, cause error) {
	hint := "常见原因：机器人不是管理群管理员、缺少 can_delete_messages 权限、或消息类型不支持转发。"

	var apiErr *tgapi.APIError
	if errors.As(cause, &apiErr) {
		switch apiErr.Code {
		case 400:
			hint = "常见原因：话题已被删除，或消息类型不支持转发。"
		case 403:
			hint = "机器人已不在管理群里，或被取消了管理员权限。"
		}
	}

	text := fmt.Sprintf("⚠️ **消息转发失败**\n\n`%s`\n\n%s",
		truncate(cause.Error(), 200), hint)
	_, _ = r.sendToTopic(ctx, topic.MessageThreadID, text, nil, false)
}

// handleStart 处理私聊里的 /start —— 回欢迎语并建档。
func (r *Runtime) handleStart(ctx context.Context, m *tgapi.Message) {
	if m.From == nil {
		return
	}

	settings, err := r.db.GetBotSettings(ctx, r.botID)
	if err != nil {
		return
	}

	contact, err := r.db.UpsertContact(ctx, r.botID, profileOf(m.From))
	if err != nil {
		return
	}
	if contact.IsBlocked {
		return
	}

	text := renderTemplate(settings.GreetingText, map[string]string{
		"name":      m.From.DisplayName(),
		"username":  atPrefix(m.From.Username),
		"id":        itoa(m.From.ID),
		"firstName": m.From.FirstName,
		"lastName":  m.From.LastName,
		"botName":   r.botName,
	})
	r.sendToUser(ctx, m.From.ID, text)

	// /start 是唯一一个「用户还没说话就已经需要建话题」的时机，
	// 先把话题备好，管理员就能在用户开口前看到这个人。
	topic, created, err := r.ensureTopic(ctx, settings, contact)
	if err != nil {
		r.log.Error("为用户创建话题失败", "contactId", contact.ID, "err", err)
		return
	}
	if created {
		_ = r.bumpStats(ctx, store.StatDelta{TopicsCreated: 1})
		r.publishSessionEvent(ctx, topic.ID, bus.EventSessionCreated)
	}
	_ = r.db.TouchTopic(ctx, topic.ID)
}

// ────────────────────────────── 管理员回复 ──────────────────────────────

// relayAdminReply 把管理员在话题里的回复转发给用户。
func (r *Runtime) relayAdminReply(ctx context.Context, topic store.TopicRow, m *tgapi.Message) {
	settings, err := r.db.GetBotSettings(ctx, r.botID)
	if err != nil {
		return
	}

	contact, err := r.db.GetContact(ctx, topic.ContactID)
	if err != nil {
		r.log.Warn("话题对应的联系人已不存在", "topicId", topic.ID)
		return
	}

	// 被拉黑的用户：告诉管理员而不是默默丢弃 —— 否则管理员会以为
	// 「消息发出去了但对方没回」，反复重发。
	if contact.IsBlocked {
		_, _ = r.sendToTopic(ctx, topic.MessageThreadID,
			"🚫 **该用户已被拉黑**，你的消息没有发送给他。\n如需恢复通信，请点击置顶消息里的「解除拉黑」。", nil, false)
		return
	}

	// 规则在此**仅审计不处罚** —— 规则是给终端用户定的，
	// 让管理员被自己的规则静默掉是荒谬的。
	raw, normalized := buildHaystacks(m)
	if hits, err := r.engine.Evaluate(ctx, rulesContext(r.botID, "admin", raw, normalized), settings.RulesEnabled); err == nil && len(hits) > 0 {
		_ = r.applyRuleHits(ctx, hitContext{
			settings:        settings,
			contact:         contact,
			scope:           "admin",
			sourceChatID:    m.Chat.ID,
			sourceMessageID: m.MessageID,
			topicID:         &topic.ID,
			threadID:        &topic.MessageThreadID,
			text:            rawText(m),
		}, hits)
	}

	// 引用回复：管理员长按某条用户消息回复时，把被引的内容一并带过去，
	// 用户才知道自己在回答哪一句。
	quoted := r.resolveQuoted(ctx, topic, m)

	if quoted != "" && m.Text != "" {
		text := "> " + strings.ReplaceAll(quoted, "\n", "\n> ") + "\n\n" + m.Text
		sent, messageID := r.sendToUser(ctx, contact.TgUserID, text)
		if !sent {
			r.reportUnreachable(ctx, topic, contact)
			return
		}
		// 这条是我们**自己拼出来的文本**，不是 copyMessage 的副本，
		// 所以只能手工落库。
		content := extractContent(m)
		content.Text = &text
		row, err := r.db.RecordMessage(ctx, store.RecordMessageInput{
			BotID:            r.botID,
			TopicID:          topic.ID,
			Direction:        domain.DirAdminToUser,
			SourceChatID:     m.Chat.ID,
			TgMessageID:      m.MessageID,
			DestChatID:       &contact.TgUserID,
			RelayedMessageID: &messageID,
			Content:          content,
			SenderLabel:      senderLabel(m),
		})
		if err == nil {
			r.publishMessage(ctx, row, bus.EventMessageNew)
		}
	} else {
		copied, err := r.api.CopyMessage(ctx, contact.TgUserID, m.Chat.ID, m.MessageID, 0)
		if err != nil {
			r.reportUnreachable(ctx, topic, contact)
			return
		}
		destChat := contact.TgUserID
		row, err := r.db.RecordMessage(ctx, store.RecordMessageInput{
			BotID:            r.botID,
			TopicID:          topic.ID,
			Direction:        domain.DirAdminToUser,
			SourceChatID:     m.Chat.ID,
			TgMessageID:      m.MessageID,
			DestChatID:       &destChat,
			RelayedMessageID: &copied.MessageID,
			Content:          extractContent(m),
			SenderLabel:      senderLabel(m),
		})
		if err == nil {
			r.publishMessage(ctx, row, bus.EventMessageNew)
		}
	}

	_ = r.db.TouchTopic(ctx, topic.ID)
	_ = r.bumpStats(ctx, store.StatDelta{MessagesOut: 1})
	r.publishSession(ctx, topic.ID)
}

// resolveQuoted 找出被回复的那条消息在用户侧对应的原文。
//
// 只有「回复一条我们转发过的消息」才成立 —— 管理员回复自己的话、
// 或回复告警卡片，都不该带引用。
func (r *Runtime) resolveQuoted(ctx context.Context, topic store.TopicRow, m *tgapi.Message) string {
	if m.ReplyToMessage == nil {
		return ""
	}

	mapped, err := r.db.FindMessageByDest(ctx, m.Chat.ID, m.ReplyToMessage.MessageID)
	if err != nil || mapped.TopicID != topic.ID {
		return ""
	}
	if mapped.Direction != domain.DirUserToAdmin {
		return ""
	}

	text := deref(mapped.Text)
	if text == "" {
		text = deref(mapped.Caption)
	}
	return truncate(text, 200)
}

// reportUnreachable 发送失败时在话题里说明原因。
func (r *Runtime) reportUnreachable(ctx context.Context, topic store.TopicRow, contact store.ContactRow) {
	if !contact.IsUnreachable {
		if err := r.db.SetContactUnreachable(ctx, r.botID, contact.TgUserID, true); err != nil {
			r.log.Warn("标记联系人不可达失败", "err", err)
		}
	}
	_, _ = r.sendToTopic(ctx, topic.MessageThreadID,
		"📵 **消息发送失败**：对方可能已经屏蔽了这个机器人。", nil, false)
}

// ────────────────────────────── 编辑镜像 ──────────────────────────────

// handleEditedMessage 把编辑同步到对面。
func (r *Runtime) handleEditedMessage(ctx context.Context, m *tgapi.Message) {
	settings, err := r.db.GetBotSettings(ctx, r.botID)
	if err != nil || !settings.MirrorEdits {
		return
	}

	mapped, err := r.db.FindMessageBySource(ctx, m.Chat.ID, m.MessageID)
	if err != nil || mapped.BotID != r.botID {
		return
	}
	if mapped.DestChatID == nil || mapped.RelayedMessageID == nil {
		return
	}

	var text, caption *string
	if m.Text != "" {
		text = &m.Text
	}
	if m.Caption != "" {
		caption = &m.Caption
	}
	// 纯媒体消息没有可编辑的文本，Telegram 侧改的只是文件本身 —— 镜像不了
	if text == nil && caption == nil {
		return
	}

	if text != nil {
		err = r.api.EditMessageText(ctx, *mapped.DestChatID, *mapped.RelayedMessageID, *text)
	} else {
		err = r.api.EditMessageCaption(ctx, *mapped.DestChatID, *mapped.RelayedMessageID, *caption)
	}
	if err != nil {
		// 常见原因：目标消息太旧、或内容与原来完全相同
		// （Telegram 会报 "message is not modified"）。两者都不值得打断流程。
		r.log.Debug("镜像编辑失败", "messageRowId", mapped.ID, "err", err)
		return
	}

	if err := r.db.MarkMessageEdited(ctx, mapped.ID, text, caption); err != nil {
		r.log.Warn("更新消息记录失败", "err", err)
		return
	}

	mapped.Text = text
	mapped.Caption = caption
	r.publishMessage(ctx, mapped, bus.EventMessageUpdated)
	r.publishSession(ctx, mapped.TopicID)
}

// ────────────────────────────── 事件广播 ──────────────────────────────

func (r *Runtime) publishMessage(ctx context.Context, row store.MessageRow, event string) {
	media, err := r.db.GetMessageMedia(ctx, row.ID)
	if err != nil {
		media = nil
	}
	r.bus.Publish(bus.TopicChannel(row.TopicID), event, row.ToDTO(media))
}

func (r *Runtime) publishSession(ctx context.Context, topicID int64) {
	r.publishSessionEvent(ctx, topicID, bus.EventSessionUpdated)
}

func (r *Runtime) publishSessionEvent(ctx context.Context, topicID int64, event string) {
	summary, err := r.db.GetSessionSummary(ctx, topicID)
	if err != nil {
		return
	}
	// 广播的是**查询后的真实对象**而不是本地拼的：前端拿它直接替换列表行，
	// 本地拼装一旦与查询层有出入，就会出现「刷新一下页面就变了样」的诡异 bug。
	r.bus.Publish(bus.BotChannel(summary.BotID), event, summary)
}

func (r *Runtime) bumpStats(ctx context.Context, delta store.StatDelta) error {
	return r.db.BumpStats(ctx, r.loc, r.botID, delta)
}

// ────────────────────────────── 辅助 ──────────────────────────────

func profileOf(u *tgapi.User) store.UserProfile {
	return store.UserProfile{
		ID:           u.ID,
		Username:     u.Username,
		FirstName:    u.FirstName,
		LastName:     u.LastName,
		LanguageCode: u.LanguageCode,
	}
}

func senderLabel(m *tgapi.Message) *string {
	if m.From == nil {
		label := "管理员"
		return &label
	}
	label := m.From.DisplayName()
	if m.From.Username != "" {
		label = "@" + m.From.Username
	}
	return &label
}

func sameExpiry(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// muteState 缓存每个联系人最近一次被提示过的禁言解禁时刻。
type muteState struct {
	mu sync.Mutex
	m  map[int64]*int64
}

// setRelayError 把中继失败的原因记到机器人行上，供面板显示。
//
// 这个字段存在的唯一理由：中继失败原本只写进容器日志，面板上完全看不出来 ——
// 管理员看到的现象是「用户发了消息，但会话列表里什么都没有」，
// 而真正的原因（没绑群、机器人不是管理员、群没开 Topics）一条都看不到。
// 他只能去翻容器日志，而很多人根本不知道要去看那里。
func (r *Runtime) setRelayError(ctx context.Context, cause error) {
	msg := cause.Error()
	runes := []rune(msg)
	if len(runes) > 300 {
		msg = string(runes[:300]) + "…"
	}

	// 用 WithoutCancel：中继失败时 ctx 可能已经被取消（handler 超时），
	// 而「记下失败原因」这件事必须完成 —— 否则面板上永远看不到这条线索。
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	if err := r.db.SetRelayError(writeCtx, r.botID, msg); err != nil {
		r.log.Warn("记录中继错误失败", "err", err)
	}
}

// clearRelayError 在一次成功中继后清掉错误标记。
//
// 只有成功才清 —— 错误标记表示的是「当前状态」，不是「历史事件」。
// 留着一条早就修好的错误会让人一直以为它是坏的。
func (r *Runtime) clearRelayError(ctx context.Context) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()

	if err := r.db.ClearRelayError(writeCtx, r.botID); err != nil {
		r.log.Warn("清除中继错误失败", "err", err)
	}
}
