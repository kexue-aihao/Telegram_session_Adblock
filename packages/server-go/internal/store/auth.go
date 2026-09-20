package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tgs/server/internal/secret"
)

// ErrNoAdminPassword 表示库里还没有管理员，且没有提供 ADMIN_PASSWORD。
// 这是首次启动的常见卡点，错误信息里必须写清楚怎么修。
var ErrNoAdminPassword = errors.New(
	"首次启动需要设置 ADMIN_PASSWORD 才能创建管理员账号。" +
		"请在 .env 中填入一个至少 8 位、同时包含字母和数字的密码后重启")

func hashAdminPassword(password string) (string, error) {
	if err := secret.PasswordPolicyError(password); err != nil {
		return "", fmt.Errorf("ADMIN_PASSWORD 不合规：%w", err)
	}
	return secret.HashPassword(password)
}

// AdminUser 是一条管理员记录。
type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
}

// GetAdmin 取第一个管理员。本产品是单管理员模型，因此不按用户名查 ——
// 登录页只有一个密码框，用户名是固定的。
func (s *Store) GetAdmin(ctx context.Context) (AdminUser, error) {
	var u AdminUser
	err := s.read.QueryRowContext(ctx,
		`SELECT id, username, password_hash FROM admin_users ORDER BY id LIMIT 1`).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	return u, err
}

// UpdateAdminPassword 更新密码哈希。
func (s *Store) UpdateAdminPassword(ctx context.Context, adminID int64, password string) error {
	hash, err := hashAdminPassword(password)
	if err != nil {
		return err
	}
	_, err = s.write.ExecContext(ctx,
		`UPDATE admin_users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, time.Now().UnixMilli(), adminID)
	return err
}

// ────────────────────────────── 会话 ──────────────────────────────

// AdminSession 是一条登录会话。
type AdminSession struct {
	ID        int64
	AdminID   int64
	Username  string
	ExpiresAt int64
	IP        *string
	UserAgent *string
	CreatedAt int64
}

// CreateSession 建立会话。存的是 token 的 SHA-256，不是 token 本身。
func (s *Store) CreateSession(
	ctx context.Context,
	adminID int64,
	token string,
	ip, userAgent *string,
	ttl time.Duration,
) error {
	now := time.Now().UnixMilli()
	_, err := s.write.ExecContext(ctx, `
		INSERT INTO admin_sessions (token_hash, admin_user_id, ip, user_agent, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		secret.HashToken(token), adminID, ip, userAgent, now, now+ttl.Milliseconds())
	return err
}

// GetSession 校验 token 并返回会话。
// 过期、已注销、token 不匹配一律返回 ErrNotFound —— 调用方不需要区分，
// 区分只会给攻击者提供信息。
func (s *Store) GetSession(ctx context.Context, token string) (AdminSession, error) {
	var sess AdminSession
	err := s.read.QueryRowContext(ctx, `
		SELECT s.id, s.admin_user_id, u.username, s.expires_at, s.ip, s.user_agent, s.created_at
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_user_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ?
		LIMIT 1`, secret.HashToken(token), time.Now().UnixMilli()).
		Scan(&sess.ID, &sess.AdminID, &sess.Username, &sess.ExpiresAt,
			&sess.IP, &sess.UserAgent, &sess.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSession{}, ErrNotFound
	}
	return sess, err
}

// RevokeSession 注销单个会话。
func (s *Store) RevokeSession(ctx context.Context, sessionID int64) error {
	_, err := s.write.ExecContext(ctx,
		`UPDATE admin_sessions SET revoked_at = ? WHERE id = ?`, time.Now().UnixMilli(), sessionID)
	return err
}

// RevokeAllSessions 注销全部会话，可保留一个（通常是当前这一个）。
//
// 改密码后必须踢掉其它设备 —— 那才是改密码的意义；但保留当前会话，
// 否则用户会被自己刚做完的操作登出，看起来像改失败了。
func (s *Store) RevokeAllSessions(ctx context.Context, exceptID int64) (int64, error) {
	res, err := s.write.ExecContext(ctx,
		`UPDATE admin_sessions SET revoked_at = ? WHERE revoked_at IS NULL AND id <> ?`,
		time.Now().UnixMilli(), exceptID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ────────────────────────────── 登录限流 ──────────────────────────────

// RecordLoginAttempt 记一次登录尝试。
func (s *Store) RecordLoginAttempt(ctx context.Context, ip string, succeeded bool) error {
	_, err := s.write.ExecContext(ctx,
		`INSERT INTO login_attempts (ip, succeeded, attempted_at) VALUES (?, ?, ?)`,
		ip, boolToInt(succeeded), time.Now().UnixMilli())
	return err
}

// CountRecentFailures 统计窗口内的失败次数。
//
// 关键细节：只统计**最近一次成功登录之后**的失败。否则一个用户输错 4 次、
// 登录成功、过一会儿又输错 1 次，就会被判定为 5 次失败而锁定 ——
// 明明他已经证明过自己是本人。
func (s *Store) CountRecentFailures(ctx context.Context, ip string, window time.Duration) (int, error) {
	since := time.Now().Add(-window).UnixMilli()

	var lastSuccess int64
	err := s.read.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(attempted_at), 0) FROM login_attempts WHERE ip = ? AND succeeded = 1`,
		ip).Scan(&lastSuccess)
	if err != nil {
		return 0, err
	}

	from := since
	if lastSuccess > from {
		from = lastSuccess
	}

	var count int
	err = s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM login_attempts WHERE ip = ? AND succeeded = 0 AND attempted_at > ?`,
		ip, from).Scan(&count)
	return count, err
}

// PruneAuthData 清理过期的会话与登录记录，避免这两张表无限增长。
func (s *Store) PruneAuthData(ctx context.Context) error {
	now := time.Now().UnixMilli()
	cutoff := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()

	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM admin_sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.write.ExecContext(ctx,
		`DELETE FROM login_attempts WHERE attempted_at < ?`, cutoff)
	return err
}
