package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/tgs/server/internal/domain"
)

// ────────────────────────────── 处罚 ──────────────────────────────

// SanctionRow 是 sanctions 表的行。
type SanctionRow struct {
	ID        int64
	BotID     int64
	ContactID int64
	Type      string
	Reason    string
	RuleID    *int64
	ExpiresAt *int64
	IsActive  bool
	CreatedBy *string
	ScoreAt   int
	CreatedAt int64
	LiftedAt  *int64
}

// ToDTO 投影成对外结构。
func (s SanctionRow) ToDTO() domain.Sanction {
	return domain.Sanction{
		ID:        s.ID,
		BotID:     s.BotID,
		ContactID: s.ContactID,
		Type:      s.Type,
		Reason:    s.Reason,
		RuleID:    s.RuleID,
		ExpiresAt: s.ExpiresAt,
		IsActive:  s.IsActive,
		CreatedBy: s.CreatedBy,
		CreatedAt: s.CreatedAt,
		LiftedAt:  s.LiftedAt,
	}
}

const sanctionColumns = `id, bot_id, contact_id, type, reason, rule_id, expires_at,
	is_active, created_by, score_at, created_at, lifted_at`

func scanSanction(row interface{ Scan(...any) error }) (SanctionRow, error) {
	var s SanctionRow
	var active int
	err := row.Scan(&s.ID, &s.BotID, &s.ContactID, &s.Type, &s.Reason, &s.RuleID,
		&s.ExpiresAt, &active, &s.CreatedBy, &s.ScoreAt, &s.CreatedAt, &s.LiftedAt)
	s.IsActive = active == 1
	return s, err
}

// ListActiveSanctions 取当前生效的处罚（已排除过期项）。
//
// 过期的禁言在库里**仍然是 active**，但语义上已经失效。这里过滤而不是
// 顺手写回：读路径不该产生写放大，真正的清理由 ExpireDueSanctions 做。
func (s *Store) ListActiveSanctions(ctx context.Context, contactID int64) ([]SanctionRow, error) {
	now := time.Now().UnixMilli()
	rows, err := s.read.QueryContext(ctx, `
		SELECT `+sanctionColumns+` FROM sanctions
		WHERE contact_id = ? AND is_active = 1
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at DESC`, contactID, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SanctionRow
	for rows.Next() {
		row, err := scanSanction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// CreateSanction 写入一条处罚记录。
func (s *Store) CreateSanction(ctx context.Context, in SanctionRow) (int64, error) {
	res, err := s.write.ExecContext(ctx, `
		INSERT INTO sanctions (bot_id, contact_id, type, reason, rule_id, expires_at,
			is_active, created_by, score_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
		in.BotID, in.ContactID, in.Type, in.Reason, in.RuleID, in.ExpiresAt,
		in.CreatedBy, in.ScoreAt, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeactivateContactSanctions 让某联系人的全部生效处罚作废。
//
// 保留历史行（不删）只是为了审计可追溯，因此只翻 is_active。
func (s *Store) DeactivateContactSanctions(ctx context.Context, contactID int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE sanctions SET is_active = 0, lifted_at = ?
		 WHERE contact_id = ? AND is_active = 1`,
		time.Now().UnixMilli(), contactID)
	return err
}

// ExpireDueSanctions 把到期的禁言写回 inactive，返回处理条数。
//
// 由定时任务调用。永久处罚（拉黑、静默）的 expires_at 是 NULL，
// 必须显式排除 —— 虽然 SQL 里 `NULL <= ?` 为假不会误伤，但把条件写成
// `IS NULL OR ...` 这种形式的话，有人顺手改一下就会把永久处罚全部清掉。
func (s *Store) ExpireDueSanctions(ctx context.Context) (int64, error) {
	now := time.Now().UnixMilli()
	res, err := s.write.ExecContext(ctx, `
		UPDATE sanctions SET is_active = 0, lifted_at = ?
		WHERE is_active = 1 AND expires_at IS NOT NULL AND expires_at <= ?`, now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ────────────────────────────── 违规分 ──────────────────────────────

// UpdateViolationScore 更新违规分与最后违规时间。
func (s *Store) UpdateViolationScore(ctx context.Context, contactID int64, score int) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE contacts SET violation_score = ?, last_violation_at = ? WHERE id = ?`,
		score, time.Now().UnixMilli(), contactID)
	return err
}

// ResetViolations 清零违规分并解除全部处罚。
//
// 两件事必须一起做：只清分数会留下还在生效的禁言，
// 下次命中时状态机的档位判断也会错乱 —— 用户会因为「分数是 0 但被禁言」
// 而永远出不来。
func (s *Store) ResetViolations(ctx context.Context, contactID int64) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.Exec(
			`UPDATE contacts SET violation_score = 0, last_violation_at = NULL WHERE id = ?`,
			contactID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE sanctions SET is_active = 0, lifted_at = ? WHERE contact_id = ? AND is_active = 1`,
			time.Now().UnixMilli(), contactID)
		return err
	})
}

// CountRuleHitsForContact 统计某联系人最近的命中次数，供会话详情页展示。
func (s *Store) CountRuleHitsForContact(ctx context.Context, contactID int64) (int64, error) {
	var count int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rule_hits WHERE contact_id = ?`, contactID).Scan(&count)
	return count, err
}

// ────────────────────────────── 会话详情 ──────────────────────────────

// RecentHit 是会话详情页里的一条近期命中摘要。
type RecentHit struct {
	ID          int64  `json:"id"`
	RuleName    string `json:"ruleName"`
	MatchedText string `json:"matchedText"`
	Outcome     string `json:"outcome"`
	CreatedAt   int64  `json:"createdAt"`
}

// ListRecentHits 取某联系人最近的命中记录。
func (s *Store) ListRecentHits(ctx context.Context, contactID int64, limit int) ([]RecentHit, error) {
	rows, err := s.read.QueryContext(ctx, `
		SELECT id, rule_name, matched_text, outcomes, created_at
		FROM rule_hits WHERE contact_id = ? ORDER BY id DESC LIMIT ?`, contactID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []RecentHit
	for rows.Next() {
		var h RecentHit
		var outcomesJSON string
		if err := rows.Scan(&h.ID, &h.RuleName, &h.MatchedText, &outcomesJSON, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Outcome = summarizeOutcomes(outcomesJSON)
		out = append(out, h)
	}
	return out, rows.Err()
}

// summarizeOutcomes 把 JSON 数组压成一行可读文本。
//
// 解析失败时返回原始串而不是报错 —— 这只是面板上的一个展示摘要，
// 为一个装饰性字段让整个会话详情接口失败是不值得的。
func summarizeOutcomes(raw string) string {
	var outcomes []string
	if err := json.Unmarshal([]byte(raw), &outcomes); err != nil {
		return raw
	}
	return strings.Join(outcomes, " + ")
}
