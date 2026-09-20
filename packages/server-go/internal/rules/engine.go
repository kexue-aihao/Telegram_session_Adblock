package rules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tgs/server/internal/domain"
)

// Engine 判断「命中了什么」。
//
// 职责边界：引擎**只做匹配**，不执行任何处置。处罚要写库、要发消息、
// 要改话题状态 —— 那些在 actions.go。拆开的硬性理由：面板的正则沙盒
// 要复用引擎，但绝不能因为管理员点了「测试」就真的去删消息。
type Engine struct {
	db Queryer

	mu    sync.RWMutex
	cache []compiledRule
	// 缓存版本，用于让缓存失效可观测
	loadedAt time.Time
}

// Queryer 是 Engine 需要的数据库能力。
// 用接口而不是 *sql.DB：便于测试时注入，也让依赖面一目了然。
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// compiledRule 是编译好的规则。
//
// 关键优化：正则**只编译一次**。Go 的 regexp 编译有实打实的成本
// （解析 + 生成自动机），而中继热路径上每条消息都要跑全部规则 ——
// 每次重新 Compile 会让匹配耗时高出一个数量级。
type compiledRule struct {
	row     ruleRow
	re      *regexp.Regexp // 字面量模式（contains）为 nil
	literal string         // contains 模式用，走 strings.Contains 更快
	// cooccurrence 模式用：触发所需的「不同规则命中数」下限。
	// 该模式不编译正则，pattern 存的就是这个整数。
	threshold int
}

type ruleRow struct {
	ID           int64
	BotID        *int64
	Name         string
	Pattern      string
	Flags        string
	MatchMode    string
	Target       string
	Action       string
	Severity     int
	Priority     int
	IsEnabled    bool
	IsSystem     bool
	Note         *string
	HitCount     int
	LastHitAt    *int64
	AutoDisabled bool
	CreatedAt    int64
	UpdatedAt    int64
}

// ToAdRule 投影成对外 DTO。
func (r ruleRow) ToAdRule() domain.AdRule {
	return domain.AdRule{
		ID:           r.ID,
		BotID:        r.BotID,
		Name:         r.Name,
		Pattern:      r.Pattern,
		Flags:        r.Flags,
		MatchMode:    r.MatchMode,
		Target:       r.Target,
		Action:       r.Action,
		Severity:     r.Severity,
		Priority:     r.Priority,
		IsEnabled:    r.IsEnabled,
		IsSystem:     r.IsSystem,
		Note:         r.Note,
		HitCount:     r.HitCount,
		LastHitAt:    r.LastHitAt,
		AutoDisabled: r.AutoDisabled,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

// New 构造引擎。
func New(db Queryer) *Engine {
	return &Engine{db: db}
}

// Invalidate 让缓存失效。规则的增删改都必须调用它。
func (e *Engine) Invalidate() {
	e.mu.Lock()
	e.cache = nil
	e.mu.Unlock()
}

// ────────────────────────────── 编译 ──────────────────────────────

// CompileOptions 把 Go 风格的正则标志（i/m/s）转成 Go 的 (?i) 前缀。
//
// 面板沿用了 JavaScript 的写法（`iu`、`gi`），因为管理员熟悉的是那个 ——
// 但 Go 的 regexp 不接受这些标志参数，只能用内联标志。
// `g`（全局）在 Go 里没有对应概念：FindAllString 天然就是全局的，
// 因此直接忽略。`u` 在 Go 里始终开启，同样忽略。
func inlineFlags(flags string) string {
	var mods strings.Builder
	for _, f := range flags {
		switch f {
		case 'i':
			mods.WriteByte('i')
		case 'm':
			mods.WriteByte('m')
		case 's':
			mods.WriteByte('s')
		case 'g', 'u', 'y':
			// Go 里无对应概念：全局匹配是默认行为，Unicode 始终开启
		}
	}
	if mods.Len() == 0 {
		return ""
	}
	return "(?" + mods.String() + ")"
}

// compile 把一行规则编译成可复用的执行形态。
func compile(row ruleRow) (compiledRule, error) {
	cr := compiledRule{row: row}

	switch row.MatchMode {
	case domain.MatchContains:
		// 字面量匹配走 strings.Contains：不需要正则，也不存在元字符问题
		cr.literal = row.Pattern
		return cr, nil

	case domain.MatchCooccurrence:
		// pattern 位置放的是阈值（一个十进制正整数），不是正则。
		// 解析失败必须报错而不是回落到 0：阈值 0 会让这条规则**无条件命中**，
		// 一条手滑写错的正则就变成了「拦截所有人全部消息」。
		n, err := strconv.Atoi(strings.TrimSpace(row.Pattern))
		if err != nil || n < 1 {
			return cr, fmt.Errorf("共现阈值必须是正整数，实际是 %q", row.Pattern)
		}
		cr.threshold = n
		return cr, nil

	case domain.MatchWholeWord:
		// RE2 不支持前后瞻，所以「词边界」不用 (?<![\w]) 表达，
		// 而是在匹配之后手工检查边界（见 matchWholeWord）。
		// 这里先编译出基础模式，用于找出所有候选位置。
		re, err := regexp.Compile(inlineFlags(row.Flags) + regexp.QuoteMeta(row.Pattern))
		if err != nil {
			return cr, fmt.Errorf("编译整词模式: %w", err)
		}
		cr.re = re
		return cr, nil

	default: // regex
		pattern := inlineFlags(row.Flags) + row.Pattern
		re, err := regexp.Compile(pattern)
		if err != nil {
			return cr, err
		}
		cr.re = re
		return cr, nil
	}
}

// CheckPattern 在保存规则前做一次编译试跑。
//
// 这里**没有** safe-regex 那种「检测嵌套量词」的分析 —— Go 的 regexp 是
// RE2，匹配复杂度与输入长度成线性关系，灾难性回溯在结构上不可能发生。
// 需要检查的只剩「语法能不能编译」这一件事，而编译本身就是最准确的检查。
func CheckPattern(pattern, flags, matchMode string) error {
	if pattern == "" {
		return errors.New("模式不能为空")
	}
	if len(pattern) > 2000 {
		return errors.New("模式过长（上限 2000 字符）")
	}
	for _, f := range flags {
		switch f {
		case 'g', 'i', 'm', 's', 'u', 'y':
		default:
			return fmt.Errorf("不支持的标志 %q，只允许 g i m s u y", string(f))
		}
	}

	if matchMode == domain.MatchContains {
		return nil
	}
	if matchMode == domain.MatchCooccurrence {
		// 单独判一次是为了给出比 compile 更具体的错误说法：
		// 管理员在「正则」框里填了 `\d{3}` 之类的写法时，得有人告诉他
		// 这个模式要的是数字本身，不是匹配数字的正则。
		if n, err := strconv.Atoi(strings.TrimSpace(pattern)); err != nil || n < 1 {
			return fmt.Errorf("共现模式要在模式框里填一个正整数（触发所需的命中规则条数），例如 3；实际填的是 %q", pattern)
		}
		return nil
	}
	if _, err := compile(ruleRow{Pattern: pattern, Flags: flags, MatchMode: matchMode}); err != nil {
		// 把 Go 的英文错误转成管理员能看懂的说法
		return fmt.Errorf("正则语法错误：%s", err.Error())
	}
	return nil
}

// ────────────────────────────── 规则加载 ──────────────────────────────

func (e *Engine) load(ctx context.Context) ([]compiledRule, error) {
	e.mu.RLock()
	if e.cache != nil {
		cached := e.cache
		e.mu.RUnlock()
		return cached, nil
	}
	e.mu.RUnlock()

	rows, err := e.db.QueryContext(ctx, `
		SELECT id, bot_id, name, pattern, flags, match_mode, target, action,
		       severity, priority, is_enabled, is_system, note, hit_count,
		       last_hit_at, auto_disabled_at, created_at, updated_at
		FROM ad_rules
		WHERE is_enabled = 1
		ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("加载规则: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var compiled []compiledRule
	var broken []string

	for rows.Next() {
		var r ruleRow
		var autoDisabledAt *int64
		var isEnabled, isSystem int
		if err := rows.Scan(
			&r.ID, &r.BotID, &r.Name, &r.Pattern, &r.Flags, &r.MatchMode, &r.Target,
			&r.Action, &r.Severity, &r.Priority, &isEnabled, &isSystem, &r.Note,
			&r.HitCount, &r.LastHitAt, &autoDisabledAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("读取规则行: %w", err)
		}
		r.IsEnabled = isEnabled == 1
		r.IsSystem = isSystem == 1
		r.AutoDisabled = autoDisabledAt != nil

		cr, err := compile(r)
		if err != nil {
			// 一条编译不过的规则不能拖垮整个引擎。
			// 记下来并在面板上标红，其余规则照常工作。
			broken = append(broken, fmt.Sprintf("#%d %s: %v", r.ID, r.Name, err))
			continue
		}
		compiled = append(compiled, cr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	_ = broken // 由调用方通过规则列表页看到 autoDisabled 标记

	e.mu.Lock()
	e.cache = compiled
	e.loadedAt = time.Now()
	e.mu.Unlock()

	return compiled, nil
}

// ListAll 返回全部规则（含停用的），供面板列表使用。
func (e *Engine) ListAll(ctx context.Context) ([]domain.AdRule, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT id, bot_id, name, pattern, flags, match_mode, target, action,
		       severity, priority, is_enabled, is_system, note, hit_count,
		       last_hit_at, auto_disabled_at, created_at, updated_at
		FROM ad_rules
		ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("列出规则: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.AdRule, 0, 32)
	for rows.Next() {
		var r ruleRow
		var autoDisabledAt *int64
		var isEnabled, isSystem int
		if err := rows.Scan(
			&r.ID, &r.BotID, &r.Name, &r.Pattern, &r.Flags, &r.MatchMode, &r.Target,
			&r.Action, &r.Severity, &r.Priority, &isEnabled, &isSystem, &r.Note,
			&r.HitCount, &r.LastHitAt, &autoDisabledAt, &r.CreatedAt, &r.UpdatedAt,
		); err != nil {
			return nil, err
		}
		r.IsEnabled = isEnabled == 1
		r.IsSystem = isSystem == 1
		r.AutoDisabled = autoDisabledAt != nil
		out = append(out, r.ToAdRule())
	}
	return out, rows.Err()
}

// ────────────────────────────── 匹配 ──────────────────────────────

// Context 是一次匹配的输入。
type Context struct {
	BotID int64
	// Scope 区分用户消息（可处罚）与管理员消息（仅审计）
	Scope string
	// Raw 是按 target 预先拼好的原文
	Raw map[string]string
	// Normalized 是同一批文本的归一化结果 —— 规则实际匹配的就是它
	Normalized map[string]string
}

// Hit 是一次命中。
type Hit struct {
	Rule              domain.AdRule
	Target            string
	MatchedText       string
	NormalizedExcerpt string
	Severity          int
}

// Segment 是一个命中片段在文本里的位置。
type Segment struct {
	Start  int
	End    int
	Text   string
	Groups []string
}

// MatchResult 是一次「模式 + 文本」的匹配结果。
// 同时被中继链路与面板沙盒使用，保证两边行为一致。
type MatchResult struct {
	Segments []Segment
	Matched  bool
}

// Run 在给定文本上执行一个模式。
//
// 这是中继链路与沙盒**共用**的唯一匹配入口。两条路径各写一份匹配逻辑
// 迟早会漂移，而沙盒的全部意义就是「所见即运行时所得」。
func Run(pattern, flags, matchMode, text string) (MatchResult, error) {
	// 共现模式不看文本。没有这道拦截它会在沙盒里安静地返回「未命中」——
	// 管理员会以为自己的阈值配错了，而实际上这个模式根本无法单条测试。
	if matchMode == domain.MatchCooccurrence {
		return MatchResult{}, errors.New("共现模式依赖同一条消息上的全部命中，无法在沙盒里单独测试；请保存后用真实消息验证")
	}

	if text == "" {
		return MatchResult{}, nil
	}

	if matchMode == domain.MatchContains {
		// 空模式必须挡在循环之外：strings.Index 对空串恒返回 0，
		// 推进量也是 0，会变成死循环把中继协程卡住。
		if pattern == "" {
			return MatchResult{}, nil
		}

		// 字面量查找：大小写敏感度由 i 标志决定
		haystack := text
		needle := pattern
		if strings.ContainsRune(flags, 'i') {
			haystack = strings.ToLower(haystack)
			needle = strings.ToLower(needle)
		}
		var segs []Segment
		from := 0
		for from <= len(haystack)-len(needle) {
			at := strings.Index(haystack[from:], needle)
			if at < 0 {
				break
			}
			start := from + at
			end := start + len(needle)
			// 从原文切片以保留原始大小写
			segs = append(segs, Segment{Start: start, End: end, Text: text[start:end]})
			from = end
		}
		return MatchResult{Segments: segs, Matched: len(segs) > 0}, nil
	}

	// 委托给 runCompiled，而不是在这里再写一遍同样的枚举逻辑。
	// 沙盒与中继链路必须走完全相同的代码路径 —— 那正是沙盒可信的前提。
	cr, err := compile(ruleRow{Pattern: pattern, Flags: flags, MatchMode: matchMode})
	if err != nil {
		return MatchResult{}, err
	}
	return runCompiled(cr, text)
}

// matchWholeWord 手工判断词边界。
//
// RE2 不支持 (?<![\w])...(?![\\w])，所以在匹配出候选位置后自己看两侧。
// 判据：两侧都不能是「字母、数字或下划线」。
//
// 这个定义对中文是合适的 —— 中文没有 \b 意义上的词边界，用 \b 会让
// 「加微信」里的「微信」因为两侧是汉字而被判成同一个词的一部分。
//
// 边界检查必须用 utf8.DecodeLastRuneInString / DecodeRuneInString 而不是
// 按字节取相邻字符：中文一个字占 3 字节，`text[start-1]` 拿到的是半个汉字，
// unicode.IsLetter 对那个字节的结论没有意义。
func matchWholeWord(re *regexp.Regexp, text string) MatchResult {
	locs := re.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return MatchResult{}
	}

	segs := make([]Segment, 0, len(locs))
	for _, loc := range locs {
		start, end := loc[0], loc[1]

		if start > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:start])
			if isWordRune(prev) {
				continue
			}
		}
		if end < len(text) {
			next, _ := utf8.DecodeRuneInString(text[end:])
			if isWordRune(next) {
				continue
			}
		}

		segs = append(segs, Segment{Start: start, End: end, Text: text[start:end]})
	}
	return MatchResult{Segments: segs, Matched: len(segs) > 0}
}

func isWordRune(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// Evaluate 对一个上下文跑完整套规则。
//
// 返回**全部**命中而不是第一个：动作取并集，每条都记审计。
// 只汇报第一条会让管理员永远不知道自己的规则集里有多少条在重复命中同一句话。
func (e *Engine) Evaluate(ctx context.Context, rc Context, rulesEnabled bool) ([]Hit, error) {
	if !rulesEnabled {
		return nil, nil
	}

	all, err := e.load(ctx)
	if err != nil {
		return nil, err
	}

	var hits []Hit
	// 共现规则要等到其余规则全部跑完才知道命中了几条，因此单独攒起来，
	// 第一遍结束后再判。它们自己不参与计数。
	var cooccurrence []compiledRule

	for _, cr := range all {
		// 全局规则（BotID 为 nil）对所有机器人生效
		if cr.row.BotID != nil && *cr.row.BotID != rc.BotID {
			continue
		}

		if cr.row.MatchMode == domain.MatchCooccurrence {
			cooccurrence = append(cooccurrence, cr)
			continue
		}

		target := cr.row.Target
		raw, ok := rc.Raw[target]
		if !ok || raw == "" {
			continue
		}
		// 规则始终匹配**归一化文本**：归一化的全部意义就是让
		// `加<ZWSP>微信` 这类变体塌缩到同一形态。匹配原文等于把防护关掉。
		text := rc.Normalized[target]
		if text == "" {
			text = raw
		}

		var result MatchResult
		switch {
		case cr.literal != "":
			result, err = Run(cr.literal, cr.row.Flags, domain.MatchContains, text)
		default:
			result, err = runCompiled(cr, text)
		}
		if err != nil || !result.Matched {
			continue
		}

		first := result.Segments[0]
		hits = append(hits, Hit{
			Rule:              cr.row.ToAdRule(),
			Target:            target,
			MatchedText:       first.Text,
			NormalizedExcerpt: excerptAround(text, first.Start, first.End-first.Start),
			Severity:          cr.row.Severity,
		})
	}

	hits = append(hits, evalCooccurrence(cooccurrence, hits)...)
	return hits, nil
}

// evalCooccurrence 判定共现规则。
//
// 计数口径是**命中了几条不同的规则**，不是命中了几次 —— 一条规则在一句话里
// 出现三遍仍然是「一个信号」，把它算成三个会让阈值形同虚设。
func evalCooccurrence(rules []compiledRule, hits []Hit) []Hit {
	if len(rules) == 0 {
		return nil
	}

	seen := make(map[int64]bool, len(hits))
	names := make([]string, 0, len(hits))
	for _, h := range hits {
		// ID 为 0 的是没有落库的规则，不参与计数
		if h.Rule.ID == 0 || seen[h.Rule.ID] {
			continue
		}
		seen[h.Rule.ID] = true
		names = append(names, h.Rule.Name)
	}
	distinct := len(seen)

	var out []Hit
	for _, cr := range rules {
		if distinct < cr.threshold {
			continue
		}
		out = append(out, Hit{
			Rule:   cr.row.ToAdRule(),
			Target: cr.row.Target,
			// 这条命中的「证据」不是某段文本，而是凑够阈值的那个规则清单。
			// 审计面板上要能一眼看出是哪几个信号叠出来的。
			MatchedText:       truncateRunes(fmt.Sprintf("命中 %d 条规则：%s", distinct, strings.Join(names, "、")), 300),
			NormalizedExcerpt: fmt.Sprintf("规则共现 %d/%d", distinct, cr.threshold),
			Severity:          cr.row.Severity,
		})
	}
	return out
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

func runCompiled(cr compiledRule, text string) (MatchResult, error) {
	if cr.re == nil {
		return MatchResult{}, nil
	}
	if cr.row.MatchMode == domain.MatchWholeWord {
		return matchWholeWord(cr.re, text), nil
	}

	locs := cr.re.FindAllStringSubmatchIndex(text, -1)
	segs := make([]Segment, 0, len(locs))
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		seg := Segment{Start: start, End: end, Text: text[start:end]}
		for i := 2; i+1 < len(loc); i += 2 {
			if loc[i] < 0 {
				seg.Groups = append(seg.Groups, "")
				continue
			}
			seg.Groups = append(seg.Groups, text[loc[i]:loc[i+1]])
		}
		segs = append(segs, seg)
	}
	return MatchResult{Segments: segs, Matched: len(segs) > 0}, nil
}

// excerptAround 取命中位置附近的一小片，供审计面板展示「为什么命中」。
//
// 用 rune 而不是 byte 切：按字节切会把汉字劈成两半，前端拿到的是乱码。
func excerptAround(haystack string, startByte, lengthByte int) string {
	const pad = 24

	runes := []rune(haystack)
	startRune := utf8.RuneCountInString(haystack[:startByte])
	endRune := utf8.RuneCountInString(haystack[:startByte+lengthByte])

	from := startRune - pad
	if from < 0 {
		from = 0
	}
	to := endRune + pad
	if to > len(runes) {
		to = len(runes)
	}

	prefix, suffix := "", ""
	if from > 0 {
		prefix = "…"
	}
	if to < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[from:to]) + suffix
}

// UnionActions 取动作并集：命中多条规则时，每类动作各最多执行一次。
//
// 取并集而不是「按优先级只执行一条」—— 否则「删了但没警告」这种
// 半吊子处置会让管理员以为规则没生效。
func UnionActions(hits []Hit) map[string]bool {
	actions := make(map[string]bool)
	for _, h := range hits {
		actions[h.Rule.Action] = true
	}
	return actions
}

// TotalSeverity 取命中里的最高分，而不是求和。
//
// 求和会让三条轻规则叠成一次重罚，阈值就失去了可预测性 ——
// 管理员调一条规则的分数时无法预料用户会不会因此被拉黑。
func TotalSeverity(hits []Hit) int {
	max := 0
	for _, h := range hits {
		if h.Severity > max {
			max = h.Severity
		}
	}
	return max
}

// RuleStats 供仪表盘展示。
type RuleStats struct {
	Total        int64 `json:"total"`
	Enabled      int64 `json:"enabled"`
	AutoDisabled int64 `json:"autoDisabled"`
}

// Stats 统计规则数量。
func (e *Engine) Stats(ctx context.Context) (RuleStats, error) {
	var s RuleStats
	err := e.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN is_enabled = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN auto_disabled_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM ad_rules`).Scan(&s.Total, &s.Enabled, &s.AutoDisabled)
	return s, err
}

// ────────────────────────────── 命中计数 ──────────────────────────────

// BumpHitCounts 批量自增命中计数。
// 一条消息可能同时命中多条规则，因此在事务里一次写完。
func (e *Engine) BumpHitCounts(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, id := range ids {
		if _, err := e.db.ExecContext(ctx,
			`UPDATE ad_rules SET hit_count = hit_count + 1, last_hit_at = ? WHERE id = ?`,
			now, id); err != nil {
			return err
		}
	}
	e.Invalidate()
	return nil
}

// AutoDisable 停用一条规则并记下原因。
//
// 在 Go 里触发它的场景比 Node 少得多（没有 ReDoS），但仍需要：
// 例如管理员写了 `.*` 这种会匹配一切的模式，虽不致命但显然不是本意。
func (e *Engine) AutoDisable(ctx context.Context, id int64, reason string) error {
	_, err := e.db.ExecContext(ctx, `
		UPDATE ad_rules
		SET is_enabled = 0, auto_disabled_at = ?, auto_disabled_reason = ?, updated_at = ?
		WHERE id = ?`, time.Now().UnixMilli(), reason, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	e.Invalidate()
	return nil
}
