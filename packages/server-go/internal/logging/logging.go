// Package logging 提供结构化日志。
//
// 关键点是**脱敏**：bot token 会出现在 Telegram API 的错误上下文里，
// 而 grammY 那边已经证明这类错误一定会被记进日志。一旦明文落盘，
// 加密存储就白做了。Go 的 slog 没有内置 redact，所以这里包一层 Handler。
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// 命中即整值替换。用子串匹配而不是精确键名，是因为 token 可能出现在
// 任意嵌套层级、任意键名下（err.message、req.headers 等）。
var sensitiveKeys = []string{
	"token", "password", "secret", "authorization", "cookie", "apikey", "api_key",
}

const redacted = "[REDACTED]"

// New 按 level 构造一个带脱敏的 JSON logger。
func New(level string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}

	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// 时间统一成 RFC3339，方便日志采集器解析
			if a.Key == slog.TimeKey {
				return a
			}
			return a
		},
	})

	return slog.New(&redactHandler{next: base})
}

type redactHandler struct {
	next slog.Handler
}

func (h *redactHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.next.Enabled(ctx, lvl)
}

func (h *redactHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(sanitize(a, 0))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		clean = append(clean, sanitize(a, 0))
	}
	return &redactHandler{next: h.next.WithAttrs(clean)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{next: h.next.WithGroup(name)}
}

// sanitize 递归处理一个属性。
//
// 键名命中敏感词就整个值替换；Group 则递归进去 —— 不做递归的话
// `logger.With("bot", slog.Group("cfg", "token", tok))` 这种写法会直接泄漏。
func sanitize(a slog.Attr, depth int) slog.Attr {
	if depth > 8 {
		return a
	}

	lower := strings.ToLower(a.Key)
	for _, needle := range sensitiveKeys {
		if strings.Contains(lower, needle) {
			return slog.String(a.Key, redacted)
		}
	}

	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		clean := make([]slog.Attr, 0, len(members))
		for _, m := range members {
			clean = append(clean, sanitize(m, depth+1))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
	}

	// 错误对象常常把整个请求体带在消息里，这里做一次文本级兜底扫描
	if a.Value.Kind() == slog.KindString {
		text := a.Value.String()
		if looksLikeBotToken(text) {
			return slog.String(a.Key, redacted)
		}
	}

	return a
}

// looksLikeBotToken 识别 `123456789:AAH...` 形态的串，避免它藏在
// 一段错误消息里被整体写进日志。
func looksLikeBotToken(s string) bool {
	colon := strings.IndexByte(s, ':')
	if colon < 8 || colon > 12 {
		return false
	}
	for i := 0; i < colon; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	// token 密文部分是 35 位左右的 base64url
	return len(s) >= colon+30
}
