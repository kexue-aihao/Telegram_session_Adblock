package api

import (
	"net/http"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/store"
)

// ══════════════════════════ 规则 ══════════════════════════

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	botID, err := queryInt(r, "botId")
	if err != nil {
		s.respondError(w, err)
		return
	}

	all, err := s.engine.ListAll(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}

	// botId 过滤包含全局规则（botId 为 nil），因为它们对所有机器人生效
	items := make([]domain.AdRule, 0, len(all))
	for _, rule := range all {
		if botID != nil && rule.BotID != nil && *rule.BotID != *botID {
			continue
		}
		items = append(items, rule)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ruleRequest 是创建/更新规则的请求体。
type ruleRequest struct {
	BotID      *int64  `json:"botId"`
	Name       *string `json:"name"`
	Pattern    *string `json:"pattern"`
	Flags      *string `json:"flags"`
	MatchMode  *string `json:"matchMode"`
	Target     *string `json:"target"`
	Action     *string `json:"action"`
	Severity   *int    `json:"severity"`
	Priority   *int    `json:"priority"`
	IsEnabled  *bool   `json:"isEnabled"`
	Note       *string `json:"note"`
	OrderedIDs []int64 `json:"orderedIds"`
	Sample     string  `json:"sample"`
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req ruleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	in, err := buildRuleInput(req, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 保存前静态体检。
	//
	// Go 的 RE2 不存在灾难性回溯，所以这里要检查的只有「能不能编译」——
	// 但这一件事必须做：编译不过的规则存进去之后，引擎会在加载时
	// 静默跳过它，表现为「规则加了但完全没生效」，而管理员看不出原因。
	if err := rules.CheckPattern(in.Pattern, in.Flags, in.MatchMode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ruleID, err := s.db.CreateRule(r.Context(), in)
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.engine.Invalidate()

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "rule.created",
		strPtr("rule"), strPtr(itoa(ruleID)),
		map[string]any{"name": in.Name, "pattern": in.Pattern})

	rule, err := s.db.GetRule(r.Context(), ruleID)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": rule})
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	existing, err := s.db.GetRule(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	var req ruleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	in, err := buildRuleInput(req, &existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := rules.CheckPattern(in.Pattern, in.Flags, in.MatchMode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.db.UpdateRule(r.Context(), id, in); err != nil {
		s.respondError(w, err)
		return
	}
	s.engine.Invalidate()

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "rule.updated",
		strPtr("rule"), strPtr(itoa(id)), nil)

	updated, err := s.db.GetRule(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": updated})
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	existing, err := s.db.GetRule(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	// 命中审计里存的是规则快照，所以删掉规则不会让历史记录失去意义
	if err := s.db.DeleteRule(r.Context(), id); err != nil {
		s.respondError(w, err)
		return
	}
	s.engine.Invalidate()

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "rule.deleted",
		strPtr("rule"), strPtr(itoa(id)),
		map[string]any{"name": existing.Name, "pattern": existing.Pattern})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleToggleRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	var req struct {
		IsEnabled bool `json:"isEnabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	if err := s.db.SetRuleEnabled(r.Context(), id, req.IsEnabled); err != nil {
		s.respondError(w, err)
		return
	}
	s.engine.Invalidate()

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "rule.toggled",
		strPtr("rule"), strPtr(itoa(id)), map[string]any{"isEnabled": req.IsEnabled})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReorderRules(w http.ResponseWriter, r *http.Request) {
	var req ruleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if len(req.OrderedIDs) == 0 {
		writeError(w, http.StatusBadRequest, "orderedIds 不能为空")
		return
	}

	if err := s.db.ReorderRules(r.Context(), req.OrderedIDs); err != nil {
		s.respondError(w, err)
		return
	}
	s.engine.Invalidate()

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "rule.reordered",
		nil, nil, map[string]any{"order": req.OrderedIDs})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ────────────────────────────── 沙盒 ──────────────────────────────

// testRequest 是沙盒请求体。
type testRequest struct {
	Pattern   string `json:"pattern"`
	Flags     string `json:"flags"`
	MatchMode string `json:"matchMode"`
	Sample    string `json:"sample"`
}

// testResult 是沙盒结果，与前端契约一致。
type testResult struct {
	OK         bool    `json:"ok"`
	Error      *string `json:"error"`
	Matches    []match `json:"matches"`
	Truncated  bool    `json:"truncated"`
	TimedOut   bool    `json:"timedOut"`
	DurationMs float64 `json:"durationMs"`
	// NormalizedSample 是归一化后的文本 —— 前端把它高亮展示，
	// 这样「为什么『加 微 信』也被拦了」就不言自明。
	NormalizedSample *string `json:"normalizedSample"`
}

type match struct {
	Start  int      `json:"start"`
	End    int      `json:"end"`
	Text   string   `json:"text"`
	Groups []string `json:"groups"`
}

func (s *Server) handleTestPattern(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	writeJSON(w, http.StatusOK, runSandbox(req))
}

func (s *Server) handleTestRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	var req testRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	rule, err := s.db.GetRule(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, runSandbox(testRequest{
		Pattern:   rule.Pattern,
		Flags:     rule.Flags,
		MatchMode: rule.MatchMode,
		Sample:    req.Sample,
	}))
}

func (s *Server) handleValidateRegex(w http.ResponseWriter, r *http.Request) {
	var req testRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	if err := rules.CheckPattern(req.Pattern, req.Flags, req.MatchMode); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"safe": false, "reason": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"safe": true, "reason": nil})
}

// runSandbox 复用引擎的匹配函数跑一次。
//
// 复用是刻意的：沙盒的全部意义就是「所见即运行时所得」。
// 另写一套简化版匹配必然与真实行为漂移，而那种漂移只在管理员
// 保存规则之后才会暴露 —— 那时已经影响到真实用户了。
func runSandbox(req testRequest) testResult {
	if req.Flags == "" {
		req.Flags = "iu"
	}
	if req.MatchMode == "" {
		req.MatchMode = domain.MatchRegex
	}

	if err := rules.CheckPattern(req.Pattern, req.Flags, req.MatchMode); err != nil {
		msg := err.Error()
		return testResult{OK: false, Error: &msg, Matches: []match{}}
	}

	// 沙盒也走归一化，与运行时完全一致
	normalized := rules.Normalize(req.Sample)

	start := time.Now()
	result, err := rules.Run(req.Pattern, req.Flags, req.MatchMode, normalized)
	elapsed := float64(time.Since(start).Microseconds()) / 1000.0

	if err != nil {
		msg := err.Error()
		return testResult{OK: false, Error: &msg, Matches: []match{}, NormalizedSample: &normalized}
	}

	matches := make([]match, 0, len(result.Segments))
	for _, seg := range result.Segments {
		groups := seg.Groups
		if groups == nil {
			groups = []string{}
		}
		matches = append(matches, match{
			Start: seg.Start, End: seg.End, Text: seg.Text, Groups: groups,
		})
	}

	const maxMatches = 500
	truncated := len(matches) >= maxMatches
	if truncated {
		matches = matches[:maxMatches]
	}

	return testResult{
		OK:               true,
		Matches:          matches,
		Truncated:        truncated,
		DurationMs:       elapsed,
		NormalizedSample: &normalized,
	}
}

// buildRuleInput 从请求体组装规则字段。existing 为 nil 表示新建。
func buildRuleInput(req ruleRequest, existing *domain.AdRule) (store.RuleInput, error) {
	in := store.RuleInput{
		MatchMode: domain.MatchRegex,
		Target:    domain.TargetAll,
		Action:    domain.ActionDelete,
		Severity:  10,
		Priority:  100,
		IsEnabled: true,
	}

	if existing != nil {
		in.BotID = existing.BotID
		in.Name = existing.Name
		in.Pattern = existing.Pattern
		in.Flags = existing.Flags
		in.MatchMode = existing.MatchMode
		in.Target = existing.Target
		in.Action = existing.Action
		in.Severity = existing.Severity
		in.Priority = existing.Priority
		in.IsEnabled = existing.IsEnabled
		in.Note = existing.Note
	} else {
		if req.BotID != nil {
			in.BotID = req.BotID
		}
	}

	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.Pattern != nil {
		in.Pattern = *req.Pattern
	}
	if req.Flags != nil {
		in.Flags = *req.Flags
	}
	if req.MatchMode != nil {
		in.MatchMode = *req.MatchMode
	}
	if req.Target != nil {
		in.Target = *req.Target
	}
	if req.Action != nil {
		in.Action = *req.Action
	}
	if req.Severity != nil {
		in.Severity = *req.Severity
	}
	if req.Priority != nil {
		in.Priority = *req.Priority
	}
	if req.IsEnabled != nil {
		in.IsEnabled = *req.IsEnabled
	}
	if req.Note != nil {
		in.Note = req.Note
	}

	// 校验
	if in.Name == "" {
		return in, errText("规则名称不能为空")
	}
	if len([]rune(in.Name)) > 100 {
		return in, errText("规则名称过长（上限 100 字符）")
	}
	if in.Pattern == "" {
		return in, errText("匹配模式不能为空")
	}
	if in.Flags == "" {
		in.Flags = "iu"
	}
	if !validEnum(in.MatchMode, domain.MatchRegex, domain.MatchContains,
		domain.MatchWholeWord, domain.MatchCooccurrence) {
		return in, errText("匹配方式不合法：" + in.MatchMode)
	}
	if !validEnum(in.Target, domain.TargetText, domain.TargetCaption, domain.TargetTextLink,
		domain.TargetURL, domain.TargetMention, domain.TargetForward, domain.TargetAll) {
		return in, errText("匹配目标不合法：" + in.Target)
	}
	if !validEnum(in.Action, domain.ActionDelete, domain.ActionWarn, domain.ActionEscalate, domain.ActionNotify) {
		return in, errText("命中动作不合法：" + in.Action)
	}
	if in.Severity < 0 || in.Severity > 1000 {
		return in, errText("违规分需要在 0 到 1000 之间")
	}
	if in.Priority < 0 || in.Priority > 10000 {
		return in, errText("优先级需要在 0 到 10000 之间")
	}

	return in, nil
}

func validEnum(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

// errText 是一个极简的错误类型，只为在 buildRuleInput 里少写几行。
type errText string

func (e errText) Error() string { return string(e) }

// ══════════════════════════ 审计 ══════════════════════════

func (s *Server) handleListHits(w http.ResponseWriter, r *http.Request) {
	botID, err := queryInt(r, "botId")
	if err != nil {
		s.respondError(w, err)
		return
	}
	ruleID, err := queryInt(r, "ruleId")
	if err != nil {
		s.respondError(w, err)
		return
	}
	contactID, err := queryInt(r, "contactId")
	if err != nil {
		s.respondError(w, err)
		return
	}
	cursor, err := queryInt(r, "cursor")
	if err != nil {
		s.respondError(w, err)
		return
	}
	from, err := queryInt(r, "from")
	if err != nil {
		s.respondError(w, err)
		return
	}
	to, err := queryInt(r, "to")
	if err != nil {
		s.respondError(w, err)
		return
	}

	page, err := s.db.ListRuleHits(r.Context(), store.RuleHitQuery{
		BotID:     botID,
		RuleID:    ruleID,
		ContactID: contactID,
		Search:    queryString(r, "q"),
		From:      from,
		To:        to,
		Cursor:    cursor,
		Limit:     queryLimit(r, 30, 100),
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleHitsByRule(w http.ResponseWriter, r *http.Request) {
	botID, err := queryInt(r, "botId")
	if err != nil {
		s.respondError(w, err)
		return
	}

	days := queryLimit(r, 30, 365)
	since := time.Now().AddDate(0, 0, -days).UnixMilli()

	query := `
		SELECT rule_id, rule_name, COUNT(*) AS c, MAX(created_at) AS last_at
		FROM rule_hits
		WHERE created_at >= ?`
	args := []any{since}
	if botID != nil {
		query += ` AND bot_id = ?`
		args = append(args, *botID)
	}
	query += ` GROUP BY rule_id, rule_name ORDER BY c DESC LIMIT 100`

	rows, err := s.db.Read().QueryContext(r.Context(), query, args...)
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		RuleID    *int64 `json:"ruleId"`
		RuleName  string `json:"ruleName"`
		Count     int64  `json:"count"`
		LastHitAt *int64 `json:"lastHitAt"`
	}

	items := make([]row, 0, 32)
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.RuleID, &item.RuleName, &item.Count, &item.LastHitAt); err != nil {
			s.respondError(w, err)
			return
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items, "since": since})
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	cursor, err := queryInt(r, "cursor")
	if err != nil {
		s.respondError(w, err)
		return
	}

	page, err := s.db.ListAudit(r.Context(), store.AuditQuery{
		Action:    queryString(r, "action"),
		ActorType: queryString(r, "actorType"),
		Search:    queryString(r, "q"),
		Cursor:    cursor,
		Limit:     queryLimit(r, 30, 100),
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleListAuditActions(w http.ResponseWriter, r *http.Request) {
	actions, err := s.db.ListAuditActions(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}
	if actions == nil {
		actions = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": actions})
}

// ══════════════════════════ 统计与设置 ══════════════════════════

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	stats, err := s.engine.Stats(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}

	overview, err := s.db.ComputeOverview(r.Context(), s.loc, store.OverviewRules{
		Total:        int(stats.Total),
		Enabled:      int(stats.Enabled),
		AutoDisabled: int(stats.AutoDisabled),
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	botID, err := queryInt(r, "botId")
	if err != nil {
		s.respondError(w, err)
		return
	}

	target := store.GlobalBotID
	if botID != nil {
		target = *botID
	}

	points, err := s.db.GetTimeseries(r.Context(), s.loc, queryLimit(r, 14, 90), target)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

func (s *Server) handleStatsByBot(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.GetStatsByBot(r.Context(), s.loc, queryLimit(r, 7, 90))
	if err != nil {
		s.respondError(w, err)
		return
	}
	if items == nil {
		items = []store.BotStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.GetGlobalSettings(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	current, err := s.db.GetGlobalSettings(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}

	var patch map[string]any
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	merged, err := applyGlobalSettingsPatch(current, patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.db.WriteGlobalSettings(r.Context(), merged); err != nil {
		s.respondError(w, err)
		return
	}

	// 时区变了要立刻生效，否则统计会按旧时区切日
	if loc, err := time.LoadLocation(merged.Timezone); err == nil {
		s.loc = loc
	}

	ac, _ := authFrom(r)
	actor := ac.Username
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "settings.updated",
		strPtr("global"), nil, map[string]any{"keys": keys})

	writeJSON(w, http.StatusOK, merged)
}

// applyGlobalSettingsPatch 应用全局设置的补丁。
func applyGlobalSettingsPatch(current store.GlobalSettings, patch map[string]any) (store.GlobalSettings, error) {
	base, err := toMap(current)
	if err != nil {
		return current, err
	}

	// base 是 JSON 往返来的，数字全是 float64，而下面写进 patch 的数字会被
	// normalizeJSON 转成 int64。不把两边统一成同一类型的话，一处「case int64」
	// 就会把**没传的**字段当成传了个坏值 —— 表现为局部补丁永远失败。
	for key, value := range base {
		base[key] = normalizeJSON(value)
	}

	for key, value := range patch {
		if _, known := base[key]; !known {
			return current, errText("未知的设置项：" + key)
		}
		base[key] = normalizeJSON(value)
	}

	merged := current
	if v, ok := base["timezone"].(string); ok && v != "" {
		if _, err := time.LoadLocation(v); err != nil {
			return current, errText("无法识别时区：" + v)
		}
		merged.Timezone = v
	}
	if v, ok := base["auditRetentionDays"]; ok {
		switch value := v.(type) {
		case nil:
			merged.AuditRetentionDays = nil
		case int64:
			days := int(value)
			if days < 1 || days > 3650 {
				return current, errText("审计保留天数需要在 1 到 3650 之间")
			}
			merged.AuditRetentionDays = &days
		}
	}
	if v, ok := base["autoDisableOnRegexTimeout"].(bool); ok {
		merged.AutoDisableOnRegexTimeout = v
	}
	if v, ok := base["regexTimeoutMs"].(int64); ok {
		ms := int(v)
		if ms < 5 || ms > 2000 {
			return current, errText("正则超时需要在 5 到 2000 毫秒之间")
		}
		merged.RegexTimeoutMs = ms
	}

	// 允许使用管理机器人的 Telegram 用户 ID。
	//
	// 这是**安全边界**而不是偏好设置：管理机器人在 Telegram 上公开可私聊，
	// 而它能增删托管其他机器人。0 表示未配置 —— 此时所有管理命令一律拒绝，
	// 不放行「第一个来的人」。
	if v, ok := base["adminTgUserId"]; ok {
		switch value := v.(type) {
		case nil:
			merged.AdminTgUserID = 0
		case int64:
			if value < 0 {
				return current, errText("Telegram 用户 ID 是正数")
			}
			merged.AdminTgUserID = value
		default:
			return current, errText("adminTgUserId 必须是数字")
		}
	}

	return merged, nil
}
