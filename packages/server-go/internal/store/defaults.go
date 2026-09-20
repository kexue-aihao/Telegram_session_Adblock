package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
			Name: "加密货币 / 博彩引流",
			// 前导边界不能写成 \b。Go 的 \b 是 **ASCII 词边界**（\w = [0-9A-Za-z_]），
			// 而币种的常见写法恰恰是数字紧贴 ticker：「出1000usdt」里 `0` 与 `u`
			// 都是词字符，中间没有边界，\b 会直接放行 —— 越是正常写法越漏。
			//
			// 改成 `(?:^|[^A-Za-z])` 前导：数字、标点、汉字、行首都能起头，
			// 只有紧跟在拉丁字母后面时才不匹配（which/method/ethics 里的 `eth`
			// 因此仍然被挡住）。尾部保留 \b 是因为它不消耗字符 ——
			// 若两侧都用 [^A-Za-z]，「usdt btc」这种相邻写法会漏掉第二个。
			//
			// 代价：匹配区间会多吃掉前导的那一个字符，「转账100usdt」的
			// matchedText 是「0usdt」。审计面板展示的是前后带 24 字上下文
			// 的摘录，这一点偏移看不出来。
			Pattern:   `(?:^|[^A-Za-z])(?:usdt|btc|eth|trx)\b|比特币|以太坊|博彩|棋牌|时时彩|六合彩|百家乐|带单|喊单|返水`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  25,
			Priority:  30,
			Enabled:   true,
			Note:      `前导用 (?:^|[^A-Za-z]) 而非 \b —— 见上方注释：数字紧贴 ticker 时 \b 会失效。`,
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
		// ── 犯罪工具 / 服务售卖类 ────────────────────────────────
		//
		// 上面五条锚定的是「怎么联系到卖家」（链接、加微信、短链）。但有一类
		// 广告**通篇不写联系方式** —— 它卖的是犯罪工具本身，靠发帖人账号被私聊。
		// 实测一条这类广告（盗U / 钱包授权 / 远控 / 群发软件）对上面五条的
		// 命中数是 0，下面这几条就是补这个缺口。
		{
			Name:      "盗币黑话（盗U / 盗米 / 剪贴板劫持）",
			Pattern:   `盗(?:U|u|币|米|资产|USDT)|剪(?:切|贴)板(?:劫持|盗|篡改|木马)|盗取(?:钱包|资产|数字资产)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "escalate",
			Severity:  35,
			Priority:  12,
			Enabled:   true,
			Note:      "黑话：U = USDT、米 = 钱。这些词没有正常用法，命中即走阶梯处罚而非仅删除。",
		},
		{
			Name: "盗号 / 跑分黑话",
			// 刻意不写「洗钱」「跑分平台」这类完整词：前者会被「反洗钱」命中，
			// 后者是科技新闻里的正常说法（手机跑分平台）。黑话形式本身
			// （洗U、跑分代理）已经足够具体。
			Pattern:   `盗号(?:软件|工具|教程|技术|源码)|洗(?:U|u|币)|跑分(?:代理|兼职|日结)|黑U|(?:四件套|四要素)(?:出售|批发|货源|低价)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "escalate",
			Severity:  35,
			Priority:  14,
			Enabled:   true,
			Note:      "一律限定在「工具化 / 交易化」语境：光写「盗号」会误伤「防盗号提醒」，「洗钱」会误伤「反洗钱」。",
		},
		{
			Name:      "钱包授权钓鱼 / 假钱包",
			Pattern:   `假钱包|钓鱼钱包|授权(?:后|完成|之后).{0,20}(?:盗|清空|转走|洗劫|搬空)|助记词.{0,6}(?:窃取|盗取|套取)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  30,
			Priority:  16,
			Enabled:   true,
			// 刻意不把「钱包授权」单独列为强信号：受害者在求助时说的也是
			// 同一句话（「我的钱包授权怎么取消」），那是我们最该帮的人。
			// 它被放进下面的弱信号规则，只在与其他信号共现时才升级。
			Note: "「假钱包」和「授权后…盗走」是投放者的说法；受害者只会说「钱包授权」，那种情况交给弱信号 + 共现去判。",
		},
		{
			Name:      "远控 / 木马 / 手机植入",
			Pattern:   `远控(?:软件|源码|工具|木马|手机|端|系统)|木马(?:生成|免杀|源码|定制)|免杀|(?:APP|app|软件|程序).{0,6}植入|植入(?:手机|设备|到手机)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  30,
			Priority:  18,
			Enabled:   true,
			Note:      "「远控」单独出现太容易误伤（远程控制是正常词），因此限定在工具化 / 植入语境。",
		},
		{
			Name: "群发工具",
			// 只留「群发 + 工具名」这种组合。原先还写了「一条龙」「手把手教」
			// 「包教包会」—— 那些是正常商家和培训机构都会用的说法
			// （「一条龙服务」「手把手教你做菜」），放在会删消息的规则里
			// 代价太大。它们降级到下面的弱信号规则。
			Pattern:   `群发(?:软件|器|工具|系统|脚本)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "delete",
			Severity:  20,
			Priority:  22,
			Enabled:   true,
			Note:      "群发软件/器/脚本是黑产工具的代称，正常社群里不会出现。",
		},
		{
			Name:      "弱信号：诈骗 / 黑产术语（只计数，不单独处罚）",
			Pattern:   `远控|剪(?:切|贴)板|冷钱包|热钱包|助记词|私钥|授权(?:页面|链接)|钱包授权|模拟你的|植入|木马|一条龙|手把手教|包教包会|小白.{0,4}(?:教会|上手)`,
			Flags:     "iu",
			MatchMode: "regex",
			Target:    "all",
			Action:    "notify",
			Severity:  5,
			Priority:  300,
			Enabled:   true,
			// 这些词单独出现时误报率太高，不能删消息：「我的助记词丢了怎么办」
			// 是受害者在求助，「一条龙服务」是正常商家在说话。它们的作用是
			// 给下面那条共现规则提供计数 —— 单个词说明不了什么，
			// 一条消息里同时出现三个就说明了很多。
			Note: "只告警、不删除。单个词误报率高，靠下一条共现规则把它们聚合起来。",
		},
		{
			Name: "多信号共现",
			// cooccurrence 模式的 pattern 是一个十进制整数：触发所需的
			// 「不同规则命中数」下限。不看文本，看的是整条消息的命中集合。
			Pattern:   `3`,
			Flags:     "",
			MatchMode: "cooccurrence",
			Target:    "all",
			Action:    "escalate",
			Severity:  40,
			Priority:  900,
			Enabled:   true,
			Note:      "同一条消息命中 3 条以上不同规则时触发。单看每个词都可能误伤，同时中 3 个以上基本不是正常内容。",
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
	return s.syncSystemRules(ctx)
}

// ────────────────────────────── 系统规则同步 ──────────────────────────────

// systemRuleManaged 是一条系统规则里「由代码维护」的字段集合。
//
// 拆成结构体是为了算指纹：拿它和库里那一行的对应字段比对，就能判断
// 管理员到底动没动过这条规则。
type systemRuleManaged struct {
	name      string
	pattern   string
	flags     string
	matchMode string
	target    string
	action    string
	note      string
	severity  int
	priority  int
}

func managedOf(r SeedRule) systemRuleManaged {
	return systemRuleManaged{
		name: r.Name, pattern: r.Pattern, flags: r.Flags, matchMode: r.MatchMode,
		target: r.Target, action: r.Action, note: r.Note,
		severity: r.Severity, priority: r.Priority,
	}
}

// fingerprint 算出受管字段的指纹。
//
// 哈希而不是把字段拼成一个字符串直接比：note 是自由文本，可以含任何分隔符，
// 直接拼接会因为「字段 A 的尾巴 + 字段 B 的头」撞出同一个串。
func (m systemRuleManaged) fingerprint() string {
	h := sha256.New()
	for _, s := range []string{m.name, m.pattern, m.flags, m.matchMode, m.target, m.action, m.note} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	fmt.Fprintf(h, "%d\x00%d", m.severity, m.priority)
	return hex.EncodeToString(h.Sum(nil))
}

// systemRulesSyncedKey 记录历史上同步过哪些系统规则名。
//
// 没有它就无法区分「这条规则是本版本新增的」和「管理员把老规则删了」——
// 前者必须插进去，后者若也插，等于管理员永远删不掉系统规则。
const systemRulesSyncedKey = "system_rules_synced"

type systemRuleRow struct {
	id          int64
	managed     systemRuleManaged
	fingerprint string
}

// syncSystemRules 把 SeedRules() 同步进库。
//
// 注意这是**每次启动都跑**，而不只是首次播种。原先的 seedRules 只在
// ad_rules 为空时写入，于是老实例永远拿不到后续版本新增或修正的规则 ——
// 修一条预置规则的正则，对已经跑着的部署完全不起作用。
//
// 三条边界：
//   - is_enabled / hit_count / last_hit_at 永不触碰。管理员停用过的规则
//     不能因为一次升级被重新打开。
//   - 管理员改过受管字段的规则归管理员，代码不再接管。面板上写的是
//     「预置规则……可以停用或修改」，那就得说话算数。
//   - 被删掉的规则不复活。
//
// 唯一的例外：老库升级上来时 system_fingerprint 是 NULL（从未同步过），
// 这一轮会被代码接管一次 —— 若管理员此前改过某条老规则，那次改动会被覆盖。
// 这是一次性的代价，之后指纹就位，改动不再受影响。
func (s *Store) syncSystemRules(ctx context.Context) error {
	existing, err := s.loadSystemRules(ctx)
	if err != nil {
		return err
	}
	applied, err := s.loadSyncedRuleNames(ctx)
	if err != nil {
		return err
	}

	return s.WithTx(ctx, func(tx *Tx) error {
		now := time.Now().UnixMilli()
		names := append([]string(nil), applied...)

		for _, def := range SeedRules() {
			want := managedOf(def)
			fp := want.fingerprint()

			cur, ok := existing[def.Name]
			if !ok {
				// 同步过却不在库里 = 管理员删掉了，尊重这个决定
				if containsString(applied, def.Name) {
					continue
				}
				if _, err := tx.Exec(`
					INSERT INTO ad_rules (bot_id, name, pattern, flags, match_mode, target, action,
						severity, priority, is_enabled, is_system, note, hit_count,
						system_fingerprint, created_at, updated_at)
					VALUES (NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 0, ?, ?, ?)`,
					def.Name, def.Pattern, def.Flags, def.MatchMode, def.Target, def.Action,
					def.Severity, def.Priority, boolToInt(def.Enabled), def.Note,
					fp, now, now); err != nil {
					return err
				}
				names = append(names, def.Name)
				continue
			}

			// 指纹与库里当前字段对不上 = 管理员改过，这条规则已经是他的了
			if cur.fingerprint != "" && cur.fingerprint != cur.managed.fingerprint() {
				continue
			}
			// 定义没变就什么都不写，免得每次启动都刷一遍 updated_at
			if cur.fingerprint == fp {
				continue
			}

			if _, err := tx.Exec(`
				UPDATE ad_rules
				SET pattern = ?, flags = ?, match_mode = ?, target = ?, action = ?,
				    severity = ?, priority = ?, note = ?, system_fingerprint = ?, updated_at = ?
				WHERE id = ?`,
				def.Pattern, def.Flags, def.MatchMode, def.Target, def.Action,
				def.Severity, def.Priority, def.Note, fp, now, cur.id); err != nil {
				return err
			}
		}

		payload, err := json.Marshal(names)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`
			INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			systemRulesSyncedKey, string(payload), now)
		return err
	})
}

// loadSystemRules 读出全部全局系统规则，按名字索引。
// 只认 bot_id IS NULL 的：机器人级规则是管理员建的，不归代码管。
func (s *Store) loadSystemRules(ctx context.Context) (map[string]systemRuleRow, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT id, name, pattern, flags, match_mode, target, action, severity, priority,
		       COALESCE(note, ''), COALESCE(system_fingerprint, '')
		FROM ad_rules
		WHERE is_system = 1 AND bot_id IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("读取系统规则: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]systemRuleRow, 16)
	for rows.Next() {
		var r systemRuleRow
		if err := rows.Scan(
			&r.id, &r.managed.name, &r.managed.pattern, &r.managed.flags,
			&r.managed.matchMode, &r.managed.target, &r.managed.action,
			&r.managed.severity, &r.managed.priority, &r.managed.note, &r.fingerprint,
		); err != nil {
			return nil, fmt.Errorf("读取系统规则行: %w", err)
		}
		out[r.managed.name] = r
	}
	return out, rows.Err()
}

func (s *Store) loadSyncedRuleNames(ctx context.Context) ([]string, error) {
	var raw *string
	err := s.read.QueryRowContext(ctx,
		`SELECT value FROM app_settings WHERE key = ?`, systemRulesSyncedKey).Scan(&raw)
	if err != nil || raw == nil {
		return nil, nil
	}

	var names []string
	if err := json.Unmarshal([]byte(*raw), &names); err != nil {
		// 解析失败当作「没有记录」：最坏结果是已删除的规则被重新插一次，
		// 而报错会让整个服务起不来。
		return nil, nil
	}
	return names, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ────────────────────────────── 小工具 ──────────────────────────────

func intPtr(v int) *int { return &v }
