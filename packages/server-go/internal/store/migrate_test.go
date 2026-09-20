package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// 增量迁移的验证。
//
// 这一段不是形式主义：真实的升级路径正是「用户已经有一个旧库，
// 装上带新列的版本后启动」。schema.sql 里全是 CREATE TABLE IF NOT EXISTS，
// 对已存在的表**完全不起作用** —— 新列只能靠 ALTER 补。
//
// 漏掉这一步的现象是启动直接失败（no such column），
// 而用户已经跑了几天的数据都在那个库里。

// oldSchema 是加 is_manager / last_relay_error 之前的 bots 表。
const oldSchema = `
CREATE TABLE bots (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL,
    username          TEXT    NOT NULL,
    token_cipher      TEXT    NOT NULL,
    token_iv          TEXT    NOT NULL,
    token_tag         TEXT    NOT NULL,
    token_mask        TEXT    NOT NULL,
    telegram_id       INTEGER,
    admin_group_id    INTEGER,
    admin_group_title TEXT,
    is_enabled        INTEGER NOT NULL DEFAULT 1,
    health_status     TEXT    NOT NULL DEFAULT 'unknown',
    last_error        TEXT,
    last_polled_at    INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
`

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("读取 %s 表结构失败: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestMigrateAddsColumnsToExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")

	// 1. 造一个旧版库：只有旧列，并且已经有一行数据
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := raw.Exec(oldSchema); err != nil {
		t.Fatalf("建旧表失败: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO bots (name, username, token_cipher, token_iv, token_tag, token_mask, created_at, updated_at)
		 VALUES ('旧机器人', 'oldbot', 'c', 'i', 't', 'mask', 1, 1)`); err != nil {
		t.Fatalf("插入旧数据失败: %v", err)
	}
	_ = raw.Close()

	// 2. 用新版打开 —— 这一步会跑迁移
	ctx := context.Background()
	st, err := Open(ctx, "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("用新版打开旧库失败（迁移没扛住）: %v", err)
	}
	defer func() { _ = st.Close() }()

	// 3. 新列应当已经补上
	for _, col := range []string{"is_manager", "last_relay_error", "last_relay_error_at"} {
		if !hasColumn(t, st.Read(), "bots", col) {
			t.Errorf("迁移后仍缺少列 %s", col)
		}
	}

	// 4. 老数据必须还在，且新列取到默认值
	row, err := st.GetBot(ctx, 1)
	if err != nil {
		t.Fatalf("读取旧数据失败: %v", err)
	}
	if row.Name != "旧机器人" {
		t.Errorf("旧数据被改动了：name = %q", row.Name)
	}
	if row.IsManager {
		t.Error("新列 is_manager 的默认值应当是 false")
	}
	if row.LastRelayError != nil {
		t.Error("新列 last_relay_error 的默认值应当是 NULL")
	}

	// 5. 重复打开不能报错（addColumnIfMissing 必须把「已存在」当正常路径）
	st2, err := Open(ctx, "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("二次迁移失败（说明「列已存在」没有被当成正常路径）: %v", err)
	}
	_ = st2.Close()
}

func TestMigrateOnFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.db")

	st, err := Open(context.Background(), "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("全新库初始化失败: %v", err)
	}
	defer func() { _ = st.Close() }()

	// schema.sql 里已经写了新列，所以全新库应当一步到位
	for _, col := range []string{"is_manager", "last_relay_error", "last_relay_error_at"} {
		if !hasColumn(t, st.Read(), "bots", col) {
			t.Errorf("全新库缺少列 %s", col)
		}
	}
}
