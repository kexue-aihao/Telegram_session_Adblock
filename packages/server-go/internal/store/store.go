// Package store 是数据访问层。
//
// 一个关键的性能取向：**读写分离成两个连接池**。
//
// SQLite 在 WAL 模式下支持「多读一写」，但 database/sql 的连接池不知道
// 这件事 —— 它会把写事务随机分配到某个连接上，两个写并发时其中一个必然
// 拿到 SQLITE_BUSY，而 busy_timeout 只是让它傻等。把写池限制成单连接，
// 写入自然串行化（SQLite 本来就是串行的），读池则开到 CPU 核数，
// 面板查询与中继写入因此互不阻塞。
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	// 纯 Go 的 SQLite 驱动：无 CGO，交叉编译与静态链接都能用。
	// 换成 mattn/go-sqlite3 会引入 CGO 依赖，在 Windows 上需要 C 编译器，
	// Docker 构建也要多带一套 gcc —— 这正是本项目要避免的。
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store 持有两个连接池。所有写操作走 Write，所有读操作走 Read。
type Store struct {
	read  *sql.DB
	write *sql.DB

	path string
}

// Open 建立连接并应用表结构。
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	path, err := resolvePath(databaseURL)
	if err != nil {
		return nil, err
	}

	if path != "" && path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录: %w", err)
		}
	}

	dsn := buildDSN(path)

	write, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	// 写入串行化：SQLite 同时只允许一个写事务，让连接池去排队
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)

	read, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("打开只读池: %w", err)
	}
	read.SetMaxOpenConns(max(4, runtime.NumCPU()))
	read.SetMaxIdleConns(max(2, runtime.NumCPU()/2))

	if err := write.PingContext(ctx); err != nil {
		_ = write.Close()
		_ = read.Close()
		return nil, fmt.Errorf("连接数据库失败（%s）: %w", path, err)
	}

	store := &Store{read: read, write: write, path: path}
	if err := store.migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// buildDSN 把 pragma 编进 DSN。
//
// 必须走 DSN 而不是启动时 `db.Exec("PRAGMA ...")`：后者只对当时拿到的那
// 一条连接生效，而连接池会不断新建连接 —— 新连接上没有 foreign_keys，
// 于是 ON DELETE CASCADE 会在某次重启后静默失效。这是最隐蔽的一类 bug。
func buildDSN(path string) string {
	if path == ":memory:" {
		return "file::memory:?cache=shared&_pragma=foreign_keys(1)"
	}
	pragmas := []string{
		"journal_mode(WAL)",   // 中继写入与面板读取不互相阻塞
		"foreign_keys(1)",     // 默认是关的，不开则所有 ON DELETE CASCADE 全部失效
		"busy_timeout(5000)",  // 并发写入时给 SQLITE_BUSY 一个等待窗口
		"synchronous(NORMAL)", // WAL 下这是持久性与吞吐的平衡点
	}
	params := make([]string, 0, len(pragmas))
	for _, p := range pragmas {
		params = append(params, "_pragma="+p)
	}
	return "file:" + filepath.ToSlash(path) + "?" + strings.Join(params, "&")
}

func resolvePath(databaseURL string) (string, error) {
	if databaseURL == ":memory:" || databaseURL == "" {
		return ":memory:", nil
	}
	if !strings.HasPrefix(databaseURL, "file:") {
		return "", fmt.Errorf("DATABASE_URL 只支持 file: 形式，收到 %q", databaseURL)
	}
	path := strings.TrimPrefix(databaseURL, "file:")
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	return path, nil
}

// Read 返回只读连接池。用于所有 SELECT。
func (s *Store) Read() *sql.DB { return s.read }

// Write 返回单连接写池。用于所有 INSERT/UPDATE/DELETE。
func (s *Store) Write() *sql.DB { return s.write }

// Path 返回 SQLite 文件路径，用于启动日志（内存库返回 ":memory:"）。
func (s *Store) Path() string { return s.path }

func (s *Store) Close() error {
	return errors.Join(s.read.Close(), s.write.Close())
}

// ────────────────────────────── 事务 ──────────────────────────────

// Tx 是写事务的句柄。
//
// 它**不是** *sql.Tx：我们需要 `BEGIN IMMEDIATE`，而 database/sql 的
// BeginTx 只会发 `BEGIN`（延迟事务）。延迟事务在第一次写入时才尝试拿写锁，
// 此时若已有别的写事务在跑，SQLite 直接返回 SQLITE_BUSY，**不会**走
// busy_timeout 的等待 —— 表现为随机的「database is locked」。
// 立即取锁则从一开始就进入等待队列。
//
// 因此这里直接持有 *sql.Conn，自己管事务边界。
type Tx struct {
	conn *sql.Conn
	ctx  context.Context
}

func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(t.ctx, query, args...)
}

func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(t.ctx, query, args...)
}

func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(t.ctx, query, args...)
}

// WithTx 在一个立即模式下的事务里执行 fn。
// fn 返回错误即回滚，返回 nil 即提交。
func (s *Store) WithTx(ctx context.Context, fn func(tx *Tx) error) error {
	conn, err := s.write.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取写连接: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("开启事务: %w", err)
	}

	// 回滚时必须用一个未被取消的 context —— 否则调用方超时取消后，
	// ROLLBACK 会因为 ctx 已结束而失败，事务被吊在连接上直到连接关闭。
	rollback := func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	}

	if err := fn(&Tx{conn: conn, ctx: ctx}); err != nil {
		rollback()
		return err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		rollback()
		return fmt.Errorf("提交事务: %w", err)
	}
	return nil
}

// ────────────────────────────── 迁移 ──────────────────────────────

// migrate 应用表结构。
//
// 全部语句都是 CREATE ... IF NOT EXISTS，因此可以重复执行。
// 这个项目的表结构已经稳定，不值得引入迁移框架 —— 新增列时用
// addColumnIfMissing 即可，它同样把「已经加过」当成正常路径。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.write.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("应用表结构: %w", err)
	}

	// ── 增量列 ────────────────────────────────────────────────
	//
	// schema.sql 里的 CREATE TABLE IF NOT EXISTS 对**已存在**的表
	// 完全不起作用，所以给老库加列必须单独走 ALTER。
	// 早于这一版的库（v0.2.x 部署的）就属于这种情况。
	//
	// addColumnIfMissing 内部先查 PRAGMA table_info 再决定是否 ALTER，
	// 所以重复执行是安全的。
	incremental := []struct{ table, column, definition string }{
		{"bots", "is_manager", "INTEGER NOT NULL DEFAULT 0"},
		{"bots", "last_relay_error", "TEXT"},
		{"bots", "last_relay_error_at", "INTEGER"},
		// 系统规则同步用。老库升级上来时是 NULL，syncSystemRules 把
		// 「NULL」当作「从未同步过」，因此这些行会被代码接管一次。
		{"ad_rules", "system_fingerprint", "TEXT"},
		// 命中审计的规则快照补上匹配方式（见 schema.sql）。老记录回填成
		// 'regex' —— 那正是它们此前的展示方式，行为不变。
		{"rule_hits", "rule_match_mode", "TEXT NOT NULL DEFAULT 'regex'"},
	}
	for _, col := range incremental {
		if err := s.addColumnIfMissing(ctx, col.table, col.column, col.definition); err != nil {
			return fmt.Errorf("升级表结构: %w", err)
		}
	}
	return nil
}

// addColumnIfMissing 为既有库补列。
// SQLite 没有 ADD COLUMN IF NOT EXISTS，只能先查 pragma。
func (s *Store) addColumnIfMissing(ctx context.Context, table, column, definition string) error {
	rows, err := s.read.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("读取 %s 表结构: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil // 已存在
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = s.write.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	if err != nil {
		return fmt.Errorf("为 %s 增加列 %s: %w", table, column, err)
	}
	return nil
}
