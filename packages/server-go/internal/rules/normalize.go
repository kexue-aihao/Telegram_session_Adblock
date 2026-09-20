// Package rules 是广告规则引擎。
//
// 与 Node 版本最重要的差别：**Go 的 regexp 是 RE2，线性时间、无回溯**。
// 这意味着灾难性回溯（ReDoS）在结构上不可能发生，因此原实现里那一整套
// 「safe-regex 静态校验 + node:vm 硬超时 + 超时自动熔断」的防护可以整块删掉。
//
// 代价是 RE2 不支持前后瞻（lookaround）与反向引用。原实现里
// `whole_word` 模式用 `(?<![\w])...` 表达词边界，这里改为手工判断边界 ——
// 功能等价，且顺带修掉了「中文没有 \b」这个原本需要特殊处理的点。
package rules

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/tgs/server/internal/tgapi"
)

// Normalize 是匹配前的文本归一化。
//
// 广告投放者绕过关键词的最常用手法：
//   - 零宽字符插入：`加<ZWSP>微信`
//   - 全角混排：`加Ｖ信`
//   - 用双向控制符把关键词切碎：`加<RLO>微<RLO>信`
//
// 归一化只用于**匹配与解释**，不改变落库的原文 —— 审计记录里的
// matchedText 必须能说明「凭什么是它」，所以两边都留。
func Normalize(input string) string {
	if input == "" {
		return ""
	}

	// NFKC 处理全角与兼容字符（Ｖ → V，ﬁ → fi）
	s := norm.NFKC.String(input)

	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// 1. 删除零宽 / 方向控制 / 变体选择符：肉眼不可见但能切开关键词
		if isInvisible(r) {
			continue
		}

		// 2. 同形异义字符折叠：西里尔 а 与拉丁 a 字形几乎一致
		if ascii, ok := homoglyphs[r]; ok {
			b.WriteRune(ascii)
			continue
		}

		// 3. 中文之间的分隔符噪声：加-微-信 / 加 微 信 应塌缩成同一形态。
		//    只吃空格与标点、不吃换行 —— 吃掉换行会把前后两行粘成一个词，
		//    制造出原文里根本不存在的命中。
		if isSeparator(r) && isCJKContext(runes, i) {
			continue
		}

		b.WriteRune(r)
	}

	// 4. 折叠连续空白为单个空格并去掉首尾，让 `\s+` 之类的模式行为可预测
	return strings.Join(strings.Fields(b.String()), " ")
}

// isInvisible 报告该码位是否属于「肉眼不可见但能切断关键词」的类别。
//
// 用码位判断而不是正则字符类：这些字符本身就不可见，粘进源码后
// 没人能靠读代码确认范围对不对，改错一位会静默失效。
func isInvisible(r rune) bool {
	switch {
	case r == 0x00AD: // SOFT HYPHEN
		return true
	case r == 0x061C: // ARABIC LETTER MARK
		return true
	case r == 0x180E: // MONGOLIAN VOWEL SEPARATOR
		return true
	case r >= 0x200B && r <= 0x200F: // ZWSP / ZWNJ / ZWJ / LRM / RLM
		return true
	case r >= 0x202A && r <= 0x202E: // 双向嵌入与覆盖
		return true
	case r >= 0x2060 && r <= 0x2064: // WORD JOINER 与不可见运算符
		return true
	case r >= 0x2066 && r <= 0x206F: // 双向隔离符与已废弃的格式化符
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // 变体选择符
		return true
	case r == 0xFEFF: // BOM / ZWNBSP
		return true
	}
	return false
}

// homoglyphs 收录广告里真实出现频率高的同形字。
//
// 不做全表映射 —— 过度归一化会把正常外文内容也搅乱，反而增加误报。
var homoglyphs = map[rune]rune{
	// 西里尔字母冒充拉丁字母（vx、btc 这类短词最爱用）
	0x0430: 'a', 0x0432: 'b', 0x0435: 'e', 0x043A: 'k', 0x043C: 'm',
	0x043D: 'h', 0x043E: 'o', 0x0440: 'p', 0x0441: 'c', 0x0442: 't',
	0x0443: 'y', 0x0445: 'x', 0x0455: 's', 0x0456: 'i', 0x0458: 'j',
	// 希腊字母
	0x03BF: 'o', 0x03C1: 'p', 0x03BD: 'v', 0x03C4: 't',
}

func isSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '.', '-', '*', '_', '~', '^', 0x00B7: // 中点
		return true
	}
	return false
}

// isCJKContext 判断位置 i 的分隔符是否夹在「中文与任意非空白」之间。
//
// 判据：左边是中文，右边是任何非空白字符（或反之）。
// 这样 `加-微信`、`加 - 微 信` 会塌缩，而 `hello - world` 不会被动。
func isCJKContext(runes []rune, i int) bool {
	if i == 0 || i == len(runes)-1 {
		return false
	}
	left := runes[i-1]
	// 向右跳过连续的分隔符，找到第一个非分隔符
	right := rune(0)
	for j := i + 1; j < len(runes); j++ {
		if !isSeparator(runes[j]) {
			right = runes[j]
			break
		}
	}
	if right == 0 || unicode.IsSpace(left) || unicode.IsSpace(right) {
		return false
	}
	return isCJK(left) || isCJK(right)
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

// ────────────────────────────── 实体抽取 ──────────────────────────────

// ExtractedLink 是 entity 里的一段链接。
type ExtractedLink struct {
	URL  string
	Kind string // text_link | url
	Text string // entity 覆盖的显示文本
}

// ExtractLinks 取出实体里藏着的真实 URL。
//
// 广告最爱的藏链接手法：显示文本是「点击查看」，text_link entity 里
// 藏一个 t.me 邀请链接。只看 message.Text 完全看不出来，必须单独取。
//
// 注意 offset/length 以 **UTF-16 码元** 计，而 Go 的字符串是 UTF-8 字节。
// 直接按字节切片会把 emoji 与部分中文切坏 —— 必须先转换。
func ExtractLinks(text string, entities []tgapi.MessageEntity) []ExtractedLink {
	if text == "" || len(entities) == 0 {
		return nil
	}

	runes := []rune(text)
	links := make([]ExtractedLink, 0, len(entities))

	for _, e := range entities {
		covered := utf16Slice(runes, e.Offset, e.Length)
		switch e.Type {
		case "text_link":
			if e.URL != "" {
				links = append(links, ExtractedLink{URL: e.URL, Kind: "text_link", Text: covered})
			}
		case "url":
			links = append(links, ExtractedLink{URL: covered, Kind: "url", Text: covered})
		}
	}
	return links
}

// EntityTexts 返回指定类型的 entity 覆盖的文本，用于 target 匹配。
func EntityTexts(text string, entities []tgapi.MessageEntity, types ...string) string {
	if text == "" || len(entities) == 0 {
		return ""
	}

	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}

	runes := []rune(text)
	var parts []string

	for _, e := range entities {
		if !want[e.Type] {
			continue
		}
		covered := utf16Slice(runes, e.Offset, e.Length)
		// 隐藏链接要同时算上「显示文本」与「真实 URL」两个面
		if e.Type == "text_link" && e.URL != "" {
			parts = append(parts, e.URL, covered)
			continue
		}
		parts = append(parts, covered)
	}

	return strings.Join(parts, "\n")
}

// utf16Slice 按 UTF-16 码元下标切出子串。
//
// Telegram 的 entity offset/length 以 UTF-16 码元计（与 JavaScript 的
// 字符串下标一致），而 Go 用 UTF-8。BMP 之外的字符（emoji、部分罕用汉字）
// 在 UTF-16 里占两个码元，直接按 rune 下标切会错位。
func utf16Slice(runes []rune, offset, length int) string {
	if offset < 0 || length <= 0 {
		return ""
	}

	units := 0
	start := -1
	end := -1

	for i, r := range runes {
		if units == offset && start < 0 {
			start = i
		}
		units += utf16Len(r)
		if units == offset+length && end < 0 {
			end = i + 1
			break
		}
	}
	if start < 0 {
		return ""
	}
	if end < 0 {
		end = len(runes)
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

func utf16Len(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// ────────────────────────────── Markdown 转义 ──────────────────────────────

// EscapeMarkdown 转义 Telegram Markdown(V1) 的特殊字符。
//
// 文案模板里含 `**加粗**` 与反引号代码，所以必须以 Markdown 解析发送；
// 而 `{name}` 之类的替换值完全来自用户（昵称里塞 `**` 或 `[` 都很常见），
// 不转义会让消息直接发送失败（400 can't parse entities），
// 或者更糟 —— 把用户内容渲染成我们没打算给的格式。
//
// 右方括号也一起转义：Markdown(V1) 里链接的语法是 `[文本](url)`，
// 单看 `]` 确实不是特殊字符，但模板本身完全可能含一个未转义的 `[`
// （管理员写 `[{name}]` 是很自然的），那时用户提供的 `]` 就会闭合它。
// 多转义一个字符零成本，少转义一个则是一个可以被利用的注入面。
func EscapeMarkdown(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '_', '*', '`', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SingleLine 把可能含换行的值塞进模板的一行里。
func SingleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
