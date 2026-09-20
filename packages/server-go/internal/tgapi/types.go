// Package tgapi 是 Telegram Bot API 的轻量客户端。
//
// 刻意手写而不是引入第三方 SDK：这个项目只需要 20 来个方法，而 Bot API
// 本身就是 JSON over HTTP。手写带来三件必须自己掌控的事：
//
//  1. 限速与 429 退避 —— Telegram 大约 30 条/秒全局、每群 20 条/分钟，
//     中继场景（一个用户连发几条就要往话题发几条）极易触发；
//  2. offset 的精确语义 —— 重启后不能重复处理更新；
//  3. 错误信息直接面向管理员 —— 需要翻译成「token 失效」「缺少权限」
//     这类可执行的提示，而不是把英文原文抛到面板上。
package tgapi

import "encoding/json"

// Update 是 getUpdates 返回的一个更新。
type Update struct {
	UpdateID      int64              `json:"update_id"`
	Message       *Message           `json:"message,omitempty"`
	EditedMessage *Message           `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery     `json:"callback_query,omitempty"`
	MyChatMember  *ChatMemberUpdated `json:"my_chat_member,omitempty"`
	ChatMember    *ChatMemberUpdated `json:"chat_member,omitempty"`
}

// User 是 Telegram 用户。
type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
}

// DisplayName 返回适合展示的名字。
func (u *User) DisplayName() string {
	if u.LastName != "" {
		return u.FirstName + " " + u.LastName
	}
	return u.FirstName
}

// Chat 是会话（私聊或群）。
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"` // private | group | supergroup | channel
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
	IsForum  bool   `json:"is_forum,omitempty"`
}

// MessageEntity 是消息里的富文本实体。
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	User   *User  `json:"user,omitempty"`
}

// MessageOrigin 是转发来源（Bot API 7.0 起取代了 forward_from 系列字段）。
type MessageOrigin struct {
	Type           string `json:"type"` // user | hidden_user | chat | channel
	Date           int64  `json:"date"`
	SenderUserName string `json:"sender_user_name,omitempty"`
	SenderUser     *User  `json:"sender_user,omitempty"`
	Chat           *Chat  `json:"chat,omitempty"`
}

// PhotoSize 是同一张图的一个尺寸。
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
}

// File 是带 file_id / file_unique_id 的媒体负载。
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Voice 是语音消息。
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// VideoNote 是圆形视频留言。
type VideoNote struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Length       int    `json:"length"`
	Duration     int    `json:"duration"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Sticker 是贴纸。
type Sticker struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Type         string `json:"type"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Emoji        string `json:"emoji,omitempty"`
}

// Location 是位置。
type Location struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

// Contact 是联系人名片。
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
}

// Poll 是投票。
type Poll struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

// Dice 是骰子。
type Dice struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}

// Message 是一条消息。
//
// 只声明本项目会用到的字段 —— 完整实现有上百个字段，而未知字段
// 交给 encoding/json 忽略即可，这样 Telegram 增加字段时不需要改代码。
type Message struct {
	MessageID       int64           `json:"message_id"`
	MessageThreadID int64           `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool            `json:"is_topic_message,omitempty"`
	From            *User           `json:"from,omitempty"`
	Chat            Chat            `json:"chat"`
	Date            int64           `json:"date"`
	EditDate        int64           `json:"edit_date,omitempty"`
	MediaGroupID    string          `json:"media_group_id,omitempty"`
	Text            string          `json:"text,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	Caption         string          `json:"caption,omitempty"`
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	ForwardOrigin   *MessageOrigin  `json:"forward_origin,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`

	Animation *File       `json:"animation,omitempty"`
	Audio     *File       `json:"audio,omitempty"`
	Document  *File       `json:"document,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
	Sticker   *Sticker    `json:"sticker,omitempty"`
	Video     *File       `json:"video,omitempty"`
	VideoNote *VideoNote  `json:"video_note,omitempty"`
	Voice     *Voice      `json:"voice,omitempty"`

	Location *Location `json:"location,omitempty"`
	Contact  *Contact  `json:"contact,omitempty"`
	Poll     *Poll     `json:"poll,omitempty"`
	Dice     *Dice     `json:"dice,omitempty"`

	// 服务消息：用于识别「机器人被移出群」这类事件
	NewChatMembers []User   `json:"new_chat_members,omitempty"`
	LeftChatMember *User    `json:"left_chat_member,omitempty"`
	NewChatTitle   string   `json:"new_chat_title,omitempty"`
	PinnedMessage  *Message `json:"pinned_message,omitempty"`
}

// HasEntities 报告消息是否带富文本实体（用于隐藏链接检测）。
func (m *Message) HasEntities() bool {
	return len(m.Entities) > 0 || len(m.CaptionEntities) > 0
}

// EntitySource 返回实体所属的那段文本。
// 带 caption 的消息，其实体属于 caption 而不是 text。
func (m *Message) EntitySource() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

// EntitiesOf 统一取实体列表。
func (m *Message) EntitiesOf() []MessageEntity {
	if len(m.Entities) > 0 {
		return m.Entities
	}
	return m.CaptionEntities
}

// CallbackQuery 是内联按钮回调。
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// ChatMember 是群成员状态。
//
// CanManageTopics / CanDeleteMessages 等权限字段只对 administrator 有意义，
// 因此用 omitempty 且调用方必须先判断 Status。
type ChatMember struct {
	Status             string `json:"status"` // creator | administrator | member | restricted | left | kicked
	User               *User  `json:"user"`
	CanManageTopics    bool   `json:"can_manage_topics,omitempty"`
	CanDeleteMessages  bool   `json:"can_delete_messages,omitempty"`
	CanRestrictMembers bool   `json:"can_restrict_members,omitempty"`
	CanPinMessages     bool   `json:"can_pin_messages,omitempty"`
}

// IsAdmin 报告该成员是否是管理员或群主。
func (m *ChatMember) IsAdmin() bool {
	return m.Status == "creator" || m.Status == "administrator"
}

// ChatMemberUpdated 是成员状态变更。
type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          *User      `json:"from"`
	Date          int64      `json:"date"`
	OldChatMember ChatMember `json:"old_chat_member"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

// ForumTopic 是新建话题的返回值。
type ForumTopic struct {
	MessageThreadID   int64  `json:"message_thread_id"`
	Name              string `json:"name"`
	IconColor         int    `json:"icon_color"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

// MessageID 只带消息 id 的返回值。
type MessageID struct {
	MessageID int64 `json:"message_id"`
}

// BotUser 是 getMe 的返回值。
type BotUser struct {
	ID                      int64  `json:"id"`
	IsBot                   bool   `json:"is_bot"`
	FirstName               string `json:"first_name"`
	Username                string `json:"username"`
	CanJoinGroups           bool   `json:"can_join_groups"`
	CanReadAllGroupMessages bool   `json:"can_read_all_group_messages"`
}

// InlineKeyboardButton 是内联按钮。
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// InlineKeyboardMarkup 是内联键盘。
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// LinkPreviewOptions 控制链接预览。
type LinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled,omitempty"`
}

// apiResponse 是所有 Bot API 响应的统一信封。
type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Parameters  *struct {
		RetryAfter      int   `json:"retry_after,omitempty"`
		MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
	} `json:"parameters,omitempty"`
}
