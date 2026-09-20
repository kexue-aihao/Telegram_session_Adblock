// Package config 加载并校验环境变量。
//
// 沿用原 Node 版本的设计取向：**fail-fast 且一次性报全**。
// 宁可进程立刻退出并打印「怎么修」，也不要在运行到解密 bot token 时
// 才发现 MASTER_KEY 不对；而逐条报错会让人修一个重启一次。
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Env 是校验通过后的运行配置。
//
// 字段全部是已解析好的强类型值 —— 让「MASTER_KEY 是 32 字节」这类约束
// 在启动时就固化下来，而不是在每个使用点重复判断。
type Env struct {
	AppEnv   string // development | production | test
	IsProd   bool
	Host     string
	Port     int
	LogLevel string
	Location *time.Location

	DatabaseURL string

	MasterKey     []byte // 恰好 32 字节，AES-256
	SessionSecret []byte
	AdminPassword string // 仅在首次播种时使用，可为空
	AdminUsername string

	PublicURL     string
	WebhookPath   string
	WebhookSecret string
	WebDist       string

	// EnvFile 是实际加载的 .env 路径，仅用于启动日志
	EnvFile string
}

// Load 读取环境变量并校验。返回的错误已经把「怎么修」写进去了。
func Load() (*Env, error) {
	envFile := loadDotEnv()

	var problems []string

	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v
		}
		return def
	}
	opt := func(key string) string {
		return strings.TrimSpace(os.Getenv(key))
	}
	require := func(key, why string) string {
		v, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(v) == "" {
			problems = append(problems, fmt.Sprintf("%s 未设置 —— %s", key, why))
			return ""
		}
		return v
	}

	env := &Env{}
	env.AppEnv = get("APP_ENV", get("NODE_ENV", "development"))
	env.IsProd = env.AppEnv == "production"
	env.Host = get("HOST", "0.0.0.0")
	env.LogLevel = get("LOG_LEVEL", "info")
	env.DatabaseURL = get("DATABASE_URL", "file:./data/app.db")
	env.AdminUsername = get("ADMIN_USERNAME", "admin")
	env.WebhookPath = get("WEBHOOK_PATH", "/tg/webhook")
	// 以下都是可选，空串表示「不启用该特性」
	env.PublicURL = opt("PUBLIC_URL")
	env.WebhookSecret = opt("WEBHOOK_SECRET")
	env.WebDist = opt("WEB_DIST")

	if raw := get("PORT", "8787"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			problems = append(problems, fmt.Sprintf("PORT 不是合法端口：%q", raw))
		} else {
			env.Port = port
		}
	}

	// 时区必须在任何时间格式化之前生效，否则统计会按 UTC 切日 ——
	// 而「今天的拦截数」是面板上最常看的一个数字，差 8 小时会非常刺眼。
	tzName := get("TZ", "Asia/Shanghai")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		problems = append(problems, fmt.Sprintf("无法识别 TZ %q：%v", tzName, err))
		loc = time.UTC
	}
	env.Location = loc

	switch env.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("LOG_LEVEL 只能是 debug/info/warn/error，收到 %q", env.LogLevel))
	}

	masterKeyRaw := require("MASTER_KEY", "没有它无法加密存储 bot token")
	if masterKeyRaw != "" {
		key, err := parseMasterKey(masterKeyRaw)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			env.MasterKey = key
		}
	}

	secretRaw := require("SESSION_SECRET", "它用于签名会话 Cookie")
	if secretRaw != "" && len(secretRaw) < 32 {
		problems = append(problems,
			fmt.Sprintf("SESSION_SECRET 至少 32 个字符（当前 %d）—— 生成：openssl rand -hex 32", len(secretRaw)))
	} else if secretRaw != "" {
		env.SessionSecret = []byte(secretRaw)
	}

	env.AdminPassword = os.Getenv("ADMIN_PASSWORD")

	if len(problems) > 0 {
		where := "（未找到 .env，请从 .env.example 复制一份）"
		if envFile != "" {
			where = fmt.Sprintf("（已读取 %s）", envFile)
		}
		return nil, fmt.Errorf("环境变量校验失败%s:\n  • %s", where, strings.Join(problems, "\n  • "))
	}

	env.EnvFile = envFile
	return env, nil
}

// parseMasterKey 只接受两种可验证长度的编码。
//
// 刻意不允许「任意字符串」：那样运维很可能填一个短口令，
// AES-256 的强度就名存实亡，而且没有任何迹象表明它是错的。
func parseMasterKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)

	if len(trimmed) == 64 {
		if key, err := hex.DecodeString(trimmed); err == nil {
			return key, nil
		}
	}
	if key, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(key) == 32 {
		return key, nil
	}
	if key, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil && len(key) == 32 {
		return key, nil
	}

	return nil, errors.New(
		"MASTER_KEY 必须是 64 位十六进制字符，或编码 32 字节的 base64\n" +
			"    生成一个：openssl rand -hex 32")
}

// loadDotEnv 按优先级查找并加载 .env。
//
// 只加载第一个存在的文件，避免多处配置互相覆盖造成「改了没生效」——
// 那是最难排查的一类问题。已存在的真实环境变量优先级更高，不覆盖。
func loadDotEnv() string {
	candidates := []string{
		".env",
		"../../.env",
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		applyDotEnv(string(data))
		return path
	}
	return ""
}

func applyDotEnv(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// 去掉包裹的引号；支持 `KEY="a b"` 这种带空格的写法
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}

// SQLitePath 从 DATABASE_URL 里取出文件路径。
// 非 file: 的 URL（远程 libsql）返回空串，由调用方决定是否支持。
func (e *Env) SQLitePath() string {
	const prefix = "file:"
	if !strings.HasPrefix(e.DatabaseURL, prefix) {
		return ""
	}
	path := strings.TrimPrefix(e.DatabaseURL, prefix)
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	return path
}
