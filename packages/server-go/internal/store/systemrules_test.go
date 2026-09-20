package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
)

// 系统规则同步的验证。
//
// 这一段的由来：seedRules 原先只在 ad_rules 为空时写入，于是**已经跑着的
// 部署永远拿不到后续版本新增或修正的规则**。修一条预置规则的正则，
// 对老实例完全不起作用 —— 那等于「修复」只对全新安装有效。
//
// 所以下面每一条边界都要单独钉住。

type ruleSnapshot struct {
	ID          int64
	Pattern     string
	IsEnabled   bool
	IsSystem    bool
	Fingerprint string
	UpdatedAt   int64
}

func fetchGlobalRule(t *testing.T, st *Store, name string) (ruleSnapshot, bool) {
	t.Helper()

	var r ruleSnapshot
	var enabled, system int
	var fp *string
	err := st.Read().QueryRowContext(context.Background(), `
		SELECT id, pattern, is_enabled, is_system, system_fingerprint, updated_at
		FROM ad_rules WHERE name = ? AND bot_id IS NULL`, name).
		Scan(&r.ID, &r.Pattern, &enabled, &system, &fp, &r.UpdatedAt)
	if err != nil {
		return ruleSnapshot{}, false
	}
	r.IsEnabled = enabled == 1
	r.IsSystem = system == 1
	if fp != nil {
		r.Fingerprint = *fp
	}
	return r, true
}

func seededStore(t *testing.T) *Store {
	t.Helper()

	st := newTestStore(t)
	if err := st.Seed(context.Background(), "admin", "Test1234"); err != nil {
		t.Fatalf("播种失败: %v", err)
	}
	return st
}

func TestSyncSeedsAllSystemRulesWithFingerprint(t *testing.T) {
	st := seededStore(t)

	for _, def := range SeedRules() {
		got, ok := fetchGlobalRule(t, st, def.Name)
		if !ok {
			t.Errorf("系统规则 %q 没有被写入", def.Name)
			continue
		}
		if !got.IsSystem {
			t.Errorf("%q 应当带 is_system 标记", def.Name)
		}
		if got.Fingerprint == "" {
			t.Errorf("%q 缺少指纹，下一轮同步会把它当成老库接管一次", def.Name)
		}
	}
}

// 老库升级：那一行没有指纹（system_fingerprint 是 NULL），本轮必须被接管，
// 也就是说 `\b` 的修正要能落到已经跑着的部署上。
func TestSyncUpgradesRuleFromOldInstall(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	const legacyPattern = `\b(?:usdt|btc|eth|trx)\b|比特币|以太坊|博彩|棋牌`
	now := time.Now().UnixMilli()
	if _, err := st.Write().ExecContext(ctx, `
		INSERT INTO ad_rules (bot_id, name, pattern, flags, match_mode, target, action,
			severity, priority, is_enabled, is_system, note, hit_count, created_at, updated_at)
		VALUES (NULL, ?, ?, 'iu', 'regex', 'all', 'delete', 25, 30, 1, 1, '', 0, ?, ?)`,
		"加密货币 / 博彩引流", legacyPattern, now, now); err != nil {
		t.Fatalf("造老规则失败: %v", err)
	}

	if err := st.syncSystemRules(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	got, ok := fetchGlobalRule(t, st, "加密货币 / 博彩引流")
	if !ok {
		t.Fatal("规则在同步后消失了")
	}
	if got.Pattern == legacyPattern {
		t.Fatal("老库里的旧正则没有被升级 —— 修复到不了已有部署")
	}
	if !strings.Contains(got.Pattern, `(?:^|[^A-Za-z])`) {
		t.Errorf("升级后的正则不是预期形态：%s", got.Pattern)
	}
	if got.Fingerprint == "" {
		t.Error("接管后应当打上指纹，否则下次启动还会再接管一遍")
	}
}

// 面板上写着预置规则「可以停用或修改」，那就得说话算数：
// 管理员改过的规则不能在下一次启动时被代码改回去。
func TestSyncDoesNotClobberAdminEdit(t *testing.T) {
	ctx := context.Background()
	st := seededStore(t)

	before, ok := fetchGlobalRule(t, st, "加密货币 / 博彩引流")
	if !ok {
		t.Fatal("找不到目标规则")
	}

	const adminPattern = `我自己改的正则|usdt`
	if err := st.UpdateRule(ctx, before.ID, RuleInput{
		Name: "加密货币 / 博彩引流", Pattern: adminPattern, Flags: "iu",
		MatchMode: domain.MatchRegex, Target: domain.TargetAll,
		Action: domain.ActionDelete, Severity: 25, Priority: 30, IsEnabled: true,
	}); err != nil {
		t.Fatalf("模拟管理员改动失败: %v", err)
	}

	if err := st.syncSystemRules(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	after, _ := fetchGlobalRule(t, st, "加密货币 / 博彩引流")
	if after.Pattern != adminPattern {
		t.Errorf("管理员的改动被同步覆盖了：%q", after.Pattern)
	}
}

// 停用不在受管字段里：升级可以改规则内容，但不能把管理员关掉的规则打开。
func TestSyncPreservesDisabledState(t *testing.T) {
	ctx := context.Background()
	st := seededStore(t)

	before, _ := fetchGlobalRule(t, st, "加密货币 / 博彩引流")
	if err := st.SetRuleEnabled(ctx, before.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}

	if err := st.syncSystemRules(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	after, _ := fetchGlobalRule(t, st, "加密货币 / 博彩引流")
	if after.IsEnabled {
		t.Error("同步把管理员停用的规则重新打开了")
	}
}

// 删掉的规则不复活 —— 否则管理员永远删不掉系统规则，每次重启它都回来。
func TestSyncDoesNotResurrectDeletedRule(t *testing.T) {
	ctx := context.Background()
	st := seededStore(t)

	before, ok := fetchGlobalRule(t, st, "短链接服务")
	if !ok {
		t.Fatal("找不到目标规则")
	}
	if err := st.DeleteRule(ctx, before.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	if err := st.syncSystemRules(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if _, exists := fetchGlobalRule(t, st, "短链接服务"); exists {
		t.Error("被管理员删掉的系统规则又被同步插回来了")
	}
}

// 重复同步必须是无操作：每次启动都刷一遍 updated_at，面板上的
// 「更新时间」就变成了「上次重启时间」，失去意义。
func TestSyncIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := seededStore(t)

	names := make([]string, 0, 16)
	for _, def := range SeedRules() {
		names = append(names, def.Name)
	}

	before := make(map[string]int64, len(names))
	for _, n := range names {
		r, ok := fetchGlobalRule(t, st, n)
		if !ok {
			t.Fatalf("找不到规则 %q", n)
		}
		before[n] = r.UpdatedAt
	}

	// 拉开时间差，否则「没变」和「同一毫秒内写了一次」分不出来
	time.Sleep(5 * time.Millisecond)

	if err := st.syncSystemRules(ctx); err != nil {
		t.Fatalf("第二次同步失败: %v", err)
	}

	for _, n := range names {
		after, _ := fetchGlobalRule(t, st, n)
		if after.UpdatedAt != before[n] {
			t.Errorf("%q 被重复写入了（updated_at %d → %d）", n, before[n], after.UpdatedAt)
		}
	}
}

// ────────────────────────────── 端到端 ──────────────────────────────

// 起点是这条广告：它对原来那五条预置规则的命中数是 0 ——
// 通篇没有链接、没有「加微信」，卖的是犯罪工具本身。
const crimewareAd = `🥰项目1：窃贼自动化机器人，使用免费能量低价能量吸引人们来，领取能量或者购买能量就可以轻松实现修改剪切板盗U

🥰项目2：
1.模拟你的项目链接去进行钱包授权
2.或者引导下载假钱包
（授权后的钱包U可以一次性全部盗完）必须要冷钱包才能授权

🥰项目3：远控-下载APP控制手机，用APP植入手机即可实现盗米

🥰群发软件一条龙
（不懂的小白过来手把手教会）

🥰了解认知差，才能少受骗！🥰`

func evaluate(t *testing.T, st *Store, text string) []rules.Hit {
	t.Helper()

	engine := rules.New(st.Read())
	normalized := rules.Normalize(text)
	hits, err := engine.Evaluate(context.Background(), rules.Context{
		BotID:      1,
		Scope:      "user",
		Raw:        map[string]string{domain.TargetAll: text},
		Normalized: map[string]string{domain.TargetAll: normalized},
	}, true)
	if err != nil {
		t.Fatalf("规则匹配失败: %v", err)
	}
	return hits
}

func hitRuleNames(hits []rules.Hit) []string {
	names := make([]string, 0, len(hits))
	for _, h := range hits {
		names = append(names, h.Rule.Name)
	}
	return names
}

func hasAction(hits []rules.Hit, action string) bool {
	for _, h := range hits {
		if h.Rule.Action == action {
			return true
		}
	}
	return false
}

func TestCrimewareAdIsCaught(t *testing.T) {
	st := seededStore(t)

	hits := evaluate(t, st, crimewareAd)
	if len(hits) == 0 {
		t.Fatal("这条广告依然一条规则都没命中")
	}

	// 必须有一条会真正拦下它的规则（delete 或 escalate），
	// 只有 notify 等于「告警了但消息照发」。
	if !hasAction(hits, domain.ActionDelete) && !hasAction(hits, domain.ActionEscalate) {
		t.Errorf("命中的规则里没有一条会拦截消息，只有：%v", hitRuleNames(hits))
	}

	t.Logf("命中 %d 条：%v", len(hits), hitRuleNames(hits))
}

// 共现规则要能凑够阈值触发。
func TestCooccurrenceFiresOnCrimewareAd(t *testing.T) {
	st := seededStore(t)

	for _, h := range evaluate(t, st, crimewareAd) {
		if h.Rule.MatchMode == domain.MatchCooccurrence {
			t.Logf("共现命中：%s", h.MatchedText)
			return
		}
	}
	t.Error("广告命中了多条规则，共现规则却没有触发")
}

// `\b` 修正的对照。
//
// 币种的常见写法是数字紧贴 ticker，而 Go 的 `\b` 是 ASCII 词边界
// （\w = [0-9A-Za-z_]）：`出1000usdt` 里 `0` 与 `u` 都是词字符，中间没有
// 边界，原来的 `\b(?:usdt|...)\b` 会放行 —— 越是正常写法越漏。
func TestCryptoTickerBoundary(t *testing.T) {
	st := seededStore(t)
	const ticker = "加密货币 / 博彩引流"

	hitTicker := func(text string) bool {
		for _, h := range evaluate(t, st, text) {
			if h.Rule.Name == ticker {
				return true
			}
		}
		return false
	}

	// 必须命中：数字 / 空格 / 汉字在前都能起头
	for _, text := range []string{"出1000usdt", "转账 500USDT 就行", "100usdt起", "手续费多少eth"} {
		if !hitTicker(text) {
			t.Errorf("%q 应当命中「%s」", text, ticker)
		}
	}

	// 必须挡住：拉丁字母里嵌着的 eth
	for _, text := range []string{"whether this works", "the method is fine", "ethics matter", "together"} {
		if hitTicker(text) {
			t.Errorf("%q 里的 eth 是正常英文的一部分，不该命中「%s」", text, ticker)
		}
	}
}

// 前导边界用 (?:^|[^A-Za-z]) 而不是 \b，代价是匹配区间会吃掉前一个字符。
// 这里把这个已知的偏移钉下来：它只影响 matchedText 的显示，
// 而审计面板展示的是前后带 24 字上下文的摘录，看不出来。
func TestCryptoTickerMatchIncludesLeadingChar(t *testing.T) {
	st := seededStore(t)

	for _, h := range evaluate(t, st, "出1000usdt") {
		if h.Rule.Name == "加密货币 / 博彩引流" {
			if h.MatchedText != "0usdt" {
				t.Errorf("matchedText = %q，期望 %q（前导字符被一并吃进匹配区间）",
					h.MatchedText, "0usdt")
			}
			return
		}
	}
	t.Fatal("没有命中加密货币规则")
}

// 反向对照：正常咨询不能被误伤。
//
// 这是比「广告被拦住」更重要的一半 —— 一个把正常用户消息删掉的过滤器，
// 比不工作更糟。
func TestNormalMessagesAreNotFlagged(t *testing.T) {
	st := seededStore(t)

	cases := []string{
		"你好，我想咨询一下订单 12345 的物流到哪了",
		"这个月的账单什么时候出？",
		"请问可以开发票吗，抬头怎么写",
		"我的钱包授权怎么取消？我好像被骗了，现在很慌，能帮我看看吗",
		"手机中木马了怎么办，会不会被远程控制",
		"软件下载之后提示需要授权相册权限，正常吗",
		// 下面几条是规则精度上的已知雷区，逐个钉住
		"我们提供从设计到施工的一条龙服务",
		"手把手教你做菜，包教包会",
		"最新的反洗钱政策对跨境汇款有什么影响",
		"这台手机跑分平台排名第几？",
		"请勿盗用他人作品，也提醒大家防盗号",
	}

	for _, text := range cases {
		hits := evaluate(t, st, text)
		for _, h := range hits {
			if h.Rule.Action == domain.ActionDelete || h.Rule.Action == domain.ActionEscalate {
				t.Errorf("正常消息被处罚规则命中：%q\n  → 规则「%s」（%s）",
					text, h.Rule.Name, h.Rule.Action)
			}
		}
		if len(hits) > 0 {
			t.Logf("%q → 仅告警：%v", text, hitRuleNames(hits))
		}
	}
}
