package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tgs/server/internal/bot"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/secret"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// ══════════════════════════ 机器人 ══════════════════════════

func (s *Server) handleListBots(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListBots(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}

	items := make([]domain.Bot, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.ToDTO())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetBot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	row, err := s.db.GetBot(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	settings, err := s.db.GetBotSettings(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"bot":      row.ToDTO(),
		"settings": settings,
	})
}

// createBotRequest 是创建机器人的请求体。
type createBotRequest struct {
	Token        string `json:"token"`
	AdminGroupID *int64 `json:"adminGroupId"`
	Name         string `json:"name"`
	// Manager 把它同时设为管理机器人（可以在 Telegram 里管理其他机器人）
	Manager bool `json:"manager"`
}

func (s *Server) handleCreateBot(w http.ResponseWriter, r *http.Request) {
	var req createBotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	ac, _ := authFrom(r)

	// 整条开通流程（校验 token → 判重 → 加密入库 → 建默认设置 → 启动）
	// 委托给 bot.Manager。管理机器人在 Telegram 里走的是**同一条**路径，
	// 所以从两个入口加进来的机器人在库里的形态必然一致 ——
	// 各写一份的话，权限判断这类最不该出错的地方迟早会漂移。
	created, err := s.bots.Create(r.Context(), bot.CreateRequest{
		Token:        req.Token,
		AdminGroupID: req.AdminGroupID,
		Name:         req.Name,
		Manager:      req.Manager,
		Actor:        ac.Username,
	})
	if err != nil {
		// Create 已经把 Telegram 的错误翻译成人话了，直接透传
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.writeAudit(r.Context(), r, domain.ActorAdmin, &ac.Username, "bot.created",
		strPtr("bot"), strPtr(itoa(created.ID)),
		map[string]any{"username": created.Username, "manager": req.Manager})

	// 回带一次 token 校验结果，让向导能显示 Privacy Mode 之类的提醒
	validation, _ := bot.ValidateToken(r.Context(), req.Token)

	writeJSON(w, http.StatusCreated, map[string]any{
		"bot":        created.ToDTO(),
		"validation": validation,
	})
}

// updateBotRequest 用指针区分「不修改」与「设为零值」。
//
// 这对布尔字段尤其关键：不用指针的话，IsEnabled=false 与「没传这个字段」
// 在 Go 里无法区分，结果就是关不掉机器人。
type updateBotRequest struct {
	Name         *string `json:"name"`
	AdminGroupID *int64  `json:"adminGroupId"`
	IsEnabled    *bool   `json:"isEnabled"`
	Token        *string `json:"token"`
	// IsManager 把该机器人设为管理机器人（可在 Telegram 里直接管理其他机器人）
	IsManager *bool `json:"isManager"`
}

func (s *Server) handleUpdateBot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	var req updateBotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	existing, err := s.db.GetBot(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	fields := store.UpdateBotFields{Name: req.Name}
	needsRestart := false

	if req.AdminGroupID != nil && (existing.AdminGroupID == nil || *existing.AdminGroupID != *req.AdminGroupID) {
		fields.AdminGroupID = req.AdminGroupID

		// 顺带把群名问出来；缺权限也不该阻断保存
		if token, err := secret.Open(s.masterKey, existing.Sealed()); err == nil {
			if check, err := bot.CheckGroup(r.Context(), token, *req.AdminGroupID); err == nil {
				fields.AdminGroupTitle = check.Title
				if !check.OK {
					s.log.Warn("绑定的管理群体检未通过", "botId", id, "problems", check.Problems)
				}
			}
		}
		needsRestart = true
	}

	if req.Token != nil {
		token := strings.TrimSpace(*req.Token)
		if !bot.LooksLikeBotToken(token) {
			writeError(w, http.StatusBadRequest, "token 格式不正确")
			return
		}
		validation, err := bot.ValidateToken(r.Context(), token)
		if err != nil {
			s.respondError(w, err)
			return
		}
		if !validation.OK {
			writeError(w, http.StatusBadRequest, derefOr(validation.Error, "token 校验失败"))
			return
		}

		sealed, err := secret.Seal(s.masterKey, token)
		if err != nil {
			s.respondError(w, err)
			return
		}
		mask := secret.MaskToken(token)
		fields.Sealed = &sealed
		fields.TokenMask = &mask
		fields.TelegramID = validation.TelegramID
		needsRestart = true
	}

	if req.IsEnabled != nil {
		fields.IsEnabled = req.IsEnabled
		needsRestart = true

		ac, _ := authFrom(r)
		actor := ac.Username
		action := "bot.disabled"
		if *req.IsEnabled {
			action = "bot.enabled"
		}
		s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, action, strPtr("bot"), strPtr(itoa(id)), nil)
	}

	// 管理机器人（控制台）绑定。**必须让运行时重载** —— isManager 在构造时
	// 固化，而它决定了这个运行时是控制台还是中继机器人。不重载的话，
	// 面板上开关变了、实际路由没变，是最难排查的一类不一致。
	//
	// 绑定是唯一的：绑新的会把旧的解绑，两台运行时都要重载 ——
	// 这件事由 bots.SetManagerBot 一并处理，因此它放在最后，
	// 且替代了下面那次常规重启（运行时要读到的字段此时都已写完）。
	bindingChanged := req.IsManager != nil

	if err := s.db.UpdateBot(r.Context(), id, fields); err != nil {
		s.respondError(w, err)
		return
	}

	if bindingChanged {
		if err := s.bots.SetManagerBot(r.Context(), id, *req.IsManager); err != nil {
			s.respondError(w, err)
			return
		}

		// 审计单独记：与 bot.enabled / bot.disabled 同一套命名，
		// 审计页是按 action 过滤的，「谁拿到了控制台权限」必须能单独筛出来。
		action := "bot.manager_disabled"
		if *req.IsManager {
			action = "bot.manager_enabled"
		}
		ac, _ := authFrom(r)
		s.writeAudit(r.Context(), r, domain.ActorAdmin, &ac.Username, action,
			strPtr("bot"), strPtr(itoa(id)), nil)
	} else if needsRestart {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()

		if fields.IsEnabled != nil && !*fields.IsEnabled {
			s.bots.Stop(ctx, id)
		} else if err := s.bots.Start(ctx, id); err != nil {
			s.log.Warn("机器人重启失败，状态已更新", "botId", id, "err", err)
		}
	}

	updated, err := s.db.GetBot(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bot": updated.ToDTO()})
}

func (s *Server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	row, err := s.db.GetBot(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	s.bots.Stop(ctx, id)

	if err := s.db.DeleteBot(r.Context(), id); err != nil {
		s.respondError(w, err)
		return
	}
	// 专属规则被外键级联删掉了，但规则缓存与设置缓存还留着旧的行
	s.engine.Invalidate()

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "bot.deleted", strPtr("bot"), strPtr(itoa(id)),
		map[string]any{"username": row.Username})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReloadBot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()

	if err := s.bots.Reload(ctx, id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "bot.reloaded", strPtr("bot"), strPtr(itoa(id)), nil)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBotImpact(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}
	impact, err := s.db.GetBotImpact(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, impact)
}

// ────────────────────────────── token 校验 ──────────────────────────────

func (s *Server) handleValidateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	result, err := bot.ValidateToken(r.Context(), strings.TrimSpace(req.Token))
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCheckGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token  string `json:"token"`
		ChatID int64  `json:"chatId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	result, err := bot.CheckGroup(r.Context(), strings.TrimSpace(req.Token), req.ChatID)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRecheckGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	row, err := s.db.GetBot(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if row.AdminGroupID == nil {
		writeError(w, http.StatusBadRequest, "尚未绑定管理群")
		return
	}

	token, err := secret.Open(s.masterKey, row.Sealed())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	check, err := bot.CheckGroup(r.Context(), token, *row.AdminGroupID)
	if err != nil {
		s.respondError(w, err)
		return
	}

	if check.Title != nil && (row.AdminGroupTitle == nil || *row.AdminGroupTitle != *check.Title) {
		_ = s.db.UpdateBot(r.Context(), id, store.UpdateBotFields{AdminGroupTitle: check.Title})
	}
	writeJSON(w, http.StatusOK, check)
}

// ────────────────────────────── 设置 ──────────────────────────────

func (s *Server) handleGetBotSettings(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}
	settings, err := s.db.GetBotSettings(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateBotSettings(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	// 先读出现有设置，再把请求体里的字段合并进去。
	// 用「读-改-写」而不是让前端提交整份：前端少传一个字段就把它清空了，
	// 那是很难察觉的一类数据丢失。
	current, err := s.db.GetBotSettings(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	var patch map[string]any
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	merged, err := applySettingsPatch(current, patch)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.db.WriteBotSettings(r.Context(), merged); err != nil {
		s.respondError(w, err)
		return
	}

	ac, _ := authFrom(r)
	actor := ac.Username
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "settings.updated", strPtr("bot"), strPtr(itoa(id)),
		map[string]any{"keys": keys})

	updated, err := s.db.GetBotSettings(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// ══════════════════════════ 会话 ══════════════════════════

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	botID, err := queryInt(r, "botId")
	if err != nil {
		s.respondError(w, err)
		return
	}
	cursor, err := queryInt(r, "cursor")
	if err != nil {
		s.respondError(w, err)
		return
	}

	result, err := s.db.ListSessions(r.Context(), store.SessionQuery{
		BotID:       botID,
		Status:      queryString(r, "status"),
		Search:      queryString(r, "q"),
		FlaggedOnly: queryBool(r, "flaggedOnly"),
		Cursor:      cursor,
		Limit:       queryLimit(r, 30, 100),
	})
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	summary, err := s.db.GetSessionSummary(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	contact, err := s.db.GetContact(r.Context(), summary.ContactID)
	if err != nil {
		s.respondError(w, err)
		return
	}

	active, _ := s.db.ListActiveSanctions(r.Context(), summary.ContactID)
	sanctions := make([]domain.Sanction, 0, len(active))
	for _, row := range active {
		sanctions = append(sanctions, row.ToDTO())
	}

	recent, _ := s.db.ListRecentHits(r.Context(), summary.ContactID, 10)
	if recent == nil {
		recent = []store.RecentHit{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session":         summary,
		"activeSanctions": sanctions,
		"notes":           contact.Notes,
		"recentHits":      recent,
	})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}
	cursor, err := queryInt(r, "cursor")
	if err != nil {
		s.respondError(w, err)
		return
	}

	page, err := s.db.ListMessages(r.Context(), id, cursor, queryLimit(r, 50, 200))
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSendAsAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "消息内容不能为空")
		return
	}
	if len([]rune(req.Text)) > 4096 {
		writeError(w, http.StatusBadRequest, "消息过长（上限 4096 字符）")
		return
	}

	topic, err := s.db.GetTopic(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	contact, err := s.db.GetContact(r.Context(), topic.ContactID)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if contact.IsBlocked {
		writeError(w, http.StatusBadRequest, "该用户已被拉黑，请先解除拉黑再发送")
		return
	}

	runtime, err := s.bots.Require(topic.BotID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := runtime.SendAsAdmin(r.Context(), topic, contact, req.Text); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	ac, _ := authFrom(r)
	actor := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "message.sent", strPtr("topic"), strPtr(itoa(id)),
		map[string]any{"length": len([]rune(req.Text))})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	s.sessionAction(w, r, "close")
}

func (s *Server) handleReopenSession(w http.ResponseWriter, r *http.Request) {
	s.sessionAction(w, r, "reopen")
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	s.sessionAction(w, r, "delete")
}

func (s *Server) sessionAction(w http.ResponseWriter, r *http.Request, action string) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	topic, err := s.db.GetTopic(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	runtime, err := s.bots.Require(topic.BotID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := runtime.SessionAction(r.Context(), topic, action); err != nil {
		s.respondError(w, err)
		return
	}

	ac, _ := authFrom(r)
	actor := ac.Username
	auditAction := map[string]string{
		"close":  "session.closed",
		"reopen": "session.reopened",
		"delete": "session.deleted",
	}[action]
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, auditAction, strPtr("topic"), strPtr(itoa(id)), nil)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ══════════════════════════ 联系人 ══════════════════════════

func (s *Server) handleBanContact(w http.ResponseWriter, r *http.Request) {
	s.contactAction(w, r, "ban")
}

func (s *Server) handleUnbanContact(w http.ResponseWriter, r *http.Request) {
	s.contactAction(w, r, "unban")
}

func (s *Server) handleResetViolations(w http.ResponseWriter, r *http.Request) {
	s.contactAction(w, r, "reset")
}

func (s *Server) contactAction(w http.ResponseWriter, r *http.Request, action string) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	contact, err := s.db.GetContact(r.Context(), id)
	if err != nil {
		s.respondError(w, err)
		return
	}

	switch action {
	case "ban":
		if err := s.db.SetContactBlocked(r.Context(), id, true); err != nil {
			s.respondError(w, err)
			return
		}
	case "unban":
		if err := s.db.SetContactBlocked(r.Context(), id, false); err != nil {
			s.respondError(w, err)
			return
		}
		if err := s.db.SetContactUnreachable(r.Context(), contact.BotID, contact.TgUserID, false); err != nil {
			s.log.Warn("清除不可达标记失败", "err", err)
		}
	case "reset":
		if err := s.db.ResetViolations(r.Context(), id); err != nil {
			s.respondError(w, err)
			return
		}
	}

	ac, _ := authFrom(r)
	actor := ac.Username
	auditAction := map[string]string{
		"ban":   "contact.banned",
		"unban": "contact.unbanned",
		"reset": "contact.violations_reset",
	}[action]
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, auditAction, strPtr("contact"), strPtr(itoa(id)), nil)

	s.refreshContactSessions(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUpdateContact(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		s.respondError(w, err)
		return
	}

	var req struct {
		Notes *string `json:"notes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	if req.Notes != nil {
		if err := s.db.UpdateContactNotes(r.Context(), id, req.Notes); err != nil {
			s.respondError(w, err)
			return
		}

		ac, _ := authFrom(r)
		actor := ac.Username
		s.writeAudit(r.Context(), r, domain.ActorAdmin, &actor, "contact.notes_updated",
			strPtr("contact"), strPtr(itoa(id)), nil)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// refreshContactSessions 联系人状态变了，它名下的会话摘要也要跟着刷新。
func (s *Server) refreshContactSessions(ctx context.Context, contactID int64) {
	rows, err := s.db.Read().QueryContext(ctx,
		`SELECT id FROM topics WHERE contact_id = ?`, contactID)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	var topicIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			continue
		}
		topicIDs = append(topicIDs, id)
	}

	for _, id := range topicIDs {
		if summary, err := s.db.GetSessionSummary(ctx, id); err == nil {
			s.bus.Publish("bot:"+itoa(summary.BotID), "session.updated", summary)
		}
	}
}

// ────────────────────────────── 小工具 ──────────────────────────────

func strPtr(s string) *string { return &s }

func derefOr(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

var _ = errors.Is
var _ = tgapi.APIError{}
var _ = bot.Manager{}
