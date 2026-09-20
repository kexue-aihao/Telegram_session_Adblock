package api

import (
	"context"
	"net/http"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/secret"
	"github.com/tgs/server/internal/store"
)

// 单管理员面板的鉴权。
//
// 会话用「随机 token + 库中存哈希」而不是 JWT：面板要能**主动注销**
// 某个会话（改了密码就该踢掉所有旧会话），而无状态 token 做不到这件事。
// 存哈希则保证库被读走也无法直接冒用。

const (
	// sessionCookie 是会话 Cookie 名。
	sessionCookie = "tgs_session"
	// sessionTTL 是会话有效期。
	sessionTTL = 7 * 24 * time.Hour
)

// authContext 是挂在请求上的已认证身份。
type authContext struct {
	AdminID   int64
	Username  string
	SessionID int64
}

type authKeyType struct{}

var authKey authKeyType

// authFrom 从请求上下文里取已认证身份。
func authFrom(r *http.Request) (authContext, bool) {
	ac, ok := r.Context().Value(authKey).(authContext)
	return ac, ok
}

// requireAuth 是鉴权中间件。
//
// 放在中间件而不是在每个 handler 里各查一遍 —— 漏掉一处就是一个
// 未鉴权接口，而这种遗漏在代码评审里极难被发现。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "未登录")
			return
		}

		session, err := s.db.GetSession(r.Context(), cookie.Value)
		if err != nil {
			// 会话已失效（过期 / 被踢）：顺手清掉 Cookie，
			// 前端就不必自己判断了
			s.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "会话已过期，请重新登录")
			return
		}

		ac := authContext{AdminID: session.AdminID, Username: session.Username, SessionID: session.ID}
		next(w, r.WithContext(context.WithValue(r.Context(), authKey, ac)))
	}
}

// ────────────────────────────── 登录 ──────────────────────────────

// loginRequest 是登录请求体。
type loginRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "请输入密码")
		return
	}

	ip := clientIP(r)
	if !s.rateLimiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
		return
	}

	// 先看是不是已锁定 —— 锁定期间连哈希校验都不做，
	// 免得给攻击者一个免费的「密码是否正确」的时序侧信道。
	failures, err := s.db.CountRecentFailures(r.Context(), ip, s.loginWindow)
	if err != nil {
		s.log.Warn("读取登录失败次数失败", "err", err)
	}
	if failures >= s.maxLoginAttempts {
		s.recordLoginAudit(r.Context(), ip, "login.locked", nil)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":               false,
			"session":          nil,
			"error":            "尝试次数过多，请稍后再试",
			"lockedForSeconds": int(s.loginWindow.Seconds()),
			"attemptsLeft":     0,
		})
		return
	}

	admin, err := s.db.GetAdmin(r.Context())
	if err != nil {
		// 管理员账号不存在时也走一遍**真实的**哈希校验，让响应时间与
		// 「密码错误」一致 —— 否则攻击者能靠响应快慢判断账号是否存在。
		//
		// 必须用真实生成的哈希：写死的假哈希会因为解析失败而立刻返回，
		// 反而让时间差更明显。
		_, _ = secret.VerifyPassword(s.dummyHash(), req.Password)
		s.handleLoginFailure(w, r, ip, failures)
		return
	}

	ok, verifyErr := secret.VerifyPassword(admin.PasswordHash, req.Password)
	if verifyErr != nil {
		s.log.Error("密码哈希损坏", "adminId", admin.ID, "err", verifyErr)
	}
	if !ok {
		s.handleLoginFailure(w, r, ip, failures)
		return
	}

	// 登录成功
	token, err := secret.RandomToken(32)
	if err != nil {
		s.respondError(w, err)
		return
	}

	ua := r.UserAgent()
	if err := s.db.CreateSession(r.Context(), admin.ID, token, &ip, &ua, sessionTTL); err != nil {
		s.respondError(w, err)
		return
	}
	if err := s.db.RecordLoginAttempt(r.Context(), ip, true); err != nil {
		s.log.Warn("记录登录尝试失败", "err", err)
	}

	username := admin.Username
	s.recordLoginAudit(r.Context(), ip, "login.success", &username)
	s.setSessionCookie(w, token)

	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"session": map[string]any{
			"username":  admin.Username,
			"createdAt": now.UnixMilli(),
			"expiresAt": now.Add(sessionTTL).UnixMilli(),
			"ip":        ip,
			"userAgent": ua,
		},
		"error":            nil,
		"lockedForSeconds": nil,
		"attemptsLeft":     s.maxLoginAttempts,
	})
}

func (s *Server) handleLoginFailure(w http.ResponseWriter, r *http.Request, ip string, priorFailures int) {
	if err := s.db.RecordLoginAttempt(r.Context(), ip, false); err != nil {
		s.log.Warn("记录登录失败尝试出错", "err", err)
	}
	s.recordLoginAudit(r.Context(), ip, "login.failure", nil)

	left := s.maxLoginAttempts - priorFailures - 1
	if left < 0 {
		left = 0
	}

	// 刻意不区分「密码错误」与「账号不存在」：面板是单管理员的，
	// 区分这两者只会给攻击者提供信息。
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"ok":               false,
		"session":          nil,
		"error":            "密码错误",
		"lockedForSeconds": nil,
		"attemptsLeft":     left,
	})
}

// dummyHash 生成一次性的「等时哈希」，只用于账号不存在时对齐响应时间。
// 缓存起来：生成一次 Argon2id 要几十毫秒，没必要每次都算。
func (s *Server) dummyHash() string {
	s.dummyMu.Lock()
	defer s.dummyMu.Unlock()

	if s.dummy == "" {
		hash, err := secret.HashPassword("timing-equalizer-not-a-real-password")
		if err != nil {
			// 极端情况下回落到一个明显无效的串 —— 那时响应会变快，
			// 但这只发生在 Argon2 本身不可用时，进程早就该报警了。
			return "$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}
		s.dummy = hash
	}
	return s.dummy
}

// ────────────────────────────── 会话查询与注销 ──────────────────────────────

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusOK, map[string]any{"session": nil})
		return
	}

	session, err := s.db.GetSession(r.Context(), cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"session": nil})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session": map[string]any{
			"username":  session.Username,
			"createdAt": session.CreatedAt,
			"expiresAt": session.ExpiresAt,
			"ip":        session.IP,
			"userAgent": session.UserAgent,
		},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	ac, _ := authFrom(r)
	if err := s.db.RevokeSession(r.Context(), ac.SessionID); err != nil {
		s.log.Warn("注销会话失败", "err", err)
	}
	s.clearSessionCookie(w)

	username := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &username, "logout", nil, nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}

	ac, _ := authFrom(r)

	admin, err := s.db.GetAdmin(r.Context())
	if err != nil {
		s.respondError(w, err)
		return
	}

	ok, _ := secret.VerifyPassword(admin.PasswordHash, req.CurrentPassword)
	if !ok {
		writeError(w, http.StatusBadRequest, "当前密码不正确")
		return
	}

	if err := secret.PasswordPolicyError(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.UpdateAdminPassword(r.Context(), admin.ID, req.NewPassword); err != nil {
		s.respondError(w, err)
		return
	}

	// 踢掉**其它**会话而保留当前这一个：刚改完密码就被登出，
	// 会让人以为改失败了。其它设备上的会话必须失效，那才是改密码的意义。
	revoked, err := s.db.RevokeAllSessions(r.Context(), ac.SessionID)
	if err != nil {
		s.log.Warn("注销旧会话失败", "err", err)
	}

	username := ac.Username
	s.writeAudit(r.Context(), r, domain.ActorAdmin, &username, "password.changed", nil, nil,
		map[string]any{"revokedSessions": revoked})

	s.log.Info("管理员密码已更新", "adminId", admin.ID, "revokedSessions", revoked)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revokedSessions": revoked})
}

// ────────────────────────────── Cookie ──────────────────────────────

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// secure 在生产必须为 true；开发期必须为 false ——
		// 本地是 http，带 secure 的 Cookie 根本不会被浏览器存下，
		// 表现为「登录成功但立刻又跳回登录页」，很难排查。
		Secure:   s.isProd,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isProd,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// sessionToken 从请求里取出会话 token，供 WebSocket 握手复用。
func sessionToken(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

// ────────────────────────────── 审计辅助 ──────────────────────────────

// writeAudit 写一条操作审计。失败只记日志 —— 审计是旁路。
func (s *Server) writeAudit(
	ctx context.Context,
	r *http.Request,
	actorType string,
	actorID *string,
	action string,
	targetType *string,
	targetID *string,
	detail map[string]any,
) {
	var ip *string
	if r != nil {
		v := clientIP(r)
		ip = &v
	}

	id, err := s.db.RecordAudit(ctx, store.AuditInput{
		ActorType:  actorType,
		ActorID:    actorID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		IP:         ip,
	})
	if err != nil {
		s.log.Error("写审计日志失败（已忽略，不影响主流程）", "action", action, "err", err)
		return
	}

	s.bus.Publish("", "audit.new", map[string]any{
		"id":        id,
		"action":    action,
		"actorType": actorType,
		"createdAt": time.Now().UnixMilli(),
	})
}

// recordLoginAudit 记录登录相关的审计。
func (s *Server) recordLoginAudit(ctx context.Context, ip, action string, username *string) {
	_, err := s.db.RecordAudit(ctx, store.AuditInput{
		ActorType: domain.ActorAdmin,
		ActorID:   username,
		Action:    action,
		IP:        &ip,
	})
	if err != nil {
		s.log.Debug("记录登录审计失败", "action", action, "err", err)
	}
}

// clientIP 取真实客户端 IP。
//
// 只看 X-Forwarded-For 的第一段：反代链路上后面几段是中间代理的地址。
// 注意这里信任该头是以「部署在可信反代之后」为前提的 —— README 里
// 明确说明了这一点。
func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if idx := indexByte(forwarded, ','); idx > 0 {
			return trimSpace(forwarded[:idx])
		}
		return trimSpace(forwarded)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return trimSpace(real)
	}

	host := r.RemoteAddr
	if idx := lastIndexByte(host, ':'); idx > 0 {
		return host[:idx]
	}
	return host
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
