package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tgs/server/internal/domain"
)

// ────────────────────────────── 联系人 ──────────────────────────────

// ContactRow 是 contacts 表的行。
type ContactRow struct {
	ID              int64
	BotID           int64
	TgUserID        int64
	Username        *string
	FirstName       *string
	LastName        *string
	LanguageCode    *string
	IsBlocked       bool
	IsUnreachable   bool
	ViolationScore  int
	LastViolationAt *int64
	FirstSeenAt     int64
	LastSeenAt      int64
	Notes           *string
}

// ToDTO 投影成对外结构。
func (c ContactRow) ToDTO() domain.Contact {
	return domain.Contact{
		ID:              c.ID,
		BotID:           c.BotID,
		TgUserID:        c.TgUserID,
		Username:        c.Username,
		FirstName:       c.FirstName,
		LastName:        c.LastName,
		LanguageCode:    c.LanguageCode,
		IsBlocked:       c.IsBlocked,
		IsUnreachable:   c.IsUnreachable,
		ViolationScore:  c.ViolationScore,
		LastViolationAt: c.LastViolationAt,
		FirstSeenAt:     c.FirstSeenAt,
		LastSeenAt:      c.LastSeenAt,
		Notes:           c.Notes,
	}
}

// DisplayName 返回适合展示的名字。
func (c ContactRow) DisplayName() string {
	parts := make([]string, 0, 2)
	if c.FirstName != nil && *c.FirstName != "" {
		parts = append(parts, *c.FirstName)
	}
	if c.LastName != nil && *c.LastName != "" {
		parts = append(parts, *c.LastName)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if c.Username != nil && *c.Username != "" {
		return *c.Username
	}
	return fmt.Sprintf("用户 %d", c.TgUserID)
}

const contactColumns = `id, bot_id, tg_user_id, username, first_name, last_name, language_code,
	is_blocked, is_unreachable, violation_score, last_violation_at, first_seen_at, last_seen_at, notes`

func scanContact(row interface{ Scan(...any) error }) (ContactRow, error) {
	var c ContactRow
	var blocked, unreachable int
	err := row.Scan(&c.ID, &c.BotID, &c.TgUserID, &c.Username, &c.FirstName, &c.LastName,
		&c.LanguageCode, &blocked, &unreachable, &c.ViolationScore, &c.LastViolationAt,
		&c.FirstSeenAt, &c.LastSeenAt, &c.Notes)
	c.IsBlocked = blocked == 1
	c.IsUnreachable = unreachable == 1
	return c, err
}

// UserProfile 是 upsertContact 需要的用户信息。
type UserProfile struct {
	ID           int64
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
}

// UpsertContact 取（不存在则创建）联系人档案。
//
// 这是中继热路径上每条消息都会调用的一次写。做了两处优化：
//   - 只在资料真的变了才 UPDATE，避免每条消息都产生一次无谓的 WAL 写入；
//   - 用户名/昵称的更新与 last_seen_at 分开，后者总是要更新的。
func (s *Store) UpsertContact(ctx context.Context, botID int64, u UserProfile) (ContactRow, error) {
	now := time.Now().UnixMilli()

	existing, err := s.GetContactByTgID(ctx, botID, u.ID)
	if err == nil {
		changed := !strEq(existing.Username, u.Username) ||
			!strEq(existing.FirstName, u.FirstName) ||
			!strEq(existing.LastName, u.LastName)

		if changed {
			_, err = s.write.ExecContext(ctx, `
				UPDATE contacts
				SET username = ?, first_name = ?, last_name = ?, language_code = ?, last_seen_at = ?
				WHERE id = ?`,
				nullable(u.Username), nullable(u.FirstName), nullable(u.LastName),
				nullable(u.LanguageCode), now, existing.ID)
			if err != nil {
				return existing, err
			}
			existing.Username = nullable(u.Username)
			existing.FirstName = nullable(u.FirstName)
			existing.LastName = nullable(u.LastName)
		} else {
			_, err = s.write.ExecContext(ctx,
				`UPDATE contacts SET last_seen_at = ? WHERE id = ?`, now, existing.ID)
			if err != nil {
				return existing, err
			}
		}
		existing.LastSeenAt = now
		return existing, nil
	}

	if !errors.Is(err, ErrNotFound) {
		return ContactRow{}, err
	}

	res, err := s.write.ExecContext(ctx, `
		INSERT INTO contacts (bot_id, tg_user_id, username, first_name, last_name, language_code,
			is_blocked, is_unreachable, violation_score, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
		botID, u.ID, nullable(u.Username), nullable(u.FirstName), nullable(u.LastName),
		nullable(u.LanguageCode), now, now)
	if err != nil {
		// 唯一约束冲突 = 另一个协程抢先建好了。
		// 依赖 contacts_bot_user_uniq 而不是加锁：代价远低于分布式锁，
		// 而竞态窗口只有「同一用户的两条消息几乎同时到达」这一种情况。
		if isUniqueViolation(err) {
			return s.GetContactByTgID(ctx, botID, u.ID)
		}
		return ContactRow{}, fmt.Errorf("创建联系人: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return ContactRow{}, err
	}
	return ContactRow{
		ID: id, BotID: botID, TgUserID: u.ID,
		Username: nullable(u.Username), FirstName: nullable(u.FirstName),
		LastName: nullable(u.LastName), LanguageCode: nullable(u.LanguageCode),
		FirstSeenAt: now, LastSeenAt: now,
	}, nil
}

// GetContactByTgID 按 Telegram 用户 id 取联系人（按机器人隔离）。
func (s *Store) GetContactByTgID(ctx context.Context, botID, tgUserID int64) (ContactRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+contactColumns+` FROM contacts WHERE bot_id = ? AND tg_user_id = ?`,
		botID, tgUserID)
	c, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ContactRow{}, ErrNotFound
	}
	return c, err
}

// GetContact 按主键取联系人。
func (s *Store) GetContact(ctx context.Context, id int64) (ContactRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+contactColumns+` FROM contacts WHERE id = ?`, id)
	c, err := scanContact(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ContactRow{}, ErrNotFound
	}
	return c, err
}

// SetContactBlocked 设置拉黑状态。
func (s *Store) SetContactBlocked(ctx context.Context, id int64, blocked bool) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE contacts SET is_blocked = ? WHERE id = ?`, boolToInt(blocked), id)
	return err
}

// SetContactUnreachable 标记用户屏蔽了机器人。
func (s *Store) SetContactUnreachable(ctx context.Context, botID, tgUserID int64, unreachable bool) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE contacts SET is_unreachable = ? WHERE bot_id = ? AND tg_user_id = ?`,
		boolToInt(unreachable), botID, tgUserID)
	return err
}

// UpdateContactNotes 更新备注。
func (s *Store) UpdateContactNotes(ctx context.Context, id int64, notes *string) error {
	_, err := s.write.ExecContext(ctx, `UPDATE contacts SET notes = ? WHERE id = ?`, notes, id)
	return err
}

// ────────────────────────────── 话题 ──────────────────────────────

// TopicRow 是 topics 表的行。
type TopicRow struct {
	ID              int64
	BotID           int64
	ContactID       int64
	MessageThreadID int64
	Title           string
	IconColor       int
	Status          string
	LastMessageAt   *int64
	PinnedHeaderID  *int64
	CreatedAt       int64
	ClosedAt        *int64
}

const topicColumns = `id, bot_id, contact_id, message_thread_id, title, icon_color, status,
	last_message_at, pinned_header_id, created_at, closed_at`

func scanTopic(row interface{ Scan(...any) error }) (TopicRow, error) {
	var t TopicRow
	err := row.Scan(&t.ID, &t.BotID, &t.ContactID, &t.MessageThreadID, &t.Title, &t.IconColor,
		&t.Status, &t.LastMessageAt, &t.PinnedHeaderID, &t.CreatedAt, &t.ClosedAt)
	return t, err
}

// GetTopicByContact 按联系人取话题（一人一话题）。
func (s *Store) GetTopicByContact(ctx context.Context, botID, contactID int64) (TopicRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+topicColumns+` FROM topics WHERE bot_id = ? AND contact_id = ?`, botID, contactID)
	t, err := scanTopic(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TopicRow{}, ErrNotFound
	}
	return t, err
}

// GetTopicByThread 按 message_thread_id 反查 —— 管理员回复路径的第一跳。
func (s *Store) GetTopicByThread(ctx context.Context, botID, threadID int64) (TopicRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+topicColumns+` FROM topics WHERE bot_id = ? AND message_thread_id = ?`,
		botID, threadID)
	t, err := scanTopic(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TopicRow{}, ErrNotFound
	}
	return t, err
}

// GetTopic 按主键取话题。
func (s *Store) GetTopic(ctx context.Context, id int64) (TopicRow, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+topicColumns+` FROM topics WHERE id = ?`, id)
	t, err := scanTopic(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TopicRow{}, ErrNotFound
	}
	return t, err
}

// CreateTopic 建话题记录。
func (s *Store) CreateTopic(ctx context.Context, botID, contactID, threadID int64, title string, iconColor int) (TopicRow, error) {
	now := time.Now().UnixMilli()
	res, err := s.write.ExecContext(ctx, `
		INSERT INTO topics (bot_id, contact_id, message_thread_id, title, icon_color, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'open', ?)`,
		botID, contactID, threadID, title, iconColor, now)
	if err != nil {
		if isUniqueViolation(err) {
			// 竞态：另一个协程抢先建好了。调用方会重新查一次。
			return TopicRow{}, ErrDuplicate
		}
		return TopicRow{}, fmt.Errorf("创建话题: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return TopicRow{}, err
	}
	return TopicRow{
		ID: id, BotID: botID, ContactID: contactID, MessageThreadID: threadID,
		Title: title, IconColor: iconColor, Status: domain.TopicOpen, CreatedAt: now,
	}, nil
}

// UpdateTopicStatus 改话题状态。
func (s *Store) UpdateTopicStatus(ctx context.Context, id int64, status string) error {
	var closedAt any
	if status == domain.TopicClosed || status == domain.TopicDeleted {
		closedAt = time.Now().UnixMilli()
	}
	_, err := s.write.ExecContext(ctx,
		`UPDATE topics SET status = ?, closed_at = ? WHERE id = ?`, status, closedAt, id)
	return err
}

// TouchTopic 刷新话题的最后活跃时间；仪表盘与自动归档都依赖它。
func (s *Store) TouchTopic(ctx context.Context, id int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE topics SET last_message_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

// SetTopicThread 更新话题的 thread id（话题被删除后重建时用）。
func (s *Store) SetTopicThread(ctx context.Context, id, threadID int64, title string) error {
	_, err := s.write.ExecContext(ctx, `
		UPDATE topics SET message_thread_id = ?, title = ?, status = 'open',
			closed_at = NULL, pinned_header_id = NULL, last_message_at = NULL
		WHERE id = ?`, threadID, title, id)
	return err
}

// SetTopicHeader 记录置顶头部消息的 id。
func (s *Store) SetTopicHeader(ctx context.Context, id, messageID int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE topics SET pinned_header_id = ? WHERE id = ?`, messageID, id)
	return err
}

// SetTopicTitle 更新标题（用户改名后同步）。
func (s *Store) SetTopicTitle(ctx context.Context, id int64, title string) error {
	_, err := s.write.ExecContext(ctx, `UPDATE topics SET title = ? WHERE id = ?`, title, id)
	return err
}

// ListIdleTopics 取出超过阈值没消息的开放话题，供自动归档使用。
func (s *Store) ListIdleTopics(ctx context.Context, botID int64, cutoff int64) ([]TopicRow, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT `+topicColumns+` FROM topics
		WHERE bot_id = ? AND status = 'open'
		  AND last_message_at IS NOT NULL AND last_message_at < ?`,
		botID, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []TopicRow
	for rows.Next() {
		t, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ────────────────────────────── 会话查询 ──────────────────────────────

// SessionQuery 是会话列表的筛选条件。
type SessionQuery struct {
	BotID       *int64
	Status      string
	Search      string
	FlaggedOnly bool
	Cursor      *int64
	Limit       int
}

// ListSessions 分页查询会话。
//
// 用相关子查询取「最后一条消息」而不是先查列表再逐条查：后者是典型的
// N+1，30 行列表就是 30 次往返。messages_topic_recent_idx 让子查询
// 走索引倒序取一行，代价可以忽略。
func (s *Store) ListSessions(ctx context.Context, q SessionQuery) (domain.Paginated[domain.SessionSummary], error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}

	where := []string{"1 = 1"}
	args := []any{}

	if q.BotID != nil {
		where = append(where, "t.bot_id = ?")
		args = append(args, *q.BotID)
	}
	if q.Status != "" {
		where = append(where, "t.status = ?")
		args = append(args, q.Status)
	}
	if q.FlaggedOnly {
		where = append(where, "c.violation_score > 0")
	}
	if q.Search != "" {
		where = append(where, "(c.username LIKE ? OR c.first_name LIKE ? OR c.last_name LIKE ? OR t.title LIKE ?)")
		like := "%" + q.Search + "%"
		args = append(args, like, like, like, like)
	}

	// 游标分页而不是 offset：会话会实时新增，offset 分页在翻页时
	// 必然出现重复或漏项。
	if q.Cursor != nil {
		where = append(where, "COALESCE(t.last_message_at, t.created_at) < ?")
		args = append(args, *q.Cursor)
	}

	query := `
		SELECT t.id, t.bot_id, b.name, b.username, t.contact_id,
		       c.first_name, c.last_name, c.username, c.tg_user_id,
		       t.message_thread_id, t.title, t.status, t.last_message_at,
		       t.created_at, t.closed_at, c.violation_score, c.is_blocked,
		       (SELECT COALESCE(m.text, m.caption) FROM messages m
		          WHERE m.topic_id = t.id ORDER BY m.created_at DESC LIMIT 1),
		       (SELECT m.direction FROM messages m
		          WHERE m.topic_id = t.id ORDER BY m.created_at DESC LIMIT 1),
		       (SELECT COUNT(*) FROM messages m WHERE m.topic_id = t.id)
		FROM topics t
		JOIN contacts c ON c.id = t.contact_id
		JOIN bots b ON b.id = t.bot_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY COALESCE(t.last_message_at, t.created_at) DESC
		LIMIT ?`
	args = append(args, q.Limit)

	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.Paginated[domain.SessionSummary]{}, fmt.Errorf("查询会话: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.SessionSummary, 0, q.Limit)
	for rows.Next() {
		var s domain.SessionSummary
		var firstName, lastName, username *string
		var lastPreview, lastDir *string
		if err := rows.Scan(
			&s.ID, &s.BotID, &s.BotName, &s.BotUsername, &s.ContactID,
			&firstName, &lastName, &username, &s.TgUserID,
			&s.ThreadID, &s.Title, &s.Status, &s.LastMessageAt,
			&s.CreatedAt, &s.ClosedAt, &s.ViolationScore, &s.IsBlocked,
			&lastPreview, &lastDir, &s.MessageCount,
		); err != nil {
			return domain.Paginated[domain.SessionSummary]{}, err
		}
		s.Username = username
		s.DisplayName = displayName(firstName, lastName, username, s.TgUserID)
		s.LastMessagePreview = truncatePtr(lastPreview, 120)
		s.LastMessageDirection = lastDir
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return domain.Paginated[domain.SessionSummary]{}, err
	}

	var total int64
	if err := s.read.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM topics t
		JOIN contacts c ON c.id = t.contact_id
		WHERE `+strings.Join(where[:len(where)-boolToInt(q.Cursor != nil)], " AND "),
		args[:len(args)-1-boolToInt(q.Cursor != nil)]...).Scan(&total); err != nil {
		total = 0 // 总数只是展示用，查不到不影响主流程
	}

	var nextCursor *int64
	if len(items) == q.Limit {
		last := items[len(items)-1]
		cursor := last.CreatedAt
		if last.LastMessageAt != nil {
			cursor = *last.LastMessageAt
		}
		nextCursor = &cursor
	}

	return domain.Paginated[domain.SessionSummary]{
		Items: items, NextCursor: nextCursor, Total: &total,
	}, nil
}

// GetSessionSummary 取单个会话的摘要。
func (s *Store) GetSessionSummary(ctx context.Context, topicID int64) (domain.SessionSummary, error) {
	result, err := s.ListSessions(ctx, SessionQuery{Limit: 1})
	if err == nil && len(result.Items) > 0 && result.Items[0].ID == topicID {
		return result.Items[0], nil
	}

	// 上面的快捷路径命中不了时走精确查询
	var summary domain.SessionSummary
	var firstName, lastName, username, lastPreview, lastDir *string
	err = s.read.QueryRowContext(ctx, `
		SELECT t.id, t.bot_id, b.name, b.username, t.contact_id,
		       c.first_name, c.last_name, c.username, c.tg_user_id,
		       t.message_thread_id, t.title, t.status, t.last_message_at,
		       t.created_at, t.closed_at, c.violation_score, c.is_blocked,
		       (SELECT COALESCE(m.text, m.caption) FROM messages m
		          WHERE m.topic_id = t.id ORDER BY m.created_at DESC LIMIT 1),
		       (SELECT m.direction FROM messages m
		          WHERE m.topic_id = t.id ORDER BY m.created_at DESC LIMIT 1),
		       (SELECT COUNT(*) FROM messages m WHERE m.topic_id = t.id)
		FROM topics t
		JOIN contacts c ON c.id = t.contact_id
		JOIN bots b ON b.id = t.bot_id
		WHERE t.id = ?`, topicID).Scan(
		&summary.ID, &summary.BotID, &summary.BotName, &summary.BotUsername, &summary.ContactID,
		&firstName, &lastName, &username, &summary.TgUserID,
		&summary.ThreadID, &summary.Title, &summary.Status, &summary.LastMessageAt,
		&summary.CreatedAt, &summary.ClosedAt, &summary.ViolationScore, &summary.IsBlocked,
		&lastPreview, &lastDir, &summary.MessageCount)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SessionSummary{}, ErrNotFound
	}
	if err != nil {
		return domain.SessionSummary{}, err
	}

	summary.Username = username
	summary.DisplayName = displayName(firstName, lastName, username, summary.TgUserID)
	summary.LastMessagePreview = truncatePtr(lastPreview, 120)
	summary.LastMessageDirection = lastDir
	return summary, nil
}

// ────────────────────────────── 消息 ──────────────────────────────

// MessageRow 是 messages 表的行。
type MessageRow struct {
	ID               int64
	BotID            int64
	TopicID          int64
	Direction        string
	SourceChatID     int64
	TgMessageID      int64
	DestChatID       *int64
	RelayedMessageID *int64
	ContentType      string
	Text             *string
	Caption          *string
	MediaGroupID     *string
	HasHiddenLink    bool
	HiddenLinks      []string
	RuleHitID        *int64
	SenderLabel      *string
	CreatedAt        int64
	EditedAt         *int64
	DeletedAt        *int64
}

const messageColumns = `id, bot_id, topic_id, direction, source_chat_id, tg_message_id,
	dest_chat_id, relayed_message_id, content_type, text, caption, media_group_id,
	has_hidden_link, hidden_links, rule_hit_id, sender_label, created_at, edited_at, deleted_at`

func scanMessage(row interface{ Scan(...any) error }) (MessageRow, error) {
	var m MessageRow
	var hiddenLinks *string
	var hasHidden int
	err := row.Scan(&m.ID, &m.BotID, &m.TopicID, &m.Direction, &m.SourceChatID, &m.TgMessageID,
		&m.DestChatID, &m.RelayedMessageID, &m.ContentType, &m.Text, &m.Caption, &m.MediaGroupID,
		&hasHidden, &hiddenLinks, &m.RuleHitID, &m.SenderLabel, &m.CreatedAt, &m.EditedAt, &m.DeletedAt)
	m.HasHiddenLink = hasHidden == 1
	if hiddenLinks != nil {
		_ = json.Unmarshal([]byte(*hiddenLinks), &m.HiddenLinks)
	}
	return m, err
}

// RecordMessageInput 是写入一条中继记录需要的全部字段。
type RecordMessageInput struct {
	BotID            int64
	TopicID          int64
	Direction        string
	SourceChatID     int64
	TgMessageID      int64
	DestChatID       *int64
	RelayedMessageID *int64
	Content          domain.MessageContent
	SenderLabel      *string
	RuleHitID        *int64
}

// RecordMessage 写入一条中继记录。
func (s *Store) RecordMessage(ctx context.Context, in RecordMessageInput) (MessageRow, error) {
	var hiddenLinks any
	if len(in.Content.HiddenLinks) > 0 {
		payload, _ := json.Marshal(in.Content.HiddenLinks)
		hiddenLinks = string(payload)
	}

	now := time.Now().UnixMilli()
	res, err := s.write.ExecContext(ctx, `
		INSERT INTO messages (bot_id, topic_id, direction, source_chat_id, tg_message_id,
			dest_chat_id, relayed_message_id, content_type, text, caption, media_group_id,
			has_hidden_link, hidden_links, rule_hit_id, sender_label, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.BotID, in.TopicID, in.Direction, in.SourceChatID, in.TgMessageID,
		in.DestChatID, in.RelayedMessageID, in.Content.Type, in.Content.Text, in.Content.Caption,
		in.Content.MediaGroupID, boolToInt(in.Content.HasHiddenLink), hiddenLinks,
		in.RuleHitID, in.SenderLabel, now)
	if err != nil {
		return MessageRow{}, fmt.Errorf("写入消息: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return MessageRow{}, err
	}

	// 媒体单独一张表：一个相册里的每条消息都可能带多份媒体，
	// 塞进主表会把消息表撑成宽表。
	for i, m := range in.Content.Media {
		if _, err := s.write.ExecContext(ctx, `
			INSERT INTO message_media (message_id, kind, file_id, file_unique_id, position)
			VALUES (?, ?, ?, ?, ?)`, id, m.Kind, m.FileID, m.FileUniqueID, i); err != nil {
			return MessageRow{}, fmt.Errorf("写入媒体: %w", err)
		}
	}

	return MessageRow{
		ID: id, BotID: in.BotID, TopicID: in.TopicID, Direction: in.Direction,
		SourceChatID: in.SourceChatID, TgMessageID: in.TgMessageID,
		DestChatID: in.DestChatID, RelayedMessageID: in.RelayedMessageID,
		ContentType: in.Content.Type, Text: in.Content.Text, Caption: in.Content.Caption,
		MediaGroupID: in.Content.MediaGroupID, HasHiddenLink: in.Content.HasHiddenLink,
		HiddenLinks: in.Content.HiddenLinks, RuleHitID: in.RuleHitID,
		SenderLabel: in.SenderLabel, CreatedAt: now,
	}, nil
}

// GetMessageMedia 取一条消息的媒体。
func (s *Store) GetMessageMedia(ctx context.Context, messageID int64) ([]domain.Media, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT kind, file_id, file_unique_id, position FROM message_media
		WHERE message_id = ? ORDER BY position`, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Media
	for rows.Next() {
		var m domain.Media
		if err := rows.Scan(&m.Kind, &m.FileID, &m.FileUniqueID, &m.Position); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMessage 按主键取消息行。副本撤回与消息详情都要用它。
func (s *Store) GetMessage(ctx context.Context, id int64) (MessageRow, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages WHERE id = ?`, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageRow{}, ErrNotFound
	}
	return m, err
}

// FindMessageBySource 按源消息反查映射 —— 编辑镜像的入口。
func (s *Store) FindMessageBySource(ctx context.Context, sourceChatID, tgMessageID int64) (MessageRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages
		 WHERE source_chat_id = ? AND tg_message_id = ? ORDER BY id DESC LIMIT 1`,
		sourceChatID, tgMessageID)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageRow{}, ErrNotFound
	}
	return m, err
}

// FindMessageByDest 按落地消息反查映射 —— 管理员回复时定位话题。
func (s *Store) FindMessageByDest(ctx context.Context, destChatID, relayedMessageID int64) (MessageRow, error) {
	row := s.read.QueryRowContext(ctx,
		`SELECT `+messageColumns+` FROM messages
		 WHERE dest_chat_id = ? AND relayed_message_id = ? ORDER BY id DESC LIMIT 1`,
		destChatID, relayedMessageID)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageRow{}, ErrNotFound
	}
	return m, err
}

// ListMessages 分页拉取某个话题下的消息（按 id 倒序）。
//
// 面板的聊天视图是「往上翻历史」的交互，所以按时间倒序取一页，
// 返回给前端后反转成正序渲染 —— 这样「加载更多」永远是取更早的消息，
// 与游标推进方向一致，不会出现游标错乱。
func (s *Store) ListMessages(ctx context.Context, topicID int64, cursor *int64, limit int) (domain.Paginated[domain.RelayedMessage], error) {
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT ` + messageColumns + ` FROM messages WHERE topic_id = ?`
	args := []any{topicID}
	if cursor != nil {
		query += ` AND id < ?`
		args = append(args, *cursor)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.Paginated[domain.RelayedMessage]{}, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.RelayedMessage, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return domain.Paginated[domain.RelayedMessage]{}, err
		}
		media, err := s.GetMessageMedia(ctx, m.ID)
		if err != nil {
			return domain.Paginated[domain.RelayedMessage]{}, err
		}
		items = append(items, m.ToDTO(media))
	}
	if err := rows.Err(); err != nil {
		return domain.Paginated[domain.RelayedMessage]{}, err
	}

	var nextCursor *int64
	if len(items) == limit {
		last := items[len(items)-1].ID
		nextCursor = &last
	}
	return domain.Paginated[domain.RelayedMessage]{Items: items, NextCursor: nextCursor}, nil
}

// ToDTO 把消息行投影成对外结构。
func (m MessageRow) ToDTO(media []domain.Media) domain.RelayedMessage {
	if media == nil {
		media = []domain.Media{}
	}
	hidden := m.HiddenLinks
	if hidden == nil {
		hidden = []string{}
	}
	return domain.RelayedMessage{
		ID: m.ID, TopicID: m.TopicID, Direction: m.Direction,
		Content: domain.MessageContent{
			Type: m.ContentType, Text: m.Text, Caption: m.Caption,
			Media: media, MediaGroupID: m.MediaGroupID,
			HasHiddenLink: m.HasHiddenLink, HiddenLinks: hidden,
		},
		IsDeleted:   m.DeletedAt != nil,
		EditedAt:    m.EditedAt,
		CreatedAt:   m.CreatedAt,
		RuleHitID:   m.RuleHitID,
		SenderLabel: m.SenderLabel,
	}
}

// MarkMessageDeleted 标记消息已删除并广播由调用方负责。
func (s *Store) MarkMessageDeleted(ctx context.Context, id int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE messages SET deleted_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

// MarkMessageEdited 标记消息已编辑并更新内容。
func (s *Store) MarkMessageEdited(ctx context.Context, id int64, text, caption *string) error {
	_, err := s.write.ExecContext(ctx, `
		UPDATE messages SET edited_at = ?,
			text = COALESCE(?, text), caption = COALESCE(?, caption)
		WHERE id = ?`, time.Now().UnixMilli(), text, caption, id)
	return err
}

// AttachRuleHit 把消息关联到一条命中记录。
func (s *Store) AttachRuleHit(ctx context.Context, messageID, ruleHitID int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE messages SET rule_hit_id = ? WHERE id = ?`, ruleHitID, messageID)
	return err
}

// ────────────────────────────── 小工具 ──────────────────────────────

func displayName(first, last, username *string, tgUserID int64) string {
	parts := make([]string, 0, 2)
	if first != nil && *first != "" {
		parts = append(parts, *first)
	}
	if last != nil && *last != "" {
		parts = append(parts, *last)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if username != nil && *username != "" {
		return *username
	}
	return fmt.Sprintf("用户 %d", tgUserID)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strEq(ptr *string, value string) bool {
	if ptr == nil {
		return value == ""
	}
	return *ptr == value
}

func truncatePtr(s *string, max int) *string {
	if s == nil {
		return nil
	}
	runes := []rune(*s)
	if len(runes) <= max {
		return s
	}
	out := string(runes[:max]) + "…"
	return &out
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
