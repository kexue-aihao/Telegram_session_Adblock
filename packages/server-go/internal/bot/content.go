package bot

import (
	"strings"

	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/tgapi"
)

// 把 grammY 那边对应的「消息内容抽取」搬到 Go。
//
// 单独成文件而不是塞进 handler：中继、规则引擎、编辑镜像三条路径都要
// 「从一条 Telegram 消息里读出内容类型与文本」，各写一遍必然三份实现慢慢漂移。

// detectContentType 做粗分类。中继会丢掉的信息在面板上无法还原，
// 因此只需要区分到「用什么图标显示」这个粒度。
func detectContentType(m *tgapi.Message) string {
	switch {
	case m.Text != "":
		return domain.ContentText
	case len(m.Photo) > 0:
		return domain.ContentPhoto
	case m.Video != nil:
		return domain.ContentVideo
	case m.Document != nil:
		return domain.ContentDocument
	case m.Audio != nil:
		return domain.ContentAudio
	case m.Voice != nil:
		return domain.ContentVoice
	case m.Sticker != nil:
		return domain.ContentSticker
	case m.Animation != nil:
		return domain.ContentAnimation
	case m.VideoNote != nil:
		return domain.ContentVideoNote
	case m.Location != nil:
		return domain.ContentLocation
	case m.Contact != nil:
		return domain.ContentContact
	case m.Poll != nil:
		return domain.ContentPoll
	case m.Dice != nil:
		return domain.ContentDice
	default:
		return domain.ContentUnknown
	}
}

// extractMedia 取出需要落库的媒体文件标识。
func extractMedia(m *tgapi.Message) []domain.Media {
	var out []domain.Media

	add := func(kind, fileID, uniqueID string) {
		out = append(out, domain.Media{
			Kind: kind, FileID: fileID, FileUniqueID: uniqueID, Position: len(out),
		})
	}

	// Telegram 下发的 photo 是同一张图的多个尺寸，取最大的那个
	if len(m.Photo) > 0 {
		largest := m.Photo[len(m.Photo)-1]
		add("photo", largest.FileID, largest.FileUniqueID)
	}
	if m.Video != nil {
		add("video", m.Video.FileID, m.Video.FileUniqueID)
	}
	if m.Animation != nil {
		add("animation", m.Animation.FileID, m.Animation.FileUniqueID)
	}
	if m.VideoNote != nil {
		add("video_note", m.VideoNote.FileID, m.VideoNote.FileUniqueID)
	}
	if m.Audio != nil {
		add("audio", m.Audio.FileID, m.Audio.FileUniqueID)
	}
	if m.Voice != nil {
		add("voice", m.Voice.FileID, m.Voice.FileUniqueID)
	}
	if m.Document != nil {
		add("document", m.Document.FileID, m.Document.FileUniqueID)
	}
	if m.Sticker != nil {
		add("sticker", m.Sticker.FileID, m.Sticker.FileUniqueID)
	}

	return out
}

// extractContent 把一条 Telegram 消息压成落库用的内容结构。
func extractContent(m *tgapi.Message) domain.MessageContent {
	source := m.EntitySource()
	entities := m.EntitiesOf()

	links := rules.ExtractLinks(source, entities)

	// 只有 text_link 才算「隐藏链接」：url entity 的链接是明摆着写在
	// 正文里的，管理员一眼能看见，而 text_link 的显示文本完全可以是
	// 「点击查看」，真实 URL 藏在 entity 里。
	hidden := make([]string, 0, len(links))
	for _, l := range links {
		if l.Kind == "text_link" {
			hidden = append(hidden, l.URL)
		}
	}

	content := domain.MessageContent{
		Type:          detectContentType(m),
		Media:         extractMedia(m),
		HasHiddenLink: len(hidden) > 0,
		HiddenLinks:   hidden,
	}
	if m.Text != "" {
		text := m.Text
		content.Text = &text
	}
	if m.Caption != "" {
		caption := m.Caption
		content.Caption = &caption
	}
	if m.MediaGroupID != "" {
		groupID := m.MediaGroupID
		content.MediaGroupID = &groupID
	}
	if content.Media == nil {
		content.Media = []domain.Media{}
	}
	return content
}

// relayable 报告这条消息是否值得中继。
// 服务消息（成员变动、置顶变更等）不该进话题。
func relayable(m *tgapi.Message) bool {
	if m.Text != "" || m.Caption != "" {
		return true
	}
	return detectContentType(m) != domain.ContentUnknown
}

// ────────────────────────────── 匹配目标 ──────────────────────────────

// buildHaystacks 按「匹配目标」把一条消息摊成多份文本。
//
// 为什么不合成一份大字符串：`target: 'url'` 的规则不应该被正文里的
// 「加微信」触发 —— 管理员选 url 就是明确表示「只查链接」。
// 合成一份会让 target 这个字段失去意义，规则会变得难以预测。
//
// 同时给出**原文**与**归一化文本**两份：引擎匹配归一化后的，
// 而 matchedText 要能回指原文，两者都留才解释得清「凭什么是它」。
func buildHaystacks(m *tgapi.Message) (raw, normalized map[string]string) {
	raw = make(map[string]string, 6)
	source := m.EntitySource()
	entities := m.EntitiesOf()

	if m.Text != "" {
		raw[domain.TargetText] = m.Text
	}
	if m.Caption != "" {
		raw[domain.TargetCaption] = m.Caption
	}
	if v := rules.EntityTexts(source, entities, "text_link"); v != "" {
		raw[domain.TargetTextLink] = v
	}
	if v := rules.EntityTexts(source, entities, "url"); v != "" {
		raw[domain.TargetURL] = v
	}
	if v := rules.EntityTexts(source, entities, "mention", "text_mention"); v != "" {
		raw[domain.TargetMention] = v
	}
	if v := forwardOriginText(m); v != "" {
		raw[domain.TargetForward] = v
	}

	// `all` 是「全都查」：正文 + 说明 + 各类 entity + 转发来源，一份合集。
	// 注意把隐藏链接的真实 URL 也并进来，否则最该查的那部分反而漏了。
	parts := make([]string, 0, 6)
	for _, key := range []string{
		domain.TargetText, domain.TargetCaption, domain.TargetTextLink,
		domain.TargetURL, domain.TargetMention, domain.TargetForward,
	} {
		if v, ok := raw[key]; ok && v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) > 0 {
		raw[domain.TargetAll] = strings.Join(parts, "\n")
	}

	normalized = make(map[string]string, len(raw))
	for k, v := range raw {
		normalized[k] = rules.Normalize(v)
	}
	return raw, normalized
}

// forwardOriginText 把转发来源拼成一段文本。
// 「转自 X 频道」本身就是引流信号，值得让规则看到。
func forwardOriginText(m *tgapi.Message) string {
	origin := m.ForwardOrigin
	if origin == nil {
		return ""
	}

	parts := make([]string, 0, 4)
	if origin.SenderUserName != "" {
		parts = append(parts, origin.SenderUserName)
	}
	if origin.SenderUser != nil {
		parts = append(parts, origin.SenderUser.DisplayName())
		if origin.SenderUser.Username != "" {
			parts = append(parts, "@"+origin.SenderUser.Username)
		}
	}
	if origin.Chat != nil {
		if origin.Chat.Title != "" {
			parts = append(parts, origin.Chat.Title)
		}
		if origin.Chat.Username != "" {
			parts = append(parts, "@"+origin.Chat.Username)
		}
	}
	return strings.Join(parts, "\n")
}

// rawText 返回用于审计兜底的原文。
func rawText(m *tgapi.Message) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}
