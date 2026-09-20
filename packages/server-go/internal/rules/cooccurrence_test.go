package rules

import (
	"strings"
	"testing"

	"github.com/tgs/server/internal/domain"
)

// 共现模式的验证。
//
// 这个模式与另外三种的本质差别：它不看文本，看的是**同一条消息上的命中集合**。
// 因此它的 pattern 位置放的是一个整数阈值，而不是正则 —— 这是最容易写错的
// 地方，也是最容易造成灾难性后果的地方：阈值若被解析成 0，规则会无条件命中，
// 一条手滑的配置就变成了「删除所有人的所有消息」。

func TestCooccurrenceThresholdParsing(t *testing.T) {
	accepted := []string{"1", "3", "5", " 4 "}
	for _, p := range accepted {
		if err := CheckPattern(p, "", domain.MatchCooccurrence); err != nil {
			t.Errorf("阈值 %q 应当被接受，却报了错：%v", p, err)
		}
	}

	rejected := []string{
		"",      // 空
		"abc",   // 不是数字
		"0",     // 0 会让规则无条件命中
		"-1",    // 负数
		`\d{3}`, // 管理员容易犯的错：填成了匹配数字的正则
		"3.5",   // 不是整数
		"3 4",   // 多个数字
	}
	for _, p := range rejected {
		if err := CheckPattern(p, "", domain.MatchCooccurrence); err == nil {
			t.Errorf("阈值 %q 应当被拒绝，却通过了", p)
		}
	}
}

// 阈值 0 必须编译失败而不是回落到某个默认值。
func TestCooccurrenceZeroThresholdFailsToCompile(t *testing.T) {
	if _, err := compile(ruleRow{Pattern: "0", MatchMode: domain.MatchCooccurrence}); err == nil {
		t.Fatal("阈值 0 会让这条规则命中一切，必须编译失败")
	}

	cr, err := compile(ruleRow{Pattern: "3", MatchMode: domain.MatchCooccurrence})
	if err != nil {
		t.Fatalf("阈值 3 应当编译通过：%v", err)
	}
	if cr.threshold != 3 {
		t.Errorf("threshold = %d，期望 3", cr.threshold)
	}
	// 共现模式不产生正则，也不走字面量匹配
	if cr.re != nil || cr.literal != "" {
		t.Error("共现模式不该编译出正则或字面量")
	}
}

// 沙盒必须明确报错。静默返回「未命中」会让管理员以为阈值配错了，
// 而实际上是这个模式根本无法对单条文本求值。
func TestCooccurrenceCannotRunInSandbox(t *testing.T) {
	if _, err := Run("3", "", domain.MatchCooccurrence, "任意文本"); err == nil {
		t.Error("共现模式在沙盒里应当报错，而不是安静地返回未命中")
	}
}

func cooccurrenceRule(t *testing.T, id int64, threshold string) compiledRule {
	t.Helper()

	cr, err := compile(ruleRow{
		ID: id, Name: "多信号共现", Pattern: threshold,
		MatchMode: domain.MatchCooccurrence, Target: domain.TargetAll,
		Action: domain.ActionEscalate, Severity: 40,
	})
	if err != nil {
		t.Fatalf("编译共现规则失败：%v", err)
	}
	return cr
}

func hitOf(id int64, name string) Hit {
	return Hit{Rule: domain.AdRule{ID: id, Name: name}, Severity: 10}
}

func TestEvalCooccurrenceCountsDistinctRules(t *testing.T) {
	cr := cooccurrenceRule(t, 99, "3")

	t.Run("不够阈值不触发", func(t *testing.T) {
		got := evalCooccurrence([]compiledRule{cr}, []Hit{
			hitOf(1, "盗币黑话"), hitOf(2, "群发软件"),
		})
		if len(got) != 0 {
			t.Errorf("只命中 2 条不同规则，阈值是 3，不该触发；实际触发 %d 条", len(got))
		}
	})

	t.Run("同一条规则重复命中只算一个信号", func(t *testing.T) {
		// 一条规则在一句话里出现三遍仍然是一个信号。算成三个会让阈值形同虚设。
		got := evalCooccurrence([]compiledRule{cr}, []Hit{
			hitOf(1, "盗币黑话"), hitOf(1, "盗币黑话"), hitOf(1, "盗币黑话"),
			hitOf(2, "群发软件"),
		})
		if len(got) != 0 {
			t.Error("同一条规则的重复命中被算成了多个信号")
		}
	})

	t.Run("达到阈值触发并带出规则清单", func(t *testing.T) {
		got := evalCooccurrence([]compiledRule{cr}, []Hit{
			hitOf(1, "盗币黑话"), hitOf(2, "群发软件"), hitOf(3, "远控木马"),
		})
		if len(got) != 1 {
			t.Fatalf("应当触发 1 条共现命中，实际 %d 条", len(got))
		}
		if got[0].Severity != 40 {
			t.Errorf("严重度 = %d，期望取规则自身的 40", got[0].Severity)
		}
		// 共现命中的「证据」是规则清单，审计面板要能一眼看出是哪几个信号叠的
		for _, want := range []string{"盗币黑话", "群发软件", "远控木马", "3 条"} {
			if !strings.Contains(got[0].MatchedText, want) {
				t.Errorf("命中说明里缺少 %q：%s", want, got[0].MatchedText)
			}
		}
	})

	t.Run("没有落库的规则不参与计数", func(t *testing.T) {
		got := evalCooccurrence([]compiledRule{cr}, []Hit{
			hitOf(0, "甲"), hitOf(0, "乙"), hitOf(0, "丙"),
		})
		if len(got) != 0 {
			t.Error("ID 为 0 的命中没有对应的规则行，不该被算进共现")
		}
	})

	t.Run("没有共现规则时零开销", func(t *testing.T) {
		if got := evalCooccurrence(nil, []Hit{hitOf(1, "甲")}); got != nil {
			t.Error("没有配置共现规则时不该产生任何命中")
		}
	})
}

func TestEvalCooccurrenceHonoursHigherThreshold(t *testing.T) {
	strict := cooccurrenceRule(t, 98, "5")
	hits := []Hit{hitOf(1, "甲"), hitOf(2, "乙"), hitOf(3, "丙")}

	if got := evalCooccurrence([]compiledRule{strict}, hits); len(got) != 0 {
		t.Error("阈值 5、只命中 3 条，不该触发")
	}
}
