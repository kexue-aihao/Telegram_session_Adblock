package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/secret"
)

// ErrNotFound 是「查不到」的统一错误。
//
// 用哨兵错误而不是 (nil, nil)：后者会让调用方忘记判空，
// 而 Go 里的 nil 解引用崩溃点往往离真正的原因很远。
var ErrNotFound = errors.New("记录不存在")

// ErrDuplicate 表示唯一约束冲突，通常是并发写入导致的竞态。
// 调用方应当捕获它并改为读取已存在的那一行 —— 那不是错误，是正常的并发路径。
var ErrDuplicate = errors.New("记录已存在")

// ────────────────────────────── 机器人 ──────────────────────────────

// BotRow 是 bots 表的完整行。
// 注意它含密文字段，**绝不能被直接序列化成 JSON** —— 对外一律走 ToDTO。
type BotRow struct {
	ID               int64
	Name             string
	Username         string
	TokenCipher      string
	TokenIV          string
	TokenTag         string
	TokenMask        string
	TelegramID       *int64
	AdminGroupID     *int64
	AdminGroupTitle  *string
	IsEnabled        bool
	IsManager        bool
	LastRelayError   *string
	LastRelayErrorAt *int64
	HealthStatus     string
	LastError        *string
	LastPolledAt     *int64
	CreatedAt        int64
	UpdatedAt        int64
}

// ToDTO 投影成对外结构。明文与密文 token 都不出现在结果里。
func (b BotRow) ToDTO() domain.Bot {
	return domain.Bot{
		ID:               b.ID,
		Name:             b.Name,
		Username:         b.Username,
		TokenMask:        b.TokenMask,
		TelegramID:       b.TelegramID,
		AdminGroupID:     b.AdminGroupID,
		AdminGroupTitle:  b.AdminGroupTitle,
		IsEnabled:        b.IsEnabled,
		IsManager:        b.IsManager,
		LastRelayError:   b.LastRelayError,
		LastRelayErrorAt: b.LastRelayErrorAt,
		HealthStatus:     b.HealthStatus,
		LastError:        b.LastError,
		LastPolledAt:     b.LastPolledAt,
		CreatedAt:        b.CreatedAt,
		UpdatedAt:        b.UpdatedAt,
	}
}

// Sealed 把密文字段组装成解密需要的形态。
func (b BotRow) Sealed() secret.Sealed {
	return secret.Sealed{Cipher: b.TokenCipher, IV: b.TokenIV, Tag: b.TokenTag}
}

const botColumns = `id, name, username, token_cipher, token_iv, token_tag, token_mask,
	telegram_id, admin_group_id, admin_group_title, is_enabled, is_manager,
	last_relay_error, last_relay_error_at, health_status,
	last_error, last_polled_at, created_at, updated_at`

func scanBot(row interface{ Scan(...any) error }) (BotRow, error) {
	var b BotRow
	var isEnabled, isManager int
	err := row.Scan(
		&b.ID, &b.Name, &b.Username, &b.TokenCipher, &b.TokenIV, &b.TokenTag, &b.TokenMask,
		&b.TelegramID, &b.AdminGroupID, &b.AdminGroupTitle, &isEnabled, &isManager,
		&b.LastRelayError, &b.LastRelayErrorAt, &b.HealthStatus,
		&b.LastError, &b.LastPolledAt, &b.CreatedAt, &b.UpdatedAt,
	)
	b.IsEnabled = isEnabled == 1
	b.IsManager = isManager == 1
	return b, err
}

// ListBots 按 id 升序返回全部机器人。
func (s *Store) ListBots(ctx context.Context) ([]BotRow, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+botColumns+` FROM bots ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("列出机器人: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]BotRow, 0, 4)
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListEnabledBots 只返回已启用的机器人；启动时用它。
func (s *Store) ListEnabledBots(ctx context.Context) ([]BotRow, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+botColumns+` FROM bots WHERE is_enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("列出已启用机器人: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]BotRow, 0, 4)
	for rows.Next() {
		b, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBot 按 id 取机器人。
func (s *Store) GetBot(ctx context.Context, id int64) (BotRow, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+botColumns+` FROM bots WHERE id = ?`, id)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BotRow{}, ErrNotFound
	}
	return b, err
}

// GetBotByUsername 按用户名取，用于创建时判重。
func (s *Store) GetBotByUsername(ctx context.Context, username string) (BotRow, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+botColumns+` FROM bots WHERE username = ?`, username)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BotRow{}, ErrNotFound
	}
	return b, err
}

// CreateBotInput 是建机器人需要的全部字段。
type CreateBotInput struct {
	Name            string
	Username        string
	Sealed          secret.Sealed
	TokenMask       string
	TelegramID      *int64
	AdminGroupID    *int64
	AdminGroupTitle *string
	Settings        BotSettings
}

// CreateBot 在一个事务里建机器人 + 它的默认设置。
//
// 必须同事务：只建了 bot 没建 settings 的话，后续读设置会走补默认值的
// 分支，而那个分支的值与这里的可能不一致 —— 表现为「刚建的机器人
// 配置跟预期不一样」，而且只在竞态时才出现。
func (s *Store) CreateBot(ctx context.Context, in CreateBotInput) (BotRow, error) {
	var created BotRow
	err := s.WithTx(ctx, func(tx *Tx) error {
		now := time.Now().UnixMilli()
		res, err := tx.Exec(`
			INSERT INTO bots (name, username, token_cipher, token_iv, token_tag, token_mask,
				telegram_id, admin_group_id, admin_group_title, is_enabled, health_status,
				last_error, last_polled_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'unknown', NULL, NULL, ?, ?)`,
			in.Name, in.Username, in.Sealed.Cipher, in.Sealed.IV, in.Sealed.Tag, in.TokenMask,
			in.TelegramID, in.AdminGroupID, in.AdminGroupTitle, now, now)
		if err != nil {
			return fmt.Errorf("写入机器人: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}

		if err := insertBotSettings(tx, id, in.Settings); err != nil {
			return err
		}

		created = BotRow{
			ID: id, Name: in.Name, Username: in.Username,
			TokenCipher: in.Sealed.Cipher, TokenIV: in.Sealed.IV, TokenTag: in.Sealed.Tag,
			TokenMask: in.TokenMask, TelegramID: in.TelegramID,
			AdminGroupID: in.AdminGroupID, AdminGroupTitle: in.AdminGroupTitle,
			IsEnabled: true, HealthStatus: domain.HealthUnknown,
			CreatedAt: now, UpdatedAt: now,
		}
		return nil
	})
	return created, err
}

// UpdateBotFields 是允许被修改的字段集合。
// 用零值表示「不改」，因此布尔字段用指针 —— 否则「设为 false」与
// 「不修改」无法区分，而这正是关不掉机器人的经典 bug。
type UpdateBotFields struct {
	Name            *string
	AdminGroupID    *int64
	AdminGroupTitle *string
	IsEnabled       *bool
	Sealed          *secret.Sealed
	TokenMask       *string
	TelegramID      *int64
}

// UpdateBot 按需更新字段。
func (s *Store) UpdateBot(ctx context.Context, id int64, f UpdateBotFields) error {
	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UnixMilli()}

	add := func(clause string, value any) {
		sets = append(sets, clause)
		args = append(args, value)
	}

	if f.Name != nil {
		add("name = ?", *f.Name)
	}
	if f.AdminGroupID != nil {
		add("admin_group_id = ?", *f.AdminGroupID)
	}
	if f.AdminGroupTitle != nil {
		add("admin_group_title = ?", *f.AdminGroupTitle)
	}
	if f.IsEnabled != nil {
		add("is_enabled = ?", boolToInt(*f.IsEnabled))
	}
	if f.Sealed != nil {
		add("token_cipher = ?", f.Sealed.Cipher)
		add("token_iv = ?", f.Sealed.IV)
		add("token_tag = ?", f.Sealed.Tag)
	}
	if f.TokenMask != nil {
		add("token_mask = ?", *f.TokenMask)
	}
	if f.TelegramID != nil {
		add("telegram_id = ?", *f.TelegramID)
	}

	args = append(args, id)
	query := "UPDATE bots SET " + joinComma(sets) + " WHERE id = ?"

	_, err := s.write.ExecContext(ctx, query, args...)
	return err
}

// UpdateBotHealth 只更新健康状态，是中继热路径上最频繁的一次写入。
func (s *Store) UpdateBotHealth(ctx context.Context, id int64, status string, lastErr *string) error {
	now := time.Now().UnixMilli()
	var polledAt any
	if status == domain.HealthOnline {
		polledAt = now
	}
	_, err := s.write.ExecContext(ctx, `
		UPDATE bots
		SET health_status = ?, last_error = ?,
		    last_polled_at = COALESCE(?, last_polled_at), updated_at = ?
		WHERE id = ?`, status, lastErr, polledAt, now, id)
	return err
}

// UpdateBotIdentity 在 getMe 之后同步真实用户名与 id。
func (s *Store) UpdateBotIdentity(ctx context.Context, id, telegramID int64, username string) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE bots SET telegram_id = ?, username = ?, updated_at = ? WHERE id = ?`,
		telegramID, username, time.Now().UnixMilli(), id)
	return err
}

// DeleteBot 删除机器人。外键级联会带走它的会话、消息、规则与统计行。
func (s *Store) DeleteBot(ctx context.Context, id int64) error {
	_, err := s.write.ExecContext(ctx, `DELETE FROM bots WHERE id = ?`, id)
	return err
}

// BotImpact 是删除前的「影响面」统计，用于二次确认文案。
type BotImpact struct {
	Topics int64 `json:"topics"`
	Rules  int64 `json:"rules"`
}

// GetBotImpact 统计删除会波及多少数据。
func (s *Store) GetBotImpact(ctx context.Context, id int64) (BotImpact, error) {
	var impact BotImpact
	err := s.read.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM topics WHERE bot_id = ?),
		       (SELECT COUNT(*) FROM ad_rules WHERE bot_id = ?)`, id, id).
		Scan(&impact.Topics, &impact.Rules)
	return impact, err
}

// ────────────────────────────── 长轮询 offset ──────────────────────────────

// LoadOffset 读取某个机器人的长轮询 offset。
func (s *Store) LoadOffset(ctx context.Context, botID int64) (int64, error) {
	var offset int64
	err := s.read.QueryRowContext(ctx,
		`SELECT offset FROM bot_offsets WHERE bot_id = ?`, botID).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil // 首次启动，从 0 开始
	}
	return offset, err
}

// SaveOffset 持久化 offset。
// 延迟写：调用方应当节流，不必每批更新都落盘 —— 丢了最多重复处理一次更新，
// 而每次 getUpdates 都写一次库是纯粹的浪费。
func (s *Store) SaveOffset(ctx context.Context, botID, offset int64) error {
	_, err := s.write.ExecContext(ctx, `
		INSERT INTO bot_offsets (bot_id, offset, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(bot_id) DO UPDATE SET offset = excluded.offset, updated_at = excluded.updated_at`,
		botID, offset, time.Now().UnixMilli())
	return err
}

// ────────────────────────────── 机器人设置 ──────────────────────────────

// BotSettings 是单个机器人的全部可调配置。
// 与 domain 里的 DTO 同构，直接复用它的 JSON 标签。
type BotSettings struct {
	BotID               int64          `json:"botId"`
	TopicNameTemplate   string         `json:"topicNameTemplate"`
	TopicIconColor      int            `json:"topicIconColor"`
	AutoCloseHours      *int           `json:"autoCloseHours"`
	PinTopicHeader      bool           `json:"pinTopicHeader"`
	GreetingText        string         `json:"greetingText"`
	WarnTemplate        string         `json:"warnTemplate"`
	MuteTemplate        string         `json:"muteTemplate"`
	BanTemplate         string         `json:"banTemplate"`
	SilenceTemplate     string         `json:"silenceTemplate"`
	AlertCardTemplate   string         `json:"alertCardTemplate"`
	TopicHeaderTemplate string         `json:"topicHeaderTemplate"`
	RulesEnabled        bool           `json:"rulesEnabled"`
	NotifyAdmins        bool           `json:"notifyAdmins"`
	Escalation          EscalationConf `json:"escalation"`
	DeleteOriginMessage bool           `json:"deleteOriginMessage"`
	MirrorEdits         bool           `json:"mirrorEdits"`
	MirrorDeletes       bool           `json:"mirrorDeletes"`
	CoalesceWindowMs    int            `json:"coalesceWindowMs"`
	CoalesceThreshold   int            `json:"coalesceThreshold"`
	FloodThreshold      int            `json:"floodThreshold"`
	NotifyOnUnreachable bool           `json:"notifyOnUnreachable"`
}

// EscalationConf 是阶梯处罚配置的别名，避免在这个文件里重复定义。
type EscalationConf = domain.EscalationConfig

const botSettingsColumns = `bot_id, topic_name_template, topic_icon_color, auto_close_hours,
	pin_topic_header, greeting_text, warn_template, mute_template, ban_template,
	silence_template, alert_card_template, topic_header_template, rules_enabled,
	notify_admins, escalation, delete_origin_message, mirror_edits, mirror_deletes,
	coalesce_window_ms, coalesce_threshold, flood_threshold, notify_on_unreachable`

// GetBotSettings 读设置；行不存在时返回默认值（并补写一行）。
//
// 补写而不是报错：settings 行缺失只可能来自手工改库或早期版本升级，
// 让机器人因为「少了条配置」而拒绝服务是没有道理的。
func (s *Store) GetBotSettings(ctx context.Context, botID int64) (BotSettings, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+botSettingsColumns+` FROM bot_settings WHERE bot_id = ?`, botID)

	settings, err := scanBotSettings(row, botID)
	if errors.Is(err, sql.ErrNoRows) {
		def := DefaultBotSettings(botID)
		if err := s.WriteBotSettings(ctx, def); err != nil {
			return def, err
		}
		return def, nil
	}
	return settings, err
}

func scanBotSettings(row interface{ Scan(...any) error }, botID int64) (BotSettings, error) {
	var s BotSettings
	var escalationJSON string
	var pinHeader, rulesEnabled, notifyAdmins, deleteOrigin, mirrorEdits, mirrorDeletes, notifyUnreachable int

	err := row.Scan(
		&s.BotID, &s.TopicNameTemplate, &s.TopicIconColor, &s.AutoCloseHours,
		&pinHeader, &s.GreetingText, &s.WarnTemplate, &s.MuteTemplate, &s.BanTemplate,
		&s.SilenceTemplate, &s.AlertCardTemplate, &s.TopicHeaderTemplate, &rulesEnabled,
		&notifyAdmins, &escalationJSON, &deleteOrigin, &mirrorEdits, &mirrorDeletes,
		&s.CoalesceWindowMs, &s.CoalesceThreshold, &s.FloodThreshold, &notifyUnreachable,
	)
	if err != nil {
		return s, err
	}

	s.BotID = botID
	s.PinTopicHeader = pinHeader == 1
	s.RulesEnabled = rulesEnabled == 1
	s.NotifyAdmins = notifyAdmins == 1
	s.DeleteOriginMessage = deleteOrigin == 1
	s.MirrorEdits = mirrorEdits == 1
	s.MirrorDeletes = mirrorDeletes == 1
	s.NotifyOnUnreachable = notifyUnreachable == 1

	// 解析失败时回落到默认阶梯而不是报错：这些值直接决定用户会不会被
	// 禁言，一次改库改坏了就让机器人停止中继，代价远大于静默用回默认。
	if err := json.Unmarshal([]byte(escalationJSON), &s.Escalation); err != nil {
		s.Escalation = DefaultEscalation()
	}
	if len(s.Escalation.Steps) == 0 {
		s.Escalation = DefaultEscalation()
	}
	return s, nil
}

func insertBotSettings(tx *Tx, botID int64, s BotSettings) error {
	s.BotID = botID
	escalationJSON, err := json.Marshal(s.Escalation)
	if err != nil {
		return fmt.Errorf("序列化阶梯配置: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO bot_settings (`+botSettingsColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bot_id) DO NOTHING`,
		s.BotID, s.TopicNameTemplate, s.TopicIconColor, s.AutoCloseHours,
		boolToInt(s.PinTopicHeader), s.GreetingText, s.WarnTemplate, s.MuteTemplate,
		s.BanTemplate, s.SilenceTemplate, s.AlertCardTemplate, s.TopicHeaderTemplate,
		boolToInt(s.RulesEnabled), boolToInt(s.NotifyAdmins), string(escalationJSON),
		boolToInt(s.DeleteOriginMessage), boolToInt(s.MirrorEdits), boolToInt(s.MirrorDeletes),
		s.CoalesceWindowMs, s.CoalesceThreshold, s.FloodThreshold, boolToInt(s.NotifyOnUnreachable),
	)
	return err
}

// WriteBotSettings 整行覆盖写设置。
func (s *Store) WriteBotSettings(ctx context.Context, settings BotSettings) error {
	err := s.WithTx(ctx, func(tx *Tx) error {
		return upsertBotSettings(tx, settings)
	})
	return err
}

func upsertBotSettings(tx *Tx, s BotSettings) error {
	escalationJSON, err := json.Marshal(s.Escalation)
	if err != nil {
		return fmt.Errorf("序列化阶梯配置: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO bot_settings (`+botSettingsColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bot_id) DO UPDATE SET
			topic_name_template = excluded.topic_name_template,
			topic_icon_color = excluded.topic_icon_color,
			auto_close_hours = excluded.auto_close_hours,
			pin_topic_header = excluded.pin_topic_header,
			greeting_text = excluded.greeting_text,
			warn_template = excluded.warn_template,
			mute_template = excluded.mute_template,
			ban_template = excluded.ban_template,
			silence_template = excluded.silence_template,
			alert_card_template = excluded.alert_card_template,
			topic_header_template = excluded.topic_header_template,
			rules_enabled = excluded.rules_enabled,
			notify_admins = excluded.notify_admins,
			escalation = excluded.escalation,
			delete_origin_message = excluded.delete_origin_message,
			mirror_edits = excluded.mirror_edits,
			mirror_deletes = excluded.mirror_deletes,
			coalesce_window_ms = excluded.coalesce_window_ms,
			coalesce_threshold = excluded.coalesce_threshold,
			flood_threshold = excluded.flood_threshold,
			notify_on_unreachable = excluded.notify_on_unreachable`,
		s.BotID, s.TopicNameTemplate, s.TopicIconColor, s.AutoCloseHours,
		boolToInt(s.PinTopicHeader), s.GreetingText, s.WarnTemplate, s.MuteTemplate,
		s.BanTemplate, s.SilenceTemplate, s.AlertCardTemplate, s.TopicHeaderTemplate,
		boolToInt(s.RulesEnabled), boolToInt(s.NotifyAdmins), string(escalationJSON),
		boolToInt(s.DeleteOriginMessage), boolToInt(s.MirrorEdits), boolToInt(s.MirrorDeletes),
		s.CoalesceWindowMs, s.CoalesceThreshold, s.FloodThreshold, boolToInt(s.NotifyOnUnreachable),
	)
	return err
}

// ────────────────────────────── 小工具 ──────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// ────────────────────────────── 管理机器人 ──────────────────────────────

// SetManagerBot 把某台机器人绑定为管理机器人（控制台）。
//
// 「控制台」全局只有一台 —— 它是这个项目的操作入口，而不是一类角色。
// 所以这里在**同一个事务**里清旧的点亮的：分成两次 UPDATE 的话，
// 中间失败会留下两台（或零台）控制台，而面板会显示成一个自相矛盾的状态。
func (s *Store) SetManagerBot(ctx context.Context, id int64) error {
	now := time.Now().UnixMilli()

	return s.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(
			`UPDATE bots SET is_manager = 0, updated_at = ? WHERE is_manager = 1 AND id != ?`,
			now, id); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE bots SET is_manager = 1, updated_at = ? WHERE id = ?`, now, id)
		return err
	})
}

// ClearManagerBot 解除某台机器人的控制台身份，它随即变回普通的中继机器人。
func (s *Store) ClearManagerBot(ctx context.Context, id int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE bots SET is_manager = 0, updated_at = ? WHERE id = ?`,
		time.Now().UnixMilli(), id)
	return err
}

// GetManagerBot 取当前的控制台机器人。没有则返回 ErrNotFound。
func (s *Store) GetManagerBot(ctx context.Context) (BotRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+botColumns+` FROM bots WHERE is_manager = 1 ORDER BY id LIMIT 1`)
	b, err := scanBot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BotRow{}, ErrNotFound
	}
	return b, err
}

// SetRelayError 记录最近一次中继失败的原因。
//
// 存在的意义：中继失败原本只写进容器日志，面板上完全看不出来 ——
// 管理员看到的现象是「用户发了消息但会话列表里什么都没有」，
// 而真正的原因（没绑群、机器人不是管理员、群没开 Topics）
// 一条都看不到。
func (s *Store) SetRelayError(ctx context.Context, botID int64, reason string) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE bots SET last_relay_error = ?, last_relay_error_at = ? WHERE id = ?`,
		reason, time.Now().UnixMilli(), botID)
	return err
}

// ClearRelayError 在一次成功中继后清掉错误标记。
//
// 只有成功才清：失败是「当前状态」，不是「历史事件」，
// 留着一条早就修好的错误会让人一直以为是坏的。
func (s *Store) ClearRelayError(ctx context.Context, botID int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE bots SET last_relay_error = NULL, last_relay_error_at = NULL
		 WHERE id = ? AND last_relay_error IS NOT NULL`, botID)
	return err
}
