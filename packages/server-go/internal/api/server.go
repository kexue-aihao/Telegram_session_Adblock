// Package api 是 HTTP 接口层。
//
// 用标准库的 net/http + Go 1.22 起增强的 ServeMux（支持 "GET /api/bots/{id}"
// 这样的模式），不引入任何路由库：路由表就是代码里那几十行注册，
// 读一遍就能看全，而第三方路由器的中间件语义往往要翻文档才敢确认。
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tgs/server/internal/bot"
	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/store"
)

// Server 持有全部 HTTP 依赖。
type Server struct {
	db       *store.Store
	engine   *rules.Engine
	sanction *sanction.Engine
	bots     *bot.Manager
	bus      *bus.Bus
	log      *slog.Logger
	loc      *time.Location

	sessionSecret []byte
	masterKey     []byte
	isProd        bool
	startedAt     time.Time
	webDist       string

	// 登录限流参数
	maxLoginAttempts int
	loginWindow      time.Duration
	rateLimiter      *ipRateLimiter

	// 等时哈希的缓存：账号不存在时也要跑一次真实的 Argon2id，
	// 让响应时间与「密码错误」一致。生成一次要几十毫秒，缓存起来。
	dummyMu sync.Mutex
	dummy   string

	hub *wsHub
}

// Options 是构造 Server 的依赖。
type Options struct {
	DB            *store.Store
	Engine        *rules.Engine
	Sanction      *sanction.Engine
	Bots          *bot.Manager
	Bus           *bus.Bus
	Log           *slog.Logger
	Location      *time.Location
	SessionSecret []byte
	MasterKey     []byte
	IsProd        bool
	WebDist       string
}

// New 构造 HTTP 服务。
func New(opts Options) *Server {
	s := &Server{
		db:               opts.DB,
		engine:           opts.Engine,
		sanction:         opts.Sanction,
		bots:             opts.Bots,
		bus:              opts.Bus,
		log:              opts.Log,
		loc:              opts.Location,
		sessionSecret:    opts.SessionSecret,
		masterKey:        opts.MasterKey,
		isProd:           opts.IsProd,
		webDist:          opts.WebDist,
		startedAt:        time.Now(),
		maxLoginAttempts: 5,
		loginWindow:      15 * time.Minute,
		rateLimiter:      newIPRateLimiter(10, time.Minute),
	}
	s.hub = newWSHub(s)
	return s
}

// Handler 组装完整的路由表。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ── 免鉴权 ────────────────────────────────────────────────
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/health/ready", s.handleReady)
	mux.HandleFunc("POST /api/health/client-error", s.handleClientError)

	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("POST /api/auth/password", s.requireAuth(s.handleChangePassword))

	// ── 机器人 ────────────────────────────────────────────────
	mux.HandleFunc("GET /api/bots", s.requireAuth(s.handleListBots))
	mux.HandleFunc("POST /api/bots", s.requireAuth(s.handleCreateBot))
	mux.HandleFunc("POST /api/bots/validate", s.requireAuth(s.handleValidateToken))
	mux.HandleFunc("POST /api/bots/check-group", s.requireAuth(s.handleCheckGroup))
	mux.HandleFunc("GET /api/bots/{id}", s.requireAuth(s.handleGetBot))
	mux.HandleFunc("PATCH /api/bots/{id}", s.requireAuth(s.handleUpdateBot))
	mux.HandleFunc("DELETE /api/bots/{id}", s.requireAuth(s.handleDeleteBot))
	mux.HandleFunc("POST /api/bots/{id}/reload", s.requireAuth(s.handleReloadBot))
	mux.HandleFunc("POST /api/bots/{id}/check-group", s.requireAuth(s.handleRecheckGroup))
	mux.HandleFunc("GET /api/bots/{id}/settings", s.requireAuth(s.handleGetBotSettings))
	mux.HandleFunc("PATCH /api/bots/{id}/settings", s.requireAuth(s.handleUpdateBotSettings))
	mux.HandleFunc("GET /api/bots/{id}/impact", s.requireAuth(s.handleBotImpact))

	// ── 会话 ──────────────────────────────────────────────────
	mux.HandleFunc("GET /api/sessions", s.requireAuth(s.handleListSessions))
	mux.HandleFunc("GET /api/sessions/{id}", s.requireAuth(s.handleSessionDetail))
	mux.HandleFunc("GET /api/sessions/{id}/messages", s.requireAuth(s.handleListMessages))
	mux.HandleFunc("POST /api/sessions/{id}/messages", s.requireAuth(s.handleSendAsAdmin))
	mux.HandleFunc("POST /api/sessions/{id}/close", s.requireAuth(s.handleCloseSession))
	mux.HandleFunc("POST /api/sessions/{id}/reopen", s.requireAuth(s.handleReopenSession))
	mux.HandleFunc("DELETE /api/sessions/{id}", s.requireAuth(s.handleDeleteSession))

	// ── 联系人 ────────────────────────────────────────────────
	mux.HandleFunc("POST /api/contacts/{id}/ban", s.requireAuth(s.handleBanContact))
	mux.HandleFunc("POST /api/contacts/{id}/unban", s.requireAuth(s.handleUnbanContact))
	mux.HandleFunc("POST /api/contacts/{id}/reset-violations", s.requireAuth(s.handleResetViolations))
	mux.HandleFunc("PATCH /api/contacts/{id}", s.requireAuth(s.handleUpdateContact))

	// ── 规则 ──────────────────────────────────────────────────
	mux.HandleFunc("GET /api/rules", s.requireAuth(s.handleListRules))
	mux.HandleFunc("POST /api/rules", s.requireAuth(s.handleCreateRule))
	mux.HandleFunc("POST /api/rules/test", s.requireAuth(s.handleTestPattern))
	mux.HandleFunc("POST /api/rules/reorder", s.requireAuth(s.handleReorderRules))
	mux.HandleFunc("POST /api/rules/validate-regex", s.requireAuth(s.handleValidateRegex))
	mux.HandleFunc("PATCH /api/rules/{id}", s.requireAuth(s.handleUpdateRule))
	mux.HandleFunc("DELETE /api/rules/{id}", s.requireAuth(s.handleDeleteRule))
	mux.HandleFunc("POST /api/rules/{id}/toggle", s.requireAuth(s.handleToggleRule))
	mux.HandleFunc("POST /api/rules/{id}/test", s.requireAuth(s.handleTestRule))

	// ── 审计 ──────────────────────────────────────────────────
	mux.HandleFunc("GET /api/audit/hits", s.requireAuth(s.handleListHits))
	mux.HandleFunc("GET /api/audit/hits/by-rule", s.requireAuth(s.handleHitsByRule))
	mux.HandleFunc("GET /api/audit/log", s.requireAuth(s.handleListAudit))
	mux.HandleFunc("GET /api/audit/actions", s.requireAuth(s.handleListAuditActions))

	// ── 统计与设置 ────────────────────────────────────────────
	mux.HandleFunc("GET /api/stats/overview", s.requireAuth(s.handleOverview))
	mux.HandleFunc("GET /api/stats/timeseries", s.requireAuth(s.handleTimeseries))
	mux.HandleFunc("GET /api/stats/by-bot", s.requireAuth(s.handleStatsByBot))
	mux.HandleFunc("GET /api/settings", s.requireAuth(s.handleGetSettings))
	mux.HandleFunc("PATCH /api/settings", s.requireAuth(s.handleUpdateSettings))

	// ── WebSocket ─────────────────────────────────────────────
	mux.HandleFunc("GET /ws", s.hub.serve)

	// ── 前端产物 ──────────────────────────────────────────────
	mux.HandleFunc("/", s.serveSPA)

	return s.withMiddleware(mux)
}

// ────────────────────────────── 中间件 ──────────────────────────────

// withMiddleware 套上访问日志、panic 恢复与安全响应头。
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			// 注意变量名不要用 rec：那会遮蔽上面的 *statusRecorder，
			// 让下面的状态码记录读到错误的对象。
			if recovered := recover(); recovered != nil {
				// panic 不该杀掉进程 —— 一个接口的 bug 不该让所有机器人停止中继
				s.log.Error("请求处理 panic", "path", r.URL.Path, "panic", recovered)
				if !rec.written {
					writeError(rec, http.StatusInternalServerError, "服务器内部错误，请查看服务端日志")
				}
			}
			s.log.Debug("请求",
				"method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration", time.Since(start))
		}()

		rec.Header().Set("X-Content-Type-Options", "nosniff")
		rec.Header().Set("Referrer-Policy", "same-origin")
		// 面板自身不嵌 iframe，也不该被别处嵌套
		rec.Header().Set("X-Frame-Options", "DENY")

		next.ServeHTTP(rec, r)
	})
}

// statusRecorder 记录响应状态码，供访问日志使用。
//
// ── 一个必须显式实现的细节 ──────────────────────────────────
//
// 包装 http.ResponseWriter 会**丢掉它的可选接口**，而 WebSocket 升级
// 依赖 http.Hijacker：升级需要把底层 TCP 连接从 net/http 手里拿走。
// 不实现 Hijacker 的包装器会让握手直接失败（返回 501 Not Implemented），
// 而错误信息完全不会提到「Hijacker」这个词 —— 这类问题极难排查。
//
// 所以这里必须把 Hijacker 与 Flusher 一并转发下去。
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.written {
		return
	}
	r.status = code
	r.written = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

// Hijack 转发给底层 ResponseWriter，WebSocket 升级靠它。
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("底层 ResponseWriter 不支持 Hijack，无法升级为 WebSocket")
	}
	// 升级之后响应就交给 WebSocket 自己管了，状态码按 101 记
	r.status = http.StatusSwitchingProtocols
	r.written = true
	return hijacker.Hijack()
}

// Flush 转发给底层，供流式响应使用。
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// ────────────────────────────── 健康检查 ──────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"service":       "tgs-server",
		"version":       Version,
		"uptimeSeconds": int(time.Since(s.startedAt).Seconds()),
		"onlineBots":    s.bots.OnlineCount(),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := s.db.Read().PingContext(ctx); err != nil {
		s.log.Error("就绪检查失败", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "database": "down", "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "database": "up", "checkedAt": time.Now().UnixMilli(),
	})
}

func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := decodeJSON(r, &payload); err == nil {
		s.log.Warn("前端上报异常", "detail", payload)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ────────────────────────────── 响应辅助 ──────────────────────────────

// Version 是服务版本，由构建时通过 -ldflags 注入：
//
//	-ldflags "-X github.com/tgs/server/internal/api.Version=1.2.3"
//
// 默认值刻意是 "dev" 而不是某个硬编码的版本号 —— 硬编码会让本地随手构建的
// 二进制与正式发布的二进制报告同一个版本，出问题时无法据此区分来源。
var Version = "dev"

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// 响应已经开始写了，改不了状态码，只能记日志
		return
	}
}

// apiError 是统一的错误响应体。
// 前端只需要处理一种错误结构，所以所有 4xx/5xx 都长这样。
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// httpError 是带状态码的业务错误。
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

func badRequest(format string, args ...any) error {
	return &httpError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &httpError{status: http.StatusNotFound, message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &httpError{status: http.StatusConflict, message: fmt.Sprintf(format, args...)}
}

// respondError 把内部错误翻译成 HTTP 响应。
// 未识别的错误一律 500 且不回传细节 —— 堆栈与 SQL 片段不该出现在浏览器里。
func (s *Server) respondError(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeError(w, he.status, he.message)
		return
	}

	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}

	s.log.Error("接口处理失败", "err", err)
	writeError(w, http.StatusInternalServerError, "服务器内部错误，请查看服务端日志")
}

// decodeJSON 解析请求体。
func decodeJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("请求体不是合法 JSON：%w", err)
	}
	return nil
}

// pathID 从路径里取出数字 id。
func pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("路径参数 %s 不是合法的 id", name)
	}
	return id, nil
}

// queryInt 读取可选的整数查询参数。
func queryInt(r *http.Request, name string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, badRequest("查询参数 %s 不是合法整数", name)
	}
	return &v, nil
}

func queryString(r *http.Request, name string) string {
	return strings.TrimSpace(r.URL.Query().Get(name))
}

func queryBool(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "1" || v == "true"
}

// queryLimit 读取分页大小，带上下界。
func queryLimit(r *http.Request, def, max int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// ────────────────────────────── 限流 ──────────────────────────────

// ipRateLimiter 是一个极简的按 IP 滑动窗口限流。
//
// 只用在登录接口上：真正的密码暴力破解防护靠的是数据库里的失败计数
// 与锁定（见 auth.go），这里的限流只是挡住明显的高频请求，
// 让日志不被灌满。
type ipRateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	entries map[string][]time.Time
}

func newIPRateLimiter(max int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		window:  window,
		max:     max,
		entries: make(map[string][]time.Time),
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)

	times := l.entries[ip]
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= l.max {
		l.entries[ip] = kept
		return false
	}
	l.entries[ip] = append(kept, now)
	return true
}

// ────────────────────────────── 前端产物 ──────────────────────────────

// serveSPA 托管前端构建产物，并为前端路由兜底。
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws") {
		writeError(w, http.StatusNotFound, "接口不存在")
		return
	}

	if s.webDist == "" {
		writeError(w, http.StatusNotFound, "前端产物未构建 —— 开发期请访问 Vite 的地址（默认 5173）")
		return
	}

	fs := http.FileServer(http.Dir(s.webDist))
	// 带 hash 的静态资源可以长期缓存，index.html 不行 ——
	// 缓存了 index.html 用户会一直拿到旧版本的页面外壳。
	if strings.HasSuffix(r.URL.Path, ".html") || r.URL.Path == "/" {
		w.Header().Set("Cache-Control", "no-cache")
	} else if strings.Contains(r.URL.Path, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	fs.ServeHTTP(w, r)
}

// 让 domain 的常量在这个文件里可见，避免各处重复写字符串字面量。
var (
	_ = domain.ActorAdmin
	_ = store.GlobalBotID
)

// itoa 是 strconv.FormatInt 的简写。
//
// 用 fmt.Sprintf("%d") 也行，但它在热路径上会走反射与接口装箱；
// 审计与响应组装里这个调用非常密集，值得为它写一行包装。
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
