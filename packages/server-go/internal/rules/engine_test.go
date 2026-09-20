package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/tgs/server/internal/domain"
)

// 这是整个 Go 重写最核心的一条论据，值得单独验证。
//
// `(a+)+$` 是教科书级的灾难性回溯模式：在 backtracking 引擎（PCRE、
// JavaScript 的 RegExp）上，输入每多一个 'a'，匹配耗时翻倍 ——
// 30 个 a 就能让进程假死。原 Node 实现为此堆了一整套防护：
// safe-regex 静态分析 + node:vm 硬超时 + 超时自动停用规则。
//
// Go 的 regexp 是 RE2，用的是 Thompson NFA 模拟，匹配复杂度与输入长度
// 成**线性**关系 —— 灾难性回溯在结构上不可能发生。
// 于是那一整套防护可以整块删掉，代价是失去前后瞻与反向引用。
func TestNoCatastrophicBacktracking(t *testing.T) {
	// 故意用最坏情况的模式与输入
	pattern := `(a+)+$`
	// 40 个 a，末尾一个 b 让它必然失败 —— 回溯引擎会在这里指数爆炸
	input := strings.Repeat("a", 40) + "b"

	start := time.Now()
	result, err := Run(pattern, "", domain.MatchRegex, input)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Run 返回错误：%v", err)
	}
	if result.Matched {
		t.Error("不该命中（末尾是 b，模式要求全 a 到结尾）")
	}

	// 给一个非常宽松的上限。在回溯引擎上这个输入需要数年，
	// 在 RE2 上是微秒级 —— 只要没超时就足以证明性质。
	if elapsed > 100*time.Millisecond {
		t.Errorf("匹配耗时 %v，RE2 应当是微秒级；说明它可能退化成了回溯引擎", elapsed)
	}
	t.Logf("最坏情况模式 %q 在 %d 字符输入上耗时 %v", pattern, len(input), elapsed)
}

// 长输入下的线性度：输入翻倍，耗时不应出现数量级跃升。
func TestLinearScaling(t *testing.T) {
	pattern := `(a|aa)+$`

	measure := func(n int) time.Duration {
		input := strings.Repeat("a", n) + "b"
		start := time.Now()
		_, _ = Run(pattern, "", domain.MatchRegex, input)
		return time.Since(start)
	}

	small := measure(200)
	large := measure(400)

	// 线性算法下 400 字符的耗时最多是 200 字符的若干倍（含常数开销）。
	// 这里用一个宽松阈值：只要没出现指数级的 100 倍以上跃升即可。
	if small > 0 && large > small*50 {
		t.Errorf("输入翻倍后耗时从 %v 涨到 %v，不符合线性预期", small, large)
	}
	t.Logf("200 字符 %v → 400 字符 %v", small, large)
}

func TestWholeWordMatching(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{"英文整词命中", "vx", "请加 vx 详聊", true},
		{"英文整词不命中前缀", "vx", "vxabc", false},
		{"英文整词不命中后缀", "vx", "abcvx", false},
		{"中文可命中", "微信", "加微信详聊", true},
		{"中文夹在汉字间也算命中", "微信", "我的微信是", true},
		{"标点边界", "vx", "vx: 123", true},
		{"下划线算词字符", "vx", "vx_123", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Run(tc.pattern, "", domain.MatchWholeWord, tc.input)
			if err != nil {
				t.Fatalf("Run 出错：%v", err)
			}
			if got.Matched != tc.want {
				t.Errorf("Run(%q, whole_word, %q).Matched = %v，期望 %v",
					tc.pattern, tc.input, got.Matched, tc.want)
			}
		})
	}
}

func TestContainsMatching(t *testing.T) {
	got, err := Run("加微信", "u", domain.MatchContains, "你好加微信详聊")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Matched {
		t.Fatal("应命中")
	}
	if got.Segments[0].Text != "加微信" {
		t.Errorf("命中片段 = %q", got.Segments[0].Text)
	}

	// 大小写不敏感
	got, _ = Run("usdt", "iu", domain.MatchContains, "支持 USDT 充值")
	if !got.Matched {
		t.Error("带 i 标志时应忽略大小写")
	}
	got, _ = Run("usdt", "u", domain.MatchContains, "支持 USDT 充值")
	if got.Matched {
		t.Error("不带 i 标志时不应命中大写")
	}
}

// 空模式不能造成死循环 —— 这是字面量匹配里最容易被忽略的一个坑。
func TestContainsEmptyPatternDoesNotHang(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err := Run("", "u", domain.MatchContains, "任意文本")
		if err != nil {
			t.Errorf("不该报错：%v", err)
		}
		if result.Matched {
			t.Error("空模式不该命中")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("空模式导致死循环")
	}
}

func TestCaptureGroups(t *testing.T) {
	got, err := Run(`(\d+)\s*元`, "u", domain.MatchRegex, "日入 500 元")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Matched {
		t.Fatal("应命中")
	}
	if len(got.Segments[0].Groups) != 1 || got.Segments[0].Groups[0] != "500" {
		t.Errorf("捕获组 = %v，期望 [500]", got.Segments[0].Groups)
	}
}

func TestInlineFlagsTranslation(t *testing.T) {
	// 面板用的是 JS 风格标志（iu / gi），Go 需要用内联 (?i) 表达
	if got := inlineFlags("iu"); got != "(?i)" {
		t.Errorf("inlineFlags(iu) = %q，期望 (?i)", got)
	}
	if got := inlineFlags("gimsuy"); got != "(?ims)" {
		t.Errorf("inlineFlags(gimsuy) = %q，期望 (?ims)", got)
	}
	if got := inlineFlags("guy"); got != "" {
		t.Errorf("无对应标志时应返回空串，得到 %q", got)
	}
}

func TestCheckPatternRejectsBadInput(t *testing.T) {
	cases := []struct {
		name      string
		pattern   string
		flags     string
		matchMode string
		wantErr   bool
	}{
		{"正常", `加微信`, "iu", domain.MatchRegex, false},
		{"未闭合括号", `(abc`, "iu", domain.MatchRegex, true},
		{"不支持的前后瞻", `(?<=a)b`, "iu", domain.MatchRegex, true},
		{"非法标志", `abc`, "xz", domain.MatchRegex, true},
		{"空模式", ``, "iu", domain.MatchRegex, true},
		{"字面量模式不校验正则", `(abc`, "iu", domain.MatchContains, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckPattern(tc.pattern, tc.flags, tc.matchMode)
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckPattern(%q) 错误 = %v，期望出错 = %v", tc.pattern, err, tc.wantErr)
			}
		})
	}
}

func TestUnionActionsAndSeverity(t *testing.T) {
	hits := []Hit{
		{Rule: domain.AdRule{Action: domain.ActionDelete}, Severity: 10},
		{Rule: domain.AdRule{Action: domain.ActionDelete}, Severity: 30},
		{Rule: domain.AdRule{Action: domain.ActionNotify}, Severity: 5},
	}

	actions := UnionActions(hits)
	if len(actions) != 2 || !actions[domain.ActionDelete] || !actions[domain.ActionNotify] {
		t.Errorf("动作并集 = %v", actions)
	}

	// 取最高而不是求和：三条轻规则不该叠成一次重罚
	if got := TotalSeverity(hits); got != 30 {
		t.Errorf("违规分 = %d，期望 30（取最高）", got)
	}
}
