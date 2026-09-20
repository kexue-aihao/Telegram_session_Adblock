package rules

import (
	"testing"

	"github.com/tgs/server/internal/tgapi"
)

// 测试数据里的不可见字符**一律用 Go 转义写**（ 这类），不粘字面量。
//
// 理由与 normalize.go 里那条注释相同：这些码位本身就不可见，粘进源码后
// 没人能靠读代码确认它到底是哪一个。刚才就有一个字面量 BOM 让
// Go 编译器直接报 "illegal byte order mark"。
// 用 string(rune(0x...)) 而不是 "" 这类转义写法：
// 一来这些码位不可见、字面量粘进源码无法靠阅读核对，
// 二来转义序列在多层工具链里容易被提前解释掉（已经踩过一次）。
// 写成显式的码位，任何人看一眼就知道是哪一个字符。
const (
	zwsp = string(rune(0x200B)) // ZERO WIDTH SPACE
	zwj  = string(rune(0x200D)) // ZERO WIDTH JOINER
	shy  = string(rune(0x00AD)) // SOFT HYPHEN
	bom  = string(rune(0xFEFF)) // BOM / ZWNBSP
	rlo  = string(rune(0x202E)) // RIGHT-TO-LEFT OVERRIDE
)

func TestNormalizeStripsInvisibleAndSeparators(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"原样", "加微信", "加微信"},
		{"零宽空格", "加" + zwsp + "微信", "加微信"},
		{"零宽连接符", "加" + zwj + "微信", "加微信"},
		{"软连字符", "加" + shy + "微信", "加微信"},
		{"BOM", "加" + bom + "微信", "加微信"},
		{"双向覆盖", rlo + "信微加", "信微加"},
		{"连字符分隔", "加-微-信", "加微信"},
		{"空格分隔", "加 微 信", "加微信"},
		{"中点分隔", "加·微·信", "加微信"},
		{"全角字母", "加Ｖ信", "加V信"},
		{"西里尔 a", "vxа", "vxa"},
		{"希腊 o", "bοt", "bot"},
		{"大小写保留", "USDT", "USDT"},
		{"连续空白折叠", "你好   世界", "你好 世界"},
		{"首尾空白", "  你好  ", "你好"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.input); got != tc.want {
				t.Errorf("Normalize(%q) = %q, 期望 %q", tc.input, got, tc.want)
			}
		})
	}
}

// 换行必须保留（折叠为空格），不能吃掉。
// 吃掉换行会把前后两行粘成一个词，制造出原文里根本不存在的命中。
func TestNormalizeKeepsLineBreaks(t *testing.T) {
	got := Normalize("价格多少\n微信联系")
	if got != "价格多少 微信联系" {
		t.Errorf("换行应折叠为空格而不是被删除，得到 %q", got)
	}
}

// 拉丁文本里的连字符不该被吃掉：`hello - world` 是正常英文，不是绕过手法。
func TestNormalizeLeavesLatinHyphens(t *testing.T) {
	got := Normalize("hello - world")
	if got != "hello - world" {
		t.Errorf("拉丁文本的连字符应保留，得到 %q", got)
	}
}

func TestExtractLinks(t *testing.T) {
	entities := []tgapi.MessageEntity{
		{Type: "text_link", Offset: 0, Length: 4, URL: "https://t.me/joinchat/AbCdEf"},
	}
	links := ExtractLinks("点击查看", entities)
	if len(links) != 1 {
		t.Fatalf("期望 1 条链接，得到 %d", len(links))
	}
	if links[0].URL != "https://t.me/joinchat/AbCdEf" {
		t.Errorf("URL 不对：%s", links[0].URL)
	}
	if links[0].Kind != "text_link" {
		t.Errorf("类型不对：%s", links[0].Kind)
	}
}

// UTF-16 码元偏移：emoji 占两个码元，按 rune 下标切会错位。
// 这是从 JavaScript 迁到 Go 最容易踩的一个坑 —— JS 的字符串下标本来就是 UTF-16。
func TestEntityOffsetIsUTF16NotRune(t *testing.T) {
	// "🎁" 是一个码点但占 2 个 UTF-16 码元；"点击" 占 2 个 → offset 应为 4
	entities := []tgapi.MessageEntity{
		{Type: "text_link", Offset: 4, Length: 2, URL: "https://t.me/x"},
	}
	links := ExtractLinks("🎁点击查看", entities)
	if len(links) != 1 {
		t.Fatalf("期望 1 条链接，得到 %d", len(links))
	}
	if links[0].Text != "查看" {
		t.Errorf("按 UTF-16 偏移切出的文本应为「查看」，得到 %q", links[0].Text)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	got := EscapeMarkdown("昵称**加粗**与[链接]")
	want := `昵称\*\*加粗\*\*与\[链接\]`
	if got != want {
		t.Errorf("EscapeMarkdown = %q, 期望 %q", got, want)
	}
}
