package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tgs/server/internal/secret"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createTestBot(t *testing.T, st *Store, username string) BotRow {
	t.Helper()

	row, err := st.CreateBot(context.Background(), CreateBotInput{
		Name:      username,
		Username:  username,
		Sealed:    secret.Sealed{Cipher: "c", IV: "i", Tag: "t"},
		TokenMask: "1234:…mask",
		Settings:  DefaultBotSettings(0),
	})
	if err != nil {
		t.Fatalf("建机器人 %s 失败: %v", username, err)
	}
	return row
}

// 控制台（管理机器人）是**唯一**的绑定。
//
// 这条性质不能只做在 UI 里：面板、Telegram 命令、以及「添加时就勾选
// 管理机器人」三条路径都会写这个字段，任何一条漏掉「清掉旧的」，
// 系统里就会同时存在两台控制台 —— 而它们都能增删其他机器人。
func TestManagerBotIsExclusive(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	a := createTestBot(t, st, "bot_a")
	b := createTestBot(t, st, "bot_b")

	// 起初没有绑定
	if _, err := st.GetManagerBot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("初始状态不该有控制台，实际 err = %v", err)
	}
	if a.IsManager {
		t.Error("新建的机器人不该带控制台标记")
	}

	// 绑 A
	if err := st.SetManagerBot(ctx, a.ID); err != nil {
		t.Fatalf("绑定 A 失败: %v", err)
	}
	if row, err := st.GetManagerBot(ctx); err != nil || row.ID != a.ID {
		t.Fatalf("绑定后应取到 A，实际 row=%+v err=%v", row, err)
	}

	// 改绑 B —— A 必须被清掉，否则就是两台控制台
	if err := st.SetManagerBot(ctx, b.ID); err != nil {
		t.Fatalf("绑定 B 失败: %v", err)
	}
	rowA, err := st.GetBot(ctx, a.ID)
	if err != nil {
		t.Fatalf("读 A 失败: %v", err)
	}
	if rowA.IsManager {
		t.Error("改绑之后 A 上的控制台标记应当被清掉")
	}
	if row, err := st.GetManagerBot(ctx); err != nil || row.ID != b.ID {
		t.Fatalf("改绑后应取到 B，实际 row=%+v err=%v", row, err)
	}

	// 解绑后回到「没有控制台」
	if err := st.ClearManagerBot(ctx, b.ID); err != nil {
		t.Fatalf("解绑 B 失败: %v", err)
	}
	if _, err := st.GetManagerBot(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("解绑后不该还有控制台，实际 err = %v", err)
	}

	// 重复绑同一台是幂等的（面板可能重复点）
	if err := st.SetManagerBot(ctx, a.ID); err != nil {
		t.Fatalf("重新绑定 A 失败: %v", err)
	}
	if err := st.SetManagerBot(ctx, a.ID); err != nil {
		t.Fatalf("重复绑定 A 失败: %v", err)
	}
	if row, err := st.GetManagerBot(ctx); err != nil || row.ID != a.ID {
		t.Fatalf("重复绑定后仍应取到 A，实际 row=%+v err=%v", row, err)
	}
}

// 控制台不计入「托管了几个机器人」。
func TestManagerBotExcludedFromOverview(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	relay := createTestBot(t, st, "relay_bot")
	console := createTestBot(t, st, "console_bot")

	if err := st.SetManagerBot(ctx, console.ID); err != nil {
		t.Fatalf("绑定控制台失败: %v", err)
	}

	overview, err := st.ComputeOverview(ctx, time.UTC, OverviewRules{})
	if err != nil {
		t.Fatalf("读概览失败: %v", err)
	}
	if overview.Bots.Total != 1 {
		t.Errorf("机器人总数 = %d，期望 1（只算中继机器人）", overview.Bots.Total)
	}

	// 而控制台自己仍然要能被列出来、被启动
	rows, err := st.ListEnabledBots(ctx)
	if err != nil {
		t.Fatalf("列出机器人失败: %v", err)
	}
	ids := make(map[int64]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	if !ids[relay.ID] || !ids[console.ID] {
		t.Errorf("ListEnabledBots 应当同时包含中继机器人与控制台，实际 %v", ids)
	}
}
