package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 话题内的管理与快捷操作。
//
// 每个动作都有两条入口：命令（/close）与置顶消息上的按钮。
// 两者最终都走下面这几个 do* 函数 —— 分成两套实现早晚会出现
// 「命令版本修好了、按钮版本还有老 bug」。

const helpText = `**可用命令**

/close — 关闭当前话题
/reopen — 重新打开当前话题
/ban — 拉黑该用户并关闭话题
/unban — 解除拉黑
/reset — 清零违规分并解除全部处罚
/info — 查看该用户档案
/rules — 规则引擎概况

置顶消息上的按钮与这些命令等价。`

// handleAdminCommand 处理管理群里的命令。返回 true 表示已处理。
func (r *Runtime) handleAdminCommand(ctx context.Context, m *tgapi.Message, cmd string) bool {
	threadID := m.MessageThreadID

	// 不依赖话题的全局命令
	switch cmd {
	case "/help", "/start":
		_, _ = r.sendToTopic(ctx, threadID, helpText, nil, false)
		return true
	case "/rules":
		stats, err := r.engine.Stats(ctx)
		if err != nil {
			r.log.Warn("读取规则统计失败", "err", err)
			return true
		}
		text := fmt.Sprintf(
			"**规则引擎**\n\n规则总数：%d\n已启用：%d\n因异常被自动停用：%d\n\n在面板的「规则」页可以进行增删改与正则测试。",
			stats.Total, stats.Enabled, stats.AutoDisabled)
		_, _ = r.sendToTopic(ctx, threadID, text, nil, false)
		return true
	case "/stats":
		_, _ = r.sendToTopic(ctx, threadID, "完整统计请查看面板的仪表盘页。", nil, false)
		return true
	}

	topic, err := r.db.GetTopicByThread(ctx, r.botID, threadID)
	if err != nil {
		return false
	}

	actor := "admin"
	if m.From != nil {
		actor = m.From.DisplayName()
		if m.From.Username != "" {
			actor = "@" + m.From.Username
		}
	}

	switch cmd {
	case "/close":
		r.doClose(ctx, topic, actor)
	case "/reopen":
		r.doReopen(ctx, topic, actor)
	case "/ban":
		r.doBan(ctx, topic, actor)
	case "/unban":
		r.doUnban(ctx, topic, actor)
	case "/reset":
		r.doReset(ctx, topic, actor)
	case "/info":
		r.doInfo(ctx, topic)
	default:
		// 不认识的命令：当成普通文本走中继
		return false
	}
	return true
}

// ────────────────────────────── 动作实现 ──────────────────────────────

func (r *Runtime) doClose(ctx context.Context, topic store.TopicRow, actor string) {
	if err := r.closeTopic(ctx, topic.ID, topic.MessageThreadID); err != nil {
		r.log.Warn("关闭话题失败", "topicId", topic.ID, "err", err)
	}
	r.audit(ctx, actor, "session.closed", "topic", topic.ID, nil)
	r.publishSession(ctx, topic.ID)
}

func (r *Runtime) doReopen(ctx context.Context, topic store.TopicRow, actor string) {
	if err := r.reopenTopic(ctx, topic); err != nil {
		r.log.Warn("重开话题失败", "topicId", topic.ID, "err", err)
	}
	r.audit(ctx, actor, "session.reopened", "topic", topic.ID, nil)
	r.publishSession(ctx, topic.ID)
}

func (r *Runtime) doBan(ctx context.Context, topic store.TopicRow, actor string) {
	contact, err := r.db.GetContact(ctx, topic.ContactID)
	if err != nil {
		return
	}

	if err := r.db.SetContactBlocked(ctx, contact.ID, true); err != nil {
		r.log.Warn("拉黑用户失败", "contactId", contact.ID, "err", err)
		return
	}
	// 拉黑同时清零违规分：留着分数没有意义，而且下次解封后
	// 用户会立刻从高档位开始，那不是管理员的意图。
	if err := r.sanction.Reset(ctx, contact.ID); err != nil {
		r.log.Warn("重置违规分失败", "contactId", contact.ID, "err", err)
	}

	if err := r.closeTopic(ctx, topic.ID, topic.MessageThreadID); err != nil {
		r.log.Warn("关闭话题失败", "topicId", topic.ID, "err", err)
	}

	r.audit(ctx, actor, "contact.banned", "contact", contact.ID, map[string]any{
		"tgUserId": contact.TgUserID,
		"username": deref(contact.Username),
	})

	_, _ = r.sendToTopic(ctx, topic.MessageThreadID,
		fmt.Sprintf("🚫 已拉黑 **%s**，其消息不会再进入本话题。", contact.DisplayName()), nil, false)
	r.publishSession(ctx, topic.ID)
}

func (r *Runtime) doUnban(ctx context.Context, topic store.TopicRow, actor string) {
	contact, err := r.db.GetContact(ctx, topic.ContactID)
	if err != nil {
		return
	}

	if err := r.db.SetContactBlocked(ctx, contact.ID, false); err != nil {
		r.log.Warn("解除拉黑失败", "contactId", contact.ID, "err", err)
		return
	}
	// 用户可能在被拉黑期间换了账号设置，顺手清掉不可达标记
	if err := r.db.SetContactUnreachable(ctx, r.botID, contact.TgUserID, false); err != nil {
		r.log.Warn("清除不可达标记失败", "err", err)
	}

	if err := r.reopenTopic(ctx, topic); err != nil {
		r.log.Warn("重开话题失败", "topicId", topic.ID, "err", err)
	}

	r.audit(ctx, actor, "contact.unbanned", "contact", contact.ID, nil)
	_, _ = r.sendToTopic(ctx, topic.MessageThreadID, "♻️ 已解除拉黑。", nil, false)
	r.publishSession(ctx, topic.ID)
}

func (r *Runtime) doReset(ctx context.Context, topic store.TopicRow, actor string) {
	if err := r.sanction.Reset(ctx, topic.ContactID); err != nil {
		r.log.Warn("重置违规分失败", "contactId", topic.ContactID, "err", err)
		return
	}
	r.audit(ctx, actor, "contact.violations_reset", "contact", topic.ContactID, nil)
	_, _ = r.sendToTopic(ctx, topic.MessageThreadID, "🧹 已清零违规分并解除全部处罚。", nil, false)
	r.publishSession(ctx, topic.ID)
}

func (r *Runtime) doInfo(ctx context.Context, topic store.TopicRow) {
	contact, err := r.db.GetContact(ctx, topic.ContactID)
	if err != nil {
		return
	}

	active, _ := r.db.ListActiveSanctions(ctx, contact.ID)

	lines := []string{
		fmt.Sprintf("👤 **%s**", contact.DisplayName()),
		fmt.Sprintf("用户名：%s", orDefault(atPrefix(deref(contact.Username)), "（无）")),
		fmt.Sprintf("Telegram ID：`%d`", contact.TgUserID),
		fmt.Sprintf("违规分：**%d**", contact.ViolationScore),
		fmt.Sprintf("拉黑：%s", yesNo(contact.IsBlocked)),
		fmt.Sprintf("可达：%s", reachableText(contact.IsUnreachable)),
		fmt.Sprintf("首次接触：%s", formatMillis(contact.FirstSeenAt, r.loc)),
	}

	if len(active) > 0 {
		parts := make([]string, 0, len(active))
		for _, s := range active {
			parts = append(parts, sanctionLabel(s.Type))
		}
		lines = append(lines, "生效处罚："+strings.Join(parts, "、"))
	}
	if contact.Notes != nil && *contact.Notes != "" {
		lines = append(lines, "备注："+*contact.Notes)
	}

	_, _ = r.sendToTopic(ctx, topic.MessageThreadID, strings.Join(lines, "\n"), nil, false)
}

// audit 写一条面板操作审计。
//
// 失败只记日志 —— 审计是旁路，不能因为「日志写不进去」就让「拉黑用户」失败。
func (r *Runtime) audit(
	ctx context.Context,
	actor, action, targetType string,
	targetID int64,
	detail map[string]any,
) {
	actorCopy := actor
	targetCopy := targetType
	targetIDStr := itoa(targetID)

	id, err := r.db.RecordAudit(ctx, store.AuditInput{
		ActorType:  domain.ActorAdmin,
		ActorID:    &actorCopy,
		Action:     action,
		TargetType: &targetCopy,
		TargetID:   &targetIDStr,
		Detail:     detail,
	})
	if err != nil {
		r.log.Error("写审计日志失败（已忽略，不影响主流程）", "action", action, "err", err)
		return
	}

	r.bus.Publish(bus.ChannelGlobal, bus.EventAuditNew, map[string]any{
		"id":        id,
		"action":    action,
		"actorType": domain.ActorAdmin,
		"createdAt": nowMillis(),
	})
}

// ────────────────────────────── 小工具 ──────────────────────────────

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func reachableText(unreachable bool) string {
	if unreachable {
		return "否（已屏蔽机器人）"
	}
	return "是"
}

func sanctionLabel(tier string) string {
	switch tier {
	case domain.SanctionWarn:
		return "已警告"
	case domain.SanctionSilence:
		return "已静默"
	case domain.SanctionMute:
		return "已禁言"
	case domain.SanctionBan:
		return "已拉黑"
	}
	return tier
}

func formatMillis(ms int64, loc *time.Location) string {
	return time.UnixMilli(ms).In(loc).Format("2006-01-02 15:04")
}
