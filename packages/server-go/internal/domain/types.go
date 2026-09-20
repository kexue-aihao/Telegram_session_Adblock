// Package domain 定义领域常量与对外 JSON 契约。
//
// 这一层刻意不依赖任何其它内部包 —— 它是服务端与前端共同认同的词汇表。
// 字段名与 Node 版本保持一致（camelCase JSON），这样两套实现可以共用
// 同一份前端契约，也让迁移期间的对比排查有据可依。
package domain

// ────────────────────────────── 枚举 ──────────────────────────────

// 中继消息的内容类型
const (
	ContentText      = "text"
	ContentPhoto     = "photo"
	ContentVideo     = "video"
	ContentDocument  = "document"
	ContentAudio     = "audio"
	ContentVoice     = "voice"
	ContentSticker   = "sticker"
	ContentAnimation = "animation"
	ContentVideoNote = "video_note"
	ContentLocation  = "location"
	ContentContact   = "contact"
	ContentPoll      = "poll"
	ContentDice      = "dice"
	ContentUnknown   = "unknown"
)

// 话题生命周期
const (
	TopicOpen    = "open"
	TopicClosed  = "closed"
	TopicDeleted = "deleted"
)

// 中继方向
const (
	DirUserToAdmin = "user_to_admin"
	DirAdminToUser = "admin_to_user"
)

// 机器人健康状态
const (
	HealthUnknown  = "unknown"
	HealthStarting = "starting"
	HealthOnline   = "online"
	HealthError    = "error"
	HealthStopped  = "stopped"
)

// 规则匹配方式
const (
	MatchRegex     = "regex"
	MatchContains  = "contains"
	MatchWholeWord = "whole_word"
)

// 规则匹配目标 —— 决定拿消息的哪一部分去匹配。
//
// 注意 text_link：广告最常见的藏链接手法是把 URL 塞进 entity 里，
// 显示文本完全正常，因此必须单独覆盖。
const (
	TargetText     = "text"
	TargetCaption  = "caption"
	TargetTextLink = "text_link"
	TargetURL      = "url"
	TargetMention  = "mention"
	TargetForward  = "forward"
	TargetAll      = "all"
)

// 规则命中后可执行的动作
const (
	ActionDelete   = "delete"
	ActionWarn     = "warn"
	ActionEscalate = "escalate"
	ActionNotify   = "notify"
)

// 处罚档位。
//
// 没有 Telegram 原生的 restrict/ban：在「用户私聊 → 管理群话题」的中继
// 模型下，终端用户并不在管理群里，restrictChatMember 会直接报错。
// 因此处罚一律在机器人层级实现。
const (
	SanctionWarn    = "warn"    // 私聊发送警告文案
	SanctionSilence = "silence" // 静默：消息不再转发进话题
	SanctionMute    = "mute"    // 硬禁言：回复禁言提示与解禁时间
	SanctionBan     = "ban"     // 拉黑：机器人不再响应
)

// BlockingSanctions 是真正会阻断转发的档位（warn 不阻断）。
var BlockingSanctions = []string{SanctionSilence, SanctionMute, SanctionBan}

// 处罚来源
const (
	ReasonRuleHit    = "rule_hit"
	ReasonManual     = "manual"
	ReasonFlood      = "flood"
	ReasonEscalation = "escalation"
)

// 命中后实际执行的动作（写进审计，便于复盘）
const (
	OutcomeDeleted      = "deleted"
	OutcomeDeleteFailed = "delete_failed"
	OutcomeWarned       = "warned"
	OutcomeSilenced     = "silenced"
	OutcomeMuted        = "muted"
	OutcomeBanned       = "banned"
	OutcomeNotified     = "notified"
	OutcomeNotifyFailed = "notify_failed"
	OutcomeLoggedOnly   = "logged_only"
	OutcomeRegexTimeout = "regex_timeout"
)

// 审计操作者类型
const (
	ActorAdmin  = "admin"
	ActorBot    = "bot"
	ActorSystem = "system"
)

// ────────────────────────────── 阶梯处罚配置 ──────────────────────────────

// EscalationStep 是阶梯的一级。
// AtScore 是「违规分达到该值时触发」，由规则命中的 severity 累加。
type EscalationStep struct {
	ID              string `json:"id"`
	AtScore         int    `json:"atScore"`
	Type            string `json:"type"`
	DurationMinutes *int   `json:"durationMinutes"` // 仅 mute 使用；nil 表示永久
	DeleteMessage   bool   `json:"deleteMessage"`
	NotifyAdmins    bool   `json:"notifyAdmins"`
	Enabled         bool   `json:"enabled"`
}

// EscalationConfig 是完整的阶梯配置，整块以 JSON 存在 bot_settings.escalation。
type EscalationConfig struct {
	Steps []EscalationStep `json:"steps"`
	// 多少天无违规后违规分减半；nil = 永不衰减
	DecayDays *int `json:"decayDays"`
	MaxScore  int  `json:"maxScore"`
}

// ────────────────────────────── 对外 DTO ──────────────────────────────

// Bot 是机器人在面板上的表示。
// 注意这里**没有 token 字段** —— 明文 token 在任何接口都不会回传。
type Bot struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	Username        string  `json:"username"`
	TokenMask       string  `json:"tokenMask"`
	TelegramID      *int64  `json:"telegramId"`
	AdminGroupID    *int64  `json:"adminGroupId"`
	AdminGroupTitle *string `json:"adminGroupTitle"`
	IsEnabled       bool    `json:"isEnabled"`
	HealthStatus    string  `json:"healthStatus"`
	LastError       *string `json:"lastError"`
	LastPolledAt    *int64  `json:"lastPolledAt"`
	CreatedAt       int64   `json:"createdAt"`
	UpdatedAt       int64   `json:"updatedAt"`
}

// BotValidation 是 token 校验结果（向导第一步）。
type BotValidation struct {
	OK                      bool    `json:"ok"`
	TelegramID              *int64  `json:"telegramId"`
	Name                    *string `json:"name"`
	Username                *string `json:"username"`
	CanJoinGroups           *bool   `json:"canJoinGroups"`
	CanReadAllGroupMessages *bool   `json:"canReadAllGroupMessages"`
	Error                   *string `json:"error"`
}

// GroupCheck 是管理群体检结果，直接告诉管理员缺哪一项权限。
type GroupCheck struct {
	OK                 bool     `json:"ok"`
	ChatID             int64    `json:"chatId"`
	Title              *string  `json:"title"`
	IsForum            bool     `json:"isForum"`
	IsAdmin            bool     `json:"isAdmin"`
	CanManageTopics    bool     `json:"canManageTopics"`
	CanDeleteMessages  bool     `json:"canDeleteMessages"`
	CanRestrictMembers bool     `json:"canRestrictMembers"`
	Problems           []string `json:"problems"`
}

// Contact 是终端用户（私聊机器人的人）。
type Contact struct {
	ID              int64   `json:"id"`
	BotID           int64   `json:"botId"`
	TgUserID        int64   `json:"tgUserId"`
	Username        *string `json:"username"`
	FirstName       *string `json:"firstName"`
	LastName        *string `json:"lastName"`
	LanguageCode    *string `json:"languageCode"`
	IsBlocked       bool    `json:"isBlocked"`
	IsUnreachable   bool    `json:"isUnreachable"`
	ViolationScore  int     `json:"violationScore"`
	LastViolationAt *int64  `json:"lastViolationAt"`
	FirstSeenAt     int64   `json:"firstSeenAt"`
	LastSeenAt      int64   `json:"lastSeenAt"`
	Notes           *string `json:"notes"`
}

// Media 是消息里的一份媒体文件标识。
type Media struct {
	Kind         string `json:"kind"`
	FileID       string `json:"fileId"`
	FileUniqueID string `json:"fileUniqueId"`
	Position     int    `json:"position"`
}

// MessageContent 是中继消息的内容。
type MessageContent struct {
	Type          string   `json:"type"`
	Text          *string  `json:"text"`
	Caption       *string  `json:"caption"`
	Media         []Media  `json:"media"`
	MediaGroupID  *string  `json:"mediaGroupId"`
	HasHiddenLink bool     `json:"hasHiddenLink"`
	HiddenLinks   []string `json:"hiddenLinks"`
}

// RelayedMessage 是面板聊天视图里的一条消息。
type RelayedMessage struct {
	ID          int64          `json:"id"`
	TopicID     int64          `json:"topicId"`
	Direction   string         `json:"direction"`
	Content     MessageContent `json:"content"`
	IsDeleted   bool           `json:"isDeleted"`
	EditedAt    *int64         `json:"editedAt"`
	CreatedAt   int64          `json:"createdAt"`
	RuleHitID   *int64         `json:"ruleHitId"`
	SenderLabel *string        `json:"senderLabel"`
}

// SessionSummary 是会话（一个话题 + 一个联系人）。
type SessionSummary struct {
	ID                   int64   `json:"id"`
	BotID                int64   `json:"botId"`
	BotName              string  `json:"botName"`
	BotUsername          string  `json:"botUsername"`
	ContactID            int64   `json:"contactId"`
	DisplayName          string  `json:"displayName"`
	Username             *string `json:"username"`
	TgUserID             int64   `json:"tgUserId"`
	ThreadID             int64   `json:"threadId"`
	Title                string  `json:"title"`
	Status               string  `json:"status"`
	LastMessageAt        *int64  `json:"lastMessageAt"`
	LastMessagePreview   *string `json:"lastMessagePreview"`
	LastMessageDirection *string `json:"lastMessageDirection"`
	MessageCount         int     `json:"messageCount"`
	ViolationScore       int     `json:"violationScore"`
	IsBlocked            bool    `json:"isBlocked"`
	CreatedAt            int64   `json:"createdAt"`
	ClosedAt             *int64  `json:"closedAt"`
}

// AdRule 是广告规则。
type AdRule struct {
	ID           int64   `json:"id"`
	BotID        *int64  `json:"botId"` // nil = 全局规则
	Name         string  `json:"name"`
	Pattern      string  `json:"pattern"`
	Flags        string  `json:"flags"`
	MatchMode    string  `json:"matchMode"`
	Target       string  `json:"target"`
	Action       string  `json:"action"`
	Severity     int     `json:"severity"`
	Priority     int     `json:"priority"`
	IsEnabled    bool    `json:"isEnabled"`
	IsSystem     bool    `json:"isSystem"`
	Note         *string `json:"note"`
	HitCount     int     `json:"hitCount"`
	LastHitAt    *int64  `json:"lastHitAt"`
	AutoDisabled bool    `json:"autoDisabled"`
	CreatedAt    int64   `json:"createdAt"`
	UpdatedAt    int64   `json:"updatedAt"`
}

// RuleHit 是一次规则命中 —— 广告审计的核心记录。
//
// 刻意把 RulePattern / RuleFlags 冗余存下来：规则可能事后被改甚至被删，
// 但审计记录必须永远能还原「当时是按什么规则判定的」。
type RuleHit struct {
	ID                int64    `json:"id"`
	RuleID            *int64   `json:"ruleId"`
	RuleName          string   `json:"ruleName"`
	RulePattern       string   `json:"rulePattern"`
	RuleFlags         string   `json:"ruleFlags"`
	BotID             int64    `json:"botId"`
	BotName           string   `json:"botName"`
	ContactID         int64    `json:"contactId"`
	ContactName       string   `json:"contactName"`
	ContactUsername   *string  `json:"contactUsername"`
	TgUserID          int64    `json:"tgUserId"`
	TopicID           *int64   `json:"topicId"`
	ThreadID          *int64   `json:"threadId"`
	MatchedText       string   `json:"matchedText"`
	NormalizedExcerpt *string  `json:"normalizedExcerpt"`
	Outcomes          []string `json:"outcomes"`
	Severity          int      `json:"severity"`
	CreatedAt         int64    `json:"createdAt"`
}

// Sanction 是一条处罚记录。
type Sanction struct {
	ID        int64   `json:"id"`
	BotID     int64   `json:"botId"`
	ContactID int64   `json:"contactId"`
	Type      string  `json:"type"`
	Reason    string  `json:"reason"`
	RuleID    *int64  `json:"ruleId"`
	ExpiresAt *int64  `json:"expiresAt"`
	IsActive  bool    `json:"isActive"`
	CreatedBy *string `json:"createdBy"`
	CreatedAt int64   `json:"createdAt"`
	LiftedAt  *int64  `json:"liftedAt"`
}

// AuditEntry 是一条面板操作审计。
type AuditEntry struct {
	ID         int64          `json:"id"`
	ActorType  string         `json:"actorType"`
	ActorID    *string        `json:"actorId"`
	Action     string         `json:"action"`
	TargetType *string        `json:"targetType"`
	TargetID   *string        `json:"targetId"`
	Detail     map[string]any `json:"detail"`
	IP         *string        `json:"ip"`
	CreatedAt  int64          `json:"createdAt"`
}

// Paginated 是统一的游标分页响应。
type Paginated[T any] struct {
	Items      []T    `json:"items"`
	NextCursor *int64 `json:"nextCursor"`
	Total      *int64 `json:"total"`
}
