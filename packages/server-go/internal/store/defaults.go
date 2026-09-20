package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tgs/server/internal/domain"
)

// 默认值与预置内容。
//
// 这些文案与阈值是产品的一部分，不是可选项 —— 首次启动必须有一套
// 开箱即用的合理配置，否则管理员面对的是一个什么都不会做的机器人。

// DefaultEscalation 是默认的四档阶梯。
//
// 档位设计参考了真实社群的处置节奏：先警告（给无意违规的人一个台阶），
// 再静默（不给对方反馈，避免「换个号再来」），然后禁言，最后拉黑。
// 静默放在禁言之前是有意的 —— 它是这个中继模型下最有效的一档。
func DefaultEscalation() domain.EscalationConfig {
	muteDuration := 1440 // 24 小时
	return domain.EscalationConfig{
		DecayDays: intPtr(7),
		MaxScore:  999,
		Steps: []domain.EscalationStep{
			{
				ID: "step-warn", AtScore: 1, Type: "warn",
				DeleteMessage: true, NotifyAdmins: false, Enabled: true,
			},
			{
				ID: "step-silence", AtScore: 3, Type: "silence",
				DeleteMessage: true, NotifyAdmins: true, Enabled: true,
			},
			{
				ID: "step-mute", AtScore: 5, Type: "mute",
				DurationMinutes: &muteDuration,
				DeleteMessage:   true, NotifyAdmins: true, Enabled: true,
			},
			{
				ID: "step-ban", AtScore: 8, Type: "ban",
				DeleteMessage: true, NotifyAdmins: true, Enabled: true,
			},
		},
	}
}

const (
	defaultTopicNameTemplate   = "{name} · #{id}"
	defaultTopicIconColor      = 0x6FB9F0 // 蓝色，Bot API 允许的六个值之一
	defaultGreetingText        = "你好，{name}！👋\n\n这里是与管理团队的私聊通道 —— 直接把你的问题发给我，管理员会在后台看到并回复你。\n\n请勿发送广告、推广链接或垃圾信息，此类消息会被自动拦截并可能导致你被限制。"
	defaultWarnTemplate        = "⚠️ **警告**\n\n你发送的消息触发了规则「{ruleName}」，已被移除。\n当前违规分：**{score}**\n\n请遵守规则，继续违规将升级为禁言或拉黑。"
	defaultMuteTemplate        = "🔇 **你已被禁言**\n\n原因：触发规则「{ruleName}」\n解禁时间：**{until}**\n\n禁言期间你的消息不会被送达管理员。"
	defaultBanTemplate         = "🚫 **你已被加入黑名单**\n\n原因：多次触发广告拦截规则（「{ruleName}」）。\n机器人不再接收你的消息。如有异议请通过其他渠道联系管理团队。"
	defaultAlertCardTemplate   = "🛡️ **广告拦截**\n\n**用户**：{name}（`#{id}`）\n**规则**：{ruleName}\n**命中内容**：`{matched}`\n**处置**：{outcome}\n**违规分**：{score}"
	defaultTopicHeaderTemplate = "👤 **{name}**\n🔗 {username}\n🆔 `{id}`\n🤖 经由 {botName} 转接"
	// 静默模板默认为空：静默的意义就在于让对方无感知，
	// 发一条提示等于告诉对方「你的消息被吞了，换个号再来」。
	defaultSilenceTemplate = ""
)

// DefaultBotSettings 返回新机器人的默认配置。
func DefaultBotSettings(botID int64) BotSettings {
	return BotSettings{
		BotID:               botID,
		TopicNameTemplate:   defaultTopicNameTemplate,
		TopicIconColor:      defaultTopicIconColor,
		AutoCloseHours:      intPtr(72),
		PinTopicHeader:      true,
		GreetingText:        defaultGreetingText,
		WarnTemplate:        defaultWarnTemplate,
		MuteTemplate:        defaultMuteTemplate,
		BanTemplate:         defaultBanTemplate,
		SilenceTemplate:     defaultSilenceTemplate,
		AlertCardTemplate:   defaultAlertCardTemplate,
		TopicHeaderTemplate: defaultTopicHeaderTemplate,
		RulesEnabled:        true,
		NotifyAdmins:        true,
		Escalation:          DefaultEscalation(),
		DeleteOriginMessage: true,
		MirrorEdits:         true,
		MirrorDeletes:       true,
		CoalesceWindowMs:    400,
		CoalesceThreshold:   5,
		FloodThreshold:      8,
		NotifyOnUnreachable: true,
	}
}

// ────────────────────────────── 全局设置 ──────────────────────────────

// GlobalSettings 是跨机器人的配置。
type GlobalSettings struct {
	Timezone                  string `json:"timezone"`
	AuditRetentionDays        *int   `json:"auditRetentionDays"`
	AutoDisableOnRegexTimeout bool   `json:"autoDisableOnRegexTimeout"`
	RegexTimeoutMs            int    `json:"regexTimeoutMs"`

	// AdminTgUserID 是允许使用管理机器人的 Telegram 用户 ID，0 表示未配置。
	//
	// **这是一条安全边界，不是偏好设置。** 管理机器人能增删托管其他机器人，
	// 而它是可以被任何人私聊的 —— 没有这个白名单，任何找到它的人
	// 都能往系统里塞机器人。
	//
	// 之所以不提供「第一个发 /start 的人自动成为管理员」这种便利逻辑：
	// 那等于把系统的初始控制权交给第一个碰巧找到它的人。
	AdminTgUserID int64 `json:"adminTgUserId"`
}

// DefaultGlobalSettings 返回默认全局设置。
func DefaultGlobalSettings() GlobalSettings {
	return GlobalSettings{
		Timezone:                  "Asia/Shanghai",
		AuditRetentionDays:        intPtr(180),
		AutoDisableOnRegexTimeout: true,
		RegexTimeoutMs:            50,
		AdminTgUserID:             0,
	}
}

const globalSettingsKey = "global"

// GetGlobalSettings 读全局设置，缺失时返回默认值。
func (s *Store) GetGlobalSettings(ctx context.Context) (GlobalSettings, error) {
	var raw *string
	err := s.read.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key = ?`, globalSettingsKey).Scan(&raw)
	if err != nil || raw == nil {
		return DefaultGlobalSettings(), nil
	}

	settings := DefaultGlobalSettings()
	if err := json.Unmarshal([]byte(*raw), &settings); err != nil {
		// 解析失败回落到默认值而不是报错：一次改库改坏了不该让整个服务起不来
		return DefaultGlobalSettings(), nil
	}
	return settings, nil
}

// WriteGlobalSettings 覆盖写全局设置。
func (s *Store) WriteGlobalSettings(ctx context.Context, settings GlobalSettings) error {
	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.write.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		globalSettingsKey, string(payload), time.Now().UnixMilli())
	return err
}

// ────────────────────────────── 预置规则 ──────────────────────────────

// SeedRule 是一条预置规则的定义。
type SeedRule struct {
	Name      string
	Pattern   string
	Flags     string
	MatchMode string
	Target    string
	Action    string
	Severity  int
	Priority  int
	Enabled   bool
	Note      string
}

// SeedRules 是首次启动写入的规则。
//
// 这些模式全部能在 Go 的 RE2 上编译 —— 换言之它们天然不含前后瞻与
// 反向引用。这一点很重要：预置规则是管理员的第一个参照物，
// 如果它们本身就用了 RE2 不支持的写法，管理员照抄时会直接撞墙。
func SeedRules() []SeedRule {
	return []SeedRule{
		{
			Name:      "Telegram 群组 / 频道引流链接",
			Pattern:   `(?:http://|https://)?(?:t\.me|telegram\.me|telegram\.dog)/(?:joinchat/|\+)?[A-Za-z0-9_-]{5,}`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  15,
			Priority:  10,
			Enabled:   true,
			Note:      "匹配 t.me / telegram.me 的群组邀请链接与频道链接，含隐藏 text_link。",
		},
		{
			Name:      "加好友引流话术",
			Pattern:   `(?:加|扣|私)\s*(?:我|你)?\s*(?:微信|徽信|威信|vx|VX|v信|V信|QQ|qq|扣扣|WhatsApp|Telegram|电报|飞机)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  20,
			Priority:  20,
			Enabled:   true,
			Note:      "「加微信」「私我QQ」这类最常见的引流话术。",
		},
		{
			Name:      "加密货币 / 博彩引流",
			Pattern:   `\b(?:usdt|btc|eth|trx)\b|比特币|以太坊|博彩|棋牌|时时彩|六合彩|百家乐|带单|喊单|返水`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  25,
			Priority:  30,
			Enabled:   true,
			Note:      `用 \b 词边界，避免把 ethernet 之类的正常词误伤。`,
		},
		{
			Name:      "短链接服务",
			Pattern:   `\b(?:bit\.ly|tinyurl\.com|is\.gd|cutt\.ly|shorturl\.at|rebrand\.ly|t\.cn|dwz\.cn|suo\.im|urlz\.fr)/[A-Za-z0-9]+`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  10,
			Priority:  40,
			Enabled:   true,
			Note:      "短链接常用作跳转规避，命中即删。",
		},
		{
			Name:      "兼职刷单诈骗话术",
			Pattern:   `(?:日入|月入|轻松赚|躺赚|稳定收入|一部手机)\s*\d+\s*(?:元|块|米|w|万)?|刷单|点赞任务|关注任务`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "escalate",
			Severity:  30,
			Priority:  15,
			Enabled:   true,
			Note:      "诈骗高发话术，直接走阶梯处罚而非仅删除。",
		},
		{
			Name:      "超长消息（疑似刷屏）",
			Pattern:   `^[\s\S]{1500,}$`,
			Flags:     "u",
			MatchMode: "regex",
			Target:    "text",
			Action:    "notify",
			Severity:  5,
			Priority:  200,
			Enabled:   false,
			Note:      "默认停用。启用后对超长消息只提醒管理员，不做处罚 —— 容易误伤长文咨询。",
		},
	}
}

// Seed 执行首次启动的播种。
//
// 每一项都是「只在缺失时写入」，绝不覆盖 —— 管理员改过的密码、设置、
// 规则必须活过重启。
func (s *Store) Seed(ctx context.Context, adminUsername, adminPassword string) error {
	if err := s.seedAdmin(ctx, adminUsername, adminPassword); err != nil {
		return err
	}
	if err := s.seedGlobalSettings(ctx); err != nil {
		return err
	}
	return s.seedRules(ctx)
}

func (s *Store) seedAdmin(ctx context.Context, username, password string) error {
	var count int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	if password == "" {
		return ErrNoAdminPassword
	}

	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	_, err = s.write.ExecContext(ctx,
		`INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		username, hash, now, now)
	return err
}

func (s *Store) seedGlobalSettings(ctx context.Context) error {
	payload, err := json.Marshal(DefaultGlobalSettings())
	if err != nil {
		return err
	}
	_, err = s.write.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO NOTHING`,
		globalSettingsKey, string(payload), time.Now().UnixMilli())
	return err
}

func (s *Store) seedRules(ctx context.Context) error {
	var count int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM ad_rules`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	return s.WithTx(ctx, func(tx *Tx) error {
		now := time.Now().UnixMilli()
		for _, r := range SeedRules() {
			_, err := tx.Exec(`
				INSERT INTO ad_rules (bot_id, name, pattern, flags, match_mode, target, action,
					severity, priority, is_enabled, is_system, note, hit_count, created_at, updated_at)
				VALUES (NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 0, ?, ?)`,
				r.Name, r.Pattern, r.Flags, r.MatchMode, r.Target, r.Action,
				r.Severity, r.Priority, boolToInt(r.Enabled), r.Note, now, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ────────────────────────────── 小工具 ──────────────────────────────

func intPtr(v int) *int { return &v }
