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

// ────────────────────────────── 规则 CRUD ──────────────────────────────

// RuleInput 是创建/更新规则的字段集合。
type RuleInput struct {
	BotID     *int64
	Name      string
	Pattern   string
	Flags     string
	MatchMode string
	Target    string
	Action    string
	Severity  int
	Priority  int
	IsEnabled bool
	Note      *string
}

// CreateRule 新建规则。
func (s *Store) CreateRule(ctx context.Context, in RuleInput) (int64, error) {
	now := time.Now().UnixMilli()
	res, err := s.write.ExecContext(ctx, `
		INSERT INTO ad_rules (bot_id, name, pattern, flags, match_mode, target, action,
			severity, priority, is_enabled, is_system, note, hit_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 0, ?, ?)`,
		in.BotID, in.Name, in.Pattern, in.Flags, in.MatchMode, in.Target, in.Action,
		in.Severity, in.Priority, boolToInt(in.IsEnabled), in.Note, now, now)
	if err != nil {
		return 0, fmt.Errorf("创建规则: %w", err)
	}
	return res.LastInsertId()
}

// UpdateRule 更新规则。
func (s *Store) UpdateRule(ctx context.Context, id int64, in RuleInput) error {
	_, err := s.write.ExecContext(ctx, `
		UPDATE ad_rules
		SET name = ?, pattern = ?, flags = ?, match_mode = ?, target = ?, action = ?,
		    severity = ?, priority = ?, is_enabled = ?, note = ?,
		    auto_disabled_at = NULL, auto_disabled_reason = NULL, updated_at = ?
		WHERE id = ?`,
		in.Name, in.Pattern, in.Flags, in.MatchMode, in.Target, in.Action,
		in.Severity, in.Priority, boolToInt(in.IsEnabled), in.Note,
		time.Now().UnixMilli(), id)
	return err
}

// SetRuleEnabled 启停规则。
//
// 重新启用时清掉熔断标记，否则刚打开的规则立刻又被标成「已停用」——
// 管理员会以为改动没生效。
func (s *Store) SetRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	query := `UPDATE ad_rules SET is_enabled = ?, updated_at = ? WHERE id = ?`
	args := []any{boolToInt(enabled), time.Now().UnixMilli(), id}

	if enabled {
		query = `UPDATE ad_rules
			SET is_enabled = ?, auto_disabled_at = NULL, auto_disabled_reason = NULL, updated_at = ?
			WHERE id = ?`
	}
	_, err := s.write.ExecContext(ctx, query, args...)
	return err
}

// DeleteRule 删除规则。
// 命中审计里存的是规则快照，因此删掉规则不会让历史记录失去意义。
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	_, err := s.write.ExecContext(ctx, `DELETE FROM ad_rules WHERE id = ?`, id)
	return err
}

// ReorderRules 按给定顺序重写优先级（步长 10，留出手工微调的余地）。
func (s *Store) ReorderRules(ctx context.Context, ids []int64) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		now := time.Now().UnixMilli()
		for i, id := range ids {
			if _, err := tx.Exec(
				`UPDATE ad_rules SET priority = ?, updated_at = ? WHERE id = ?`,
				(i+1)*10, now, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetRule 取单条规则。
func (s *Store) GetRule(ctx context.Context, id int64) (domain.AdRule, error) {
	var r domain.AdRule
	var autoDisabledAt *int64
	var isEnabled, isSystem int
	err := s.read.QueryRowContext(ctx, `
		SELECT id, bot_id, name, pattern, flags, match_mode, target, action,
		       severity, priority, is_enabled, is_system, note, hit_count,
		       last_hit_at, auto_disabled_at, created_at, updated_at
		FROM ad_rules WHERE id = ?`, id).Scan(
		&r.ID, &r.BotID, &r.Name, &r.Pattern, &r.Flags, &r.MatchMode, &r.Target,
		&r.Action, &r.Severity, &r.Priority, &isEnabled, &isSystem, &r.Note,
		&r.HitCount, &r.LastHitAt, &autoDisabledAt, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AdRule{}, ErrNotFound
	}
	if err != nil {
		return domain.AdRule{}, err
	}
	r.IsEnabled = isEnabled == 1
	r.IsSystem = isSystem == 1
	r.AutoDisabled = autoDisabledAt != nil
	return r, nil
}

// ────────────────────────────── 命中审计 ──────────────────────────────

// RecordRuleHitInput 是写一条命中审计需要的字段。
type RecordRuleHitInput struct {
	RuleID            *int64
	RuleName          string
	RulePattern       string
	RuleFlags         string
	BotID             int64
	ContactID         int64
	TopicID           *int64
	MessageID         *int64
	MatchedText       string
	NormalizedExcerpt *string
	Outcomes          []string
	Severity          int
}

// RecordRuleHit 写一条命中审计。
//
// rule_name / rule_pattern / rule_flags 是**快照**：规则随时可能被改
// 甚至被删，而审计记录必须永远能回答「当时是按什么判定的」。
func (s *Store) RecordRuleHit(ctx context.Context, in RecordRuleHitInput) (int64, error) {
	outcomes, err := json.Marshal(in.Outcomes)
	if err != nil {
		return 0, err
	}

	res, err := s.write.ExecContext(ctx, `
		INSERT INTO rule_hits (rule_id, rule_name, rule_pattern, rule_flags, bot_id,
			contact_id, topic_id, message_id, matched_text, normalized_excerpt,
			outcomes, severity, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.RuleID, in.RuleName, in.RulePattern, in.RuleFlags, in.BotID,
		in.ContactID, in.TopicID, in.MessageID, in.MatchedText, in.NormalizedExcerpt,
		string(outcomes), in.Severity, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("写入命中审计: %w", err)
	}
	return res.LastInsertId()
}

// RuleHitQuery 是命中记录的筛选条件。
type RuleHitQuery struct {
	BotID     *int64
	RuleID    *int64
	ContactID *int64
	Search    string
	From      *int64
	To        *int64
	Cursor    *int64
	Limit     int
}

// ListRuleHits 分页查询命中记录。
func (s *Store) ListRuleHits(ctx context.Context, q RuleHitQuery) (domain.Paginated[domain.RuleHit], error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}

	where := []string{"1 = 1"}
	args := []any{}

	if q.BotID != nil {
		where = append(where, "h.bot_id = ?")
		args = append(args, *q.BotID)
	}
	if q.RuleID != nil {
		where = append(where, "h.rule_id = ?")
		args = append(args, *q.RuleID)
	}
	if q.ContactID != nil {
		where = append(where, "h.contact_id = ?")
		args = append(args, *q.ContactID)
	}
	if q.From != nil {
		where = append(where, "h.created_at >= ?")
		args = append(args, *q.From)
	}
	if q.To != nil {
		where = append(where, "h.created_at <= ?")
		args = append(args, *q.To)
	}
	if q.Search != "" {
		where = append(where, "(h.matched_text LIKE ? OR h.rule_name LIKE ? OR c.username LIKE ? OR c.first_name LIKE ?)")
		like := "%" + q.Search + "%"
		args = append(args, like, like, like, like)
	}
	// 游标用 id 而不是时间戳：同一毫秒内可能有多条命中，
	// 用时间戳做游标会漏掉同刻的记录。
	if q.Cursor != nil {
		where = append(where, "h.id < ?")
		args = append(args, *q.Cursor)
	}

	query := `
		SELECT h.id, h.rule_id, h.rule_name, h.rule_pattern, h.rule_flags,
		       h.bot_id, COALESCE(b.name, '（已删除）'),
		       h.contact_id, COALESCE(c.first_name, ''), c.last_name, c.username, COALESCE(c.tg_user_id, 0),
		       h.topic_id, t.message_thread_id,
		       h.matched_text, h.normalized_excerpt, h.outcomes, h.severity, h.created_at
		FROM rule_hits h
		LEFT JOIN bots b ON b.id = h.bot_id
		LEFT JOIN contacts c ON c.id = h.contact_id
		LEFT JOIN topics t ON t.id = h.topic_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY h.id DESC LIMIT ?`
	args = append(args, q.Limit)

	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.Paginated[domain.RuleHit]{}, fmt.Errorf("查询命中记录: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.RuleHit, 0, q.Limit)
	for rows.Next() {
		var h domain.RuleHit
		var firstName, lastName, username *string
		var outcomesJSON string

		if err := rows.Scan(
			&h.ID, &h.RuleID, &h.RuleName, &h.RulePattern, &h.RuleFlags,
			&h.BotID, &h.BotName,
			&h.ContactID, &firstName, &lastName, &username, &h.TgUserID,
			&h.TopicID, &h.ThreadID,
			&h.MatchedText, &h.NormalizedExcerpt, &outcomesJSON, &h.Severity, &h.CreatedAt,
		); err != nil {
			return domain.Paginated[domain.RuleHit]{}, err
		}

		h.ContactUsername = username
		h.ContactName = displayName(firstName, lastName, username, h.TgUserID)
		if err := json.Unmarshal([]byte(outcomesJSON), &h.Outcomes); err != nil {
			h.Outcomes = []string{}
		}
		items = append(items, h)
	}
	if err := rows.Err(); err != nil {
		return domain.Paginated[domain.RuleHit]{}, err
	}

	var nextCursor *int64
	if len(items) == q.Limit {
		last := items[len(items)-1].ID
		nextCursor = &last
	}
	return domain.Paginated[domain.RuleHit]{Items: items, NextCursor: nextCursor}, nil
}

// ────────────────────────────── 面板操作审计 ──────────────────────────────

// AuditInput 是一次面板操作。
type AuditInput struct {
	ActorType  string
	ActorID    *string
	Action     string
	TargetType *string
	TargetID   *string
	Detail     map[string]any
	IP         *string
}

// RecordAudit 写一条操作审计。
//
// **永不返回错误给调用方影响主流程**：审计是旁路，不能因为「日志写不进去」
// 就让「拉黑用户」失败。调用方拿到错误只需要记日志。
func (s *Store) RecordAudit(ctx context.Context, in AuditInput) (int64, error) {
	var detail any
	if len(in.Detail) > 0 {
		payload, err := json.Marshal(in.Detail)
		if err != nil {
			detail = nil
		} else {
			detail = string(payload)
		}
	}

	res, err := s.write.ExecContext(ctx, `
		INSERT INTO audit_log (actor_type, actor_id, action, target_type, target_id, detail, ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ActorType, in.ActorID, in.Action, in.TargetType, in.TargetID, detail, in.IP,
		time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AuditQuery 是操作审计的筛选条件。
type AuditQuery struct {
	Action    string
	ActorType string
	Search    string
	From      *int64
	To        *int64
	Cursor    *int64
	Limit     int
}

// ListAudit 分页查询操作审计。
func (s *Store) ListAudit(ctx context.Context, q AuditQuery) (domain.Paginated[domain.AuditEntry], error) {
	if q.Limit <= 0 {
		q.Limit = 30
	}

	where := []string{"1 = 1"}
	args := []any{}

	if q.Action != "" {
		where = append(where, "action LIKE ?")
		args = append(args, q.Action+"%")
	}
	if q.ActorType != "" {
		where = append(where, "actor_type = ?")
		args = append(args, q.ActorType)
	}
	if q.From != nil {
		where = append(where, "created_at >= ?")
		args = append(args, *q.From)
	}
	if q.To != nil {
		where = append(where, "created_at <= ?")
		args = append(args, *q.To)
	}
	if q.Search != "" {
		like := "%" + q.Search + "%"
		where = append(where, "(action LIKE ? OR actor_id LIKE ? OR target_id LIKE ?)")
		args = append(args, like, like, like)
	}
	if q.Cursor != nil {
		where = append(where, "id < ?")
		args = append(args, *q.Cursor)
	}

	query := `SELECT id, actor_type, actor_id, action, target_type, target_id, detail, ip, created_at
		FROM audit_log WHERE ` + strings.Join(where, " AND ") + ` ORDER BY id DESC LIMIT ?`
	args = append(args, q.Limit)

	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.Paginated[domain.AuditEntry]{}, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.AuditEntry, 0, q.Limit)
	for rows.Next() {
		var e domain.AuditEntry
		var detailJSON *string
		if err := rows.Scan(&e.ID, &e.ActorType, &e.ActorID, &e.Action, &e.TargetType,
			&e.TargetID, &detailJSON, &e.IP, &e.CreatedAt); err != nil {
			return domain.Paginated[domain.AuditEntry]{}, err
		}
		if detailJSON != nil {
			_ = json.Unmarshal([]byte(*detailJSON), &e.Detail)
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return domain.Paginated[domain.AuditEntry]{}, err
	}

	var nextCursor *int64
	if len(items) == q.Limit {
		last := items[len(items)-1].ID
		nextCursor = &last
	}
	return domain.Paginated[domain.AuditEntry]{Items: items, NextCursor: nextCursor}, nil
}

// ListAuditActions 返回出现过的动作名，用于筛选下拉框。
func (s *Store) ListAuditActions(ctx context.Context) ([]string, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT DISTINCT action FROM audit_log ORDER BY action`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			return nil, err
		}
		out = append(out, action)
	}
	return out, rows.Err()
}
