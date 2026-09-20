package bot

import (
	"context"
	"fmt"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/store"
)

// 面向前端的运行时操作。
//
// 面板在这里扮演的是「管理员的另一个客户端」：所有动作与在 Telegram 话题里
// 点按钮完全等价，走的是同一套实现，因此不会出现
// 「面板上关闭了，Telegram 里还开着」这种状态分叉。

// SendAsAdmin 以管理员身份发消息 —— 面板里直接回复用户。
func (r *Runtime) SendAsAdmin(
	ctx context.Context,
	topic store.TopicRow,
	contact store.ContactRow,
	text string,
) error {
	sent, messageID := r.sendToUser(ctx, contact.TgUserID, text)
	if !sent {
		return fmt.Errorf("发送失败：对方可能已屏蔽机器人")
	}

	destChat := contact.TgUserID
	content := domain.MessageContent{
		Type:        domain.ContentText,
		Text:        &text,
		Media:       []domain.Media{},
		HiddenLinks: []string{},
	}

	label := "面板"
	row, err := r.db.RecordMessage(ctx, store.RecordMessageInput{
		BotID:            r.botID,
		TopicID:          topic.ID,
		Direction:        domain.DirAdminToUser,
		SourceChatID:     r.botTgID,
		TgMessageID:      0, // 面板发出的消息在 Telegram 侧没有对应的源消息
		DestChatID:       &destChat,
		RelayedMessageID: &messageID,
		Content:          content,
		SenderLabel:      &label,
	})
	if err != nil {
		// 消息已经发出去了，落库失败不该让接口报错 —— 用户确实收到了。
		// 但要在日志里留痕，因为面板上会缺这一条。
		r.log.Warn("面板消息落库失败（消息已送达）", "topicId", topic.ID, "err", err)
		return nil
	}

	r.publishMessage(ctx, row, bus.EventMessageNew)
	_ = r.db.TouchTopic(ctx, topic.ID)
	_ = r.bumpStats(ctx, store.StatDelta{MessagesOut: 1})
	r.publishSession(ctx, topic.ID)
	return nil
}

// SessionAction 执行面板上的会话操作：close / reopen / delete。
func (r *Runtime) SessionAction(ctx context.Context, topic store.TopicRow, action string) error {
	switch action {
	case "close":
		if err := r.closeTopic(ctx, topic.ID, topic.MessageThreadID); err != nil {
			return err
		}
	case "reopen":
		if err := r.reopenTopic(ctx, topic); err != nil {
			return err
		}
	case "delete":
		// 真的在 Telegram 里删掉话题。删除失败**不算错误** ——
		// 管理员在面板上表达的意图是「这个会话我不要了」，
		// 本地状态仍应当跟上；否则下一次同步会把它又变成一个
		// 「正常」的会话，看起来像是删除没生效。
		if r.adminGroupID != 0 {
			if err := r.api.DeleteForumTopic(ctx, r.adminGroupID, topic.MessageThreadID); err != nil {
				r.log.Warn("删除话题失败（通常是缺少 can_delete_messages 权限）",
					"topicId", topic.ID, "err", err)
			}
		}
		if err := r.db.UpdateTopicStatus(ctx, topic.ID, domain.TopicDeleted); err != nil {
			return err
		}
		r.bus.Publish(bus.BotChannel(r.botID), bus.EventSessionDeleted, map[string]any{
			"sessionId": topic.ID,
			"botId":     r.botID,
		})
		return nil
	default:
		return fmt.Errorf("未知的会话操作：%s", action)
	}

	r.publishSession(ctx, topic.ID)
	return nil
}
