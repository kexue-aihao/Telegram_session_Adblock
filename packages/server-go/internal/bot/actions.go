package bot

import (
	"context"
	"strings"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// 规则命中的处置执行。
//
// 与 rules 包的分工：引擎判断「命中了什么」，这里决定「因此做什么」。
// 分开的硬性理由：面板的正则沙盒要复用引擎，但绝不能因为管理员点了
// 「测试」就真的去删消息或封禁用户。

// hitContext 是一次处置的上下文。
type hitContext struct {
	settings        store.BotSettings
	contact         store.ContactRow
	scope           string // user | admin
	sourceChatID    int64
	sourceMessageID int64
	topicID         *int64
	threadID        *int64
	messageRowID    *int64
	text            string
}

// actionReport 是一次处置的结果。
type actionReport struct {
	Outcomes  []string
	Blocked   bool
	HitIDs    []int64
	Violation *sanction.Outcome
	Banned    bool
}

// applyRuleHits 执行一次命中处置。
//
// 顺序有讲究：**先删原消息，再发警告**。反过来会让用户在「收到警告」到
// 「消息被删」之间有一个窗口去截图或转发 —— 这是真实存在的对抗场景。
func (r *Runtime) applyRuleHits(ctx context.Context, hc hitContext, hits []rules.Hit) actionReport {
	report := actionReport{Outcomes: make([]string, 0, 4)}

	if len(hits) == 0 {
		return report
	}

	// 管理员的消息只审计不处罚：规则是给终端用户定的，
	// 让管理员被自己的规则静默掉是荒谬的。
	if hc.scope == "admin" {
		report.Outcomes = append(report.Outcomes, domain.OutcomeLoggedOnly)
		r.recordHits(ctx, hc, hits, report.Outcomes, &report)
		return report
	}

	actions := rules.UnionActions(hits)

	// 1. 删除用户私聊里的原消息
	if actions[domain.ActionDelete] {
		deleted := r.deleteOrigin(ctx, hc)
		report.Outcomes = append(report.Outcomes, boolOutcome(deleted, domain.OutcomeDeleted, domain.OutcomeDeleteFailed))
	}

	// 2. 阶梯处罚（仅 escalate 动作触发）
	if actions[domain.ActionEscalate] {
		// 取最高分而不是求和：三条轻规则叠成一次重罚会让阈值失去意义
		severity := rules.TotalSeverity(hits)
		var ruleID *int64
		if hits[0].Rule.ID != 0 {
			id := hits[0].Rule.ID
			ruleID = &id
		}

		outcome, err := r.sanction.Apply(ctx, sanction.ViolationInput{
			BotID:     r.botID,
			ContactID: hc.contact.ID,
			RuleID:    ruleID,
			Severity:  severity,
			Reason:    domain.ReasonRuleHit,
		})
		if err != nil {
			r.log.Error("阶梯处罚失败", "contactId", hc.contact.ID, "err", err)
			report.Outcomes = append(report.Outcomes, domain.OutcomeLoggedOnly)
		} else {
			report.Violation = &outcome
			if outcome.Escalated && outcome.CurrentType != nil {
				report.Outcomes = append(report.Outcomes, outcomeForSanction(*outcome.CurrentType))
			} else {
				report.Outcomes = append(report.Outcomes, domain.OutcomeLoggedOnly)
			}
		}
	}

	// 3. 用户侧通知：阶梯升级优先，避免一次命中发两条
	if report.Violation != nil && report.Violation.Escalated {
		if r.notifyUserOfSanction(ctx, hc, hits, report.Violation) {
			report.Outcomes = append(report.Outcomes, domain.OutcomeWarned)
		}
	} else if actions[domain.ActionWarn] {
		text := renderTemplate(hc.settings.WarnTemplate, map[string]string{
			"name":     hc.contact.DisplayName(),
			"username": deref(hc.contact.Username),
			"id":       itoa(hc.contact.TgUserID),
			"ruleName": hits[0].Rule.Name,
			"matched":  hits[0].MatchedText,
			"score":    itoa(int64(hc.contact.ViolationScore)),
			"botName":  r.botName,
		})
		if sent, _ := r.sendToUser(ctx, hc.contact.TgUserID, text); sent {
			report.Outcomes = append(report.Outcomes, domain.OutcomeWarned)
		}
	}

	// 4. 管理员侧告警卡片
	shouldNotify := hc.settings.NotifyAdmins && hc.threadID != nil &&
		(actions[domain.ActionNotify] ||
			(report.Violation != nil && report.Violation.Escalated &&
				report.Violation.Step != nil && report.Violation.Step.NotifyAdmins))

	if shouldNotify {
		if r.sendAlertCard(ctx, hc, hits, report.Outcomes, report.Violation) {
			report.Outcomes = append(report.Outcomes, domain.OutcomeNotified)
		} else {
			report.Outcomes = append(report.Outcomes, domain.OutcomeNotifyFailed)
		}
	}

	// 5. 落审计：每条命中一行（「每条都记，动作取并集」）
	r.recordHits(ctx, hc, hits, report.Outcomes, &report)

	// 6. 拉黑后关闭话题。刻意「关闭」而不是「删除」：删除会丢掉全部历史，
	//    而拉黑往往正是最需要保留证据的时候。
	if report.Violation != nil && report.Violation.CurrentType != nil &&
		*report.Violation.CurrentType == domain.SanctionBan &&
		hc.topicID != nil && hc.threadID != nil {
		report.Banned = true
		if err := r.closeTopic(ctx, *hc.topicID, *hc.threadID); err != nil {
			r.log.Warn("关闭话题失败", "topicId", *hc.topicID, "err", err)
		}
	}

	// 7. 撤回已转发进话题的副本（先转发、后新增规则命中的场景）
	if hc.messageRowID != nil && actions[domain.ActionDelete] {
		r.deleteMirror(ctx, *hc.messageRowID)
	}

	report.Blocked = report.Violation != nil && report.Violation.CurrentType != nil &&
		isBlockedTier(*report.Violation.CurrentType)

	return report
}

// recordHits 写入命中审计并自增计数。
func (r *Runtime) recordHits(
	ctx context.Context,
	hc hitContext,
	hits []rules.Hit,
	outcomes []string,
	report *actionReport,
) {
	ids := make([]int64, 0, len(hits))

	for _, hit := range hits {
		var ruleID *int64
		if hit.Rule.ID != 0 {
			id := hit.Rule.ID
			ruleID = &id
		}
		matched := hit.MatchedText
		if matched == "" {
			matched = truncate(hc.text, 200)
		}

		var excerpt *string
		if hit.NormalizedExcerpt != "" {
			excerpt = &hit.NormalizedExcerpt
		}

		hitID, err := r.db.RecordRuleHit(ctx, store.RecordRuleHitInput{
			RuleID:            ruleID,
			RuleName:          hit.Rule.Name,
			RulePattern:       hit.Rule.Pattern,
			RuleFlags:         hit.Rule.Flags,
			BotID:             r.botID,
			ContactID:         hc.contact.ID,
			TopicID:           hc.topicID,
			MessageID:         hc.messageRowID,
			MatchedText:       matched,
			NormalizedExcerpt: excerpt,
			Outcomes:          outcomes,
			Severity:          hit.Severity,
		})
		if err != nil {
			r.log.Error("写入命中审计失败", "ruleId", hit.Rule.ID, "err", err)
			continue
		}
		ids = append(ids, hitID)

		r.bus.Publish(bus.ChannelGlobal, bus.EventRuleHit, domain.RuleHit{
			ID:                hitID,
			RuleID:            ruleID,
			RuleName:          hit.Rule.Name,
			RulePattern:       hit.Rule.Pattern,
			RuleFlags:         hit.Rule.Flags,
			BotID:             r.botID,
			BotName:           r.botName,
			ContactID:         hc.contact.ID,
			ContactName:       hc.contact.DisplayName(),
			ContactUsername:   hc.contact.Username,
			TgUserID:          hc.contact.TgUserID,
			TopicID:           hc.topicID,
			ThreadID:          hc.threadID,
			MatchedText:       matched,
			NormalizedExcerpt: excerpt,
			Outcomes:          outcomes,
			Severity:          hit.Severity,
			CreatedAt:         nowMillis(),
		})
	}

	if len(ids) > 0 {
		ruleIDs := make([]int64, 0, len(hits))
		for _, hit := range hits {
			if hit.Rule.ID != 0 {
				ruleIDs = append(ruleIDs, hit.Rule.ID)
			}
		}
		if err := r.engine.BumpHitCounts(ctx, ruleIDs); err != nil {
			r.log.Warn("更新命中计数失败", "err", err)
		}
	}
	report.HitIDs = ids
}

// deleteOrigin 撤回用户私聊里的原消息。
// Bot API 明确允许删除私聊中的 incoming 消息，但有 48 小时的时间窗。
func (r *Runtime) deleteOrigin(ctx context.Context, hc hitContext) bool {
	if !hc.settings.DeleteOriginMessage {
		return false
	}
	if err := r.api.DeleteMessage(ctx, hc.sourceChatID, hc.sourceMessageID); err != nil {
		if tgapi.IsTooOldToDelete(err) {
			r.log.Debug("删除原消息失败：超过 48 小时窗口", "messageId", hc.sourceMessageID)
		} else {
			r.log.Debug("删除原消息失败", "messageId", hc.sourceMessageID, "err", err)
		}
		return false
	}
	return true
}

// deleteMirror 撤回已转发进话题的副本。
//
// 用于「先转发、后新增规则命中」的场景：规则是在消息已经进了话题之后
// 才被加上或改动的，此时话题里那份副本需要补删。
//
// 失败只记日志：副本留着不影响正确性，而删不掉最常见的原因是
// 超过 Telegram 的 48 小时窗口 —— 那不是我们能解决的问题。
func (r *Runtime) deleteMirror(ctx context.Context, messageRowID int64) {
	mapped, err := r.db.GetMessage(ctx, messageRowID)
	if err != nil {
		r.log.Debug("查不到消息记录，跳过副本撤回", "messageRowId", messageRowID, "err", err)
		return
	}
	if mapped.DestChatID == nil || mapped.RelayedMessageID == nil {
		return
	}

	if err := r.api.DeleteMessage(ctx, *mapped.DestChatID, *mapped.RelayedMessageID); err != nil {
		r.log.Debug("撤回副本失败", "messageRowId", messageRowID, "err", err)
		return
	}
	if err := r.db.MarkMessageDeleted(ctx, mapped.ID); err != nil {
		r.log.Warn("标记副本已删除失败", "err", err)
		return
	}
	r.publishMessage(ctx, mapped, bus.EventMessageDeleted)
}

// notifyUserOfSanction 按阶梯档位给用户发提示文案。
func (r *Runtime) notifyUserOfSanction(
	ctx context.Context,
	hc hitContext,
	hits []rules.Hit,
	violation *sanction.Outcome,
) bool {
	if violation.Step == nil {
		return false
	}

	// 静默刻意不通知：它的全部意义就是让对方无感知。
	// 一发提示等于告诉对方「你的消息被吞了，换个号再来」。
	if violation.Step.Type == domain.SanctionSilence {
		return false
	}

	tmpl := hc.settings.WarnTemplate
	switch violation.Step.Type {
	case domain.SanctionMute:
		tmpl = hc.settings.MuteTemplate
	case domain.SanctionBan:
		tmpl = hc.settings.BanTemplate
	}

	var expiresAt *int64
	if violation.Sanction != nil {
		expiresAt = violation.Sanction.ExpiresAt
	}

	ruleName := ""
	if len(hits) > 0 {
		ruleName = hits[0].Rule.Name
	}

	text := renderTemplate(tmpl, map[string]string{
		"name":     hc.contact.DisplayName(),
		"username": deref(hc.contact.Username),
		"id":       itoa(hc.contact.TgUserID),
		"ruleName": ruleName,
		"score":    itoa(int64(violation.Score)),
		"until":    formatUntil(expiresAt, r.loc),
		"botName":  r.botName,
	})
	if text == "" {
		return false
	}

	sent, _ := r.sendToUser(ctx, hc.contact.TgUserID, text)
	return sent
}

// sendAlertCard 发送话题内的告警卡片。
func (r *Runtime) sendAlertCard(
	ctx context.Context,
	hc hitContext,
	hits []rules.Hit,
	outcomes []string,
	violation *sanction.Outcome,
) bool {
	if hc.threadID == nil {
		return false
	}

	names := make([]string, 0, len(hits))
	for _, hit := range hits {
		names = append(names, hit.Rule.Name)
	}

	matched := ""
	if len(hits) > 0 {
		matched = hits[0].MatchedText
	}

	score := hc.contact.ViolationScore
	sanctionType := ""
	if violation != nil {
		score = violation.Score
		if violation.CurrentType != nil {
			sanctionType = *violation.CurrentType
		}
	}

	note := ""
	if len(hits) > 0 && hits[0].Rule.Note != nil {
		note = *hits[0].Rule.Note
	}

	text := renderTemplate(hc.settings.AlertCardTemplate, map[string]string{
		"name":     hc.contact.DisplayName(),
		"username": orDefault(atPrefix(deref(hc.contact.Username)), "（无用户名）"),
		"id":       itoa(hc.contact.TgUserID),
		"ruleName": strings.Join(names, "、"),
		"matched":  matched,
		"outcome":  describeOutcomes(outcomes, sanctionType),
		"score":    itoa(int64(score)),
		"botName":  r.botName,
		"reason":   note,
	})

	id, err := r.sendToTopic(ctx, *hc.threadID, text, nil, false)
	return err == nil && id != 0
}

// describeOutcomes 把处置动作翻成一句面向管理员的人话。
func describeOutcomes(outcomes []string, sanctionType string) string {
	switch sanctionType {
	case domain.SanctionBan:
		return "拉黑"
	case domain.SanctionMute:
		return "禁言"
	case domain.SanctionSilence:
		return "静默"
	case domain.SanctionWarn:
		return "警告"
	}

	labels := map[string]string{
		domain.OutcomeDeleted:      "已删除",
		domain.OutcomeDeleteFailed: "删除失败",
		domain.OutcomeNotified:     "已通知管理员",
		domain.OutcomeNotifyFailed: "通知失败",
		domain.OutcomeWarned:       "已警告",
		domain.OutcomeLoggedOnly:   "仅记录",
		domain.OutcomeRegexTimeout: "规则超时",
	}

	seen := make(map[string]bool)
	parts := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		label, ok := labels[o]
		if !ok || seen[label] {
			continue
		}
		seen[label] = true
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return "仅记录"
	}
	return strings.Join(parts, " + ")
}

func outcomeForSanction(tier string) string {
	switch tier {
	case domain.SanctionWarn:
		return domain.OutcomeWarned
	case domain.SanctionSilence:
		return domain.OutcomeSilenced
	case domain.SanctionMute:
		return domain.OutcomeMuted
	case domain.SanctionBan:
		return domain.OutcomeBanned
	}
	return domain.OutcomeLoggedOnly
}

func isBlockedTier(tier string) bool {
	for _, t := range domain.BlockingSanctions {
		if t == tier {
			return true
		}
	}
	return false
}

func boolOutcome(v bool, whenTrue, whenFalse string) string {
	if v {
		return whenTrue
	}
	return whenFalse
}
