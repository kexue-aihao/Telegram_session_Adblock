package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/tgs/server/internal/domain"
)

// 命中审计的规则快照要带匹配方式。
//
// 起因：共现模式的 pattern 存的是阈值整数而不是正则。快照里只有
// pattern + flags 的话，审计面板只能把它展示成 `/3/` —— 读起来是一条
// 匹配字面量「3」的正则。审计页开头写着「命中记录保存了规则的完整快照，
// 因此即使规则后来被改或被删，这里依然能解释当时是按什么判定的」，
// 那就得说话算数。

func recordHit(t *testing.T, st *Store, botID, contactID int64, in RecordRuleHitInput) int64 {
	t.Helper()

	in.BotID = botID
	in.ContactID = contactID
	if in.Outcomes == nil {
		in.Outcomes = []string{"delete"}
	}
	id, err := st.RecordRuleHit(context.Background(), in)
	if err != nil {
		t.Fatalf("写命中审计失败: %v", err)
	}
	return id
}

func TestRecordRuleHitKeepsMatchMode(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	bot := createTestBot(t, st, "hitbot")
	contact, err := st.UpsertContact(ctx, bot.ID, UserProfile{ID: 9001, FirstName: "广告号"})
	if err != nil {
		t.Fatalf("建联系人失败: %v", err)
	}

	// 共现那条的 pattern 是阈值 3，不是正则
	recordHit(t, st, bot.ID, contact.ID, RecordRuleHitInput{
		RuleName: "多信号共现", RulePattern: "3", RuleFlags: "",
		RuleMatchMode: domain.MatchCooccurrence,
		MatchedText:   "命中 3 条规则：盗币黑话、群发工具、远控木马",
		Severity:      40,
	})
	recordHit(t, st, bot.ID, contact.ID, RecordRuleHitInput{
		RuleName: "加密货币 / 博彩引流", RulePattern: "usdt", RuleFlags: "iu",
		RuleMatchMode: domain.MatchRegex,
		MatchedText:   "1000usdt",
		Severity:      25,
	})

	hits, err := st.ListRuleHits(ctx, RuleHitQuery{Limit: 10})
	if err != nil {
		t.Fatalf("读取命中记录失败: %v", err)
	}
	if len(hits.Items) != 2 {
		t.Fatalf("命中记录数 = %d，期望 2", len(hits.Items))
	}

	byName := make(map[string]domain.RuleHit, len(hits.Items))
	for _, h := range hits.Items {
		byName[h.RuleName] = h
	}

	if got := byName["多信号共现"].RuleMatchMode; got != domain.MatchCooccurrence {
		t.Errorf("共现命中的 ruleMatchMode = %q，期望 %q", got, domain.MatchCooccurrence)
	}
	if got := byName["加密货币 / 博彩引流"].RuleMatchMode; got != domain.MatchRegex {
		t.Errorf("正则命中的 ruleMatchMode = %q，期望 %q", got, domain.MatchRegex)
	}
}

// 漏填匹配方式不能让写入失败。列是 NOT NULL，空串会撞上去 ——
// 而「拦了但没记录」比「记录里少一个字段」严重得多。
func TestRecordRuleHitDefaultsEmptyMatchMode(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	bot := createTestBot(t, st, "defaultbot")
	contact, err := st.UpsertContact(ctx, bot.ID, UserProfile{ID: 9002, FirstName: "无模式"})
	if err != nil {
		t.Fatalf("建联系人失败: %v", err)
	}

	id := recordHit(t, st, bot.ID, contact.ID, RecordRuleHitInput{
		RuleName: "老调用方", RulePattern: "usdt", RuleFlags: "iu", MatchedText: "1000usdt",
	})

	var mode string
	if err := st.Read().QueryRowContext(ctx,
		`SELECT rule_match_mode FROM rule_hits WHERE id = ?`, id).Scan(&mode); err != nil {
		t.Fatalf("读取命中失败: %v", err)
	}
	if mode != domain.MatchRegex {
		t.Errorf("空匹配方式应当回落到 %q，实际 %q", domain.MatchRegex, mode)
	}
}

// 加 rule_match_mode 之前的 rule_hits。
//
// 刻意不写外键：这一段验的是 ALTER 补列与老行回填，带上外键就得先造出
// bots / contacts 两棵完整的父表才能插进一条老命中。列名与索引用到的列
// 与线上一致 —— 索引建不出来同样会让迁移失败。
const legacyRuleHitsSchema = `
CREATE TABLE rule_hits (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id            INTEGER,
    rule_name          TEXT    NOT NULL,
    rule_pattern       TEXT    NOT NULL,
    rule_flags         TEXT    NOT NULL,
    bot_id             INTEGER NOT NULL,
    contact_id         INTEGER NOT NULL,
    topic_id           INTEGER,
    message_id         INTEGER,
    matched_text       TEXT    NOT NULL,
    normalized_excerpt TEXT,
    outcomes           TEXT    NOT NULL,
    severity           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL
);
`

// 老库升级：这一列必须补上，且已有记录回填成 'regex' ——
// 那正是它们此前的展示方式，升级不该改变历史记录的样子。
func TestMigrateAddsMatchModeToExistingHits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	if _, err := raw.Exec(legacyRuleHitsSchema); err != nil {
		t.Fatalf("建旧表失败: %v", err)
	}
	if _, err := raw.Exec(`
		INSERT INTO rule_hits (rule_name, rule_pattern, rule_flags, bot_id, contact_id,
			matched_text, outcomes, severity, created_at)
		VALUES ('老规则', 'usdt', 'iu', 1, 1, '1000usdt', '["delete"]', 25, 1)`); err != nil {
		t.Fatalf("插入旧命中失败: %v", err)
	}
	_ = raw.Close()

	ctx := context.Background()
	st, err := Open(ctx, "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("用新版打开旧库失败（迁移没扛住）: %v", err)
	}
	defer func() { _ = st.Close() }()

	if !hasColumn(t, st.Read(), "rule_hits", "rule_match_mode") {
		t.Fatal("迁移后 rule_hits 仍缺少列 rule_match_mode")
	}

	var mode string
	if err := st.Read().QueryRowContext(ctx,
		`SELECT rule_match_mode FROM rule_hits WHERE id = 1`).Scan(&mode); err != nil {
		t.Fatalf("读取老记录失败: %v", err)
	}
	if mode != domain.MatchRegex {
		t.Errorf("老记录回填的 rule_match_mode = %q，期望 %q", mode, domain.MatchRegex)
	}

	// 二次打开不能报错（addColumnIfMissing 必须把「已存在」当正常路径）
	st2, err := Open(ctx, "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	_ = st2.Close()
}
