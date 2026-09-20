package tgapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// APIError 是 Telegram 返回的业务错误。
//
// 单独成型而不是一个字符串：调用方需要按 ErrorCode 分流 ——
// 403 意味着「用户屏蔽了机器人」这类业务状态，409 意味着轮询冲突，
// 429 意味着退避重试。把它们混成一个 error 字符串就只能靠匹配文本。
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram: %d %s (retry after %s)", e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

// IsBlockedByUser 报告错误是否是「用户屏蔽了机器人」。
// 这是一个需要被记录的业务状态，不是需要重试的故障。
func (e *APIError) IsBlockedByUser() bool {
	if e.Code != 403 {
		return false
	}
	d := strings.ToLower(e.Description)
	return strings.Contains(d, "blocked") ||
		strings.Contains(d, "user is deactivated") ||
		strings.Contains(d, "chat not found")
}

// Client 是一个机器人的 API 客户端。
//
// 每个 Client 持有自己的 http.Client —— 复用连接是关键：中继场景下
// 每秒可能十几次调用，每次新建 TLS 握手会让延迟高出一个数量级。
type Client struct {
	token string
	http  *http.Client
	base  string

	// 简单的客户端侧限速：Telegram 的限制是全局约 30 条/秒，
	// 这里留出余量。真正的退避由服务端 429 的 retry_after 驱动。
	mu       sync.Mutex
	nextSlot time.Time
	minGap   time.Duration

	// 请求超时；长轮询单独用更长的超时
	timeout time.Duration
}

// Options 控制客户端行为。
type Options struct {
	// MinInterval 是两次请求之间的最小间隔，用于客户端侧限速。
	MinInterval time.Duration
	// Timeout 是普通请求超时。
	Timeout time.Duration
	// BaseURL 允许指向自建的反代；留空使用官方地址。
	BaseURL string
}

// New 构造一个客户端。
//
// Transport 的参数是按「大量短小请求 + 少量长轮询」两类流量调的：
// 长轮询会占用连接几十秒，因此 MaxIdleConnsPerHost 必须够大，
// 否则中继请求要排队等空闲连接。
func New(token string, opts Options) *Client {
	if opts.MinInterval <= 0 {
		opts.MinInterval = 33 * time.Millisecond // ≈30 req/s
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.telegram.org"
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	return &Client{
		token:   token,
		base:    strings.TrimRight(opts.BaseURL, "/"),
		http:    &http.Client{Transport: transport, Timeout: 0}, // 超时按请求单独设
		minGap:  opts.MinInterval,
		timeout: opts.Timeout,
	}
}

// Token 返回明文 token。仅用于内部日志脱敏后的识别，不要写进响应。
func (c *Client) Token() string { return c.token }

// throttle 保证两次请求之间至少间隔 minGap。
func (c *Client) throttle(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	wait := time.Until(c.nextSlot)
	if wait < 0 {
		wait = 0
	}
	c.nextSlot = now.Add(wait).Add(c.minGap)
	c.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// call 是所有 API 调用的唯一出口。
//
// 重试策略刻意保守：只对 429（按 retry_after 等待）与网络层瞬时错误重试，
// 且最多两次。对 400/403 这类业务错误重试毫无意义 —— 重试一百次
// 「chat not found」也还是同样结果，只会拖慢中继。
func (c *Client) call(ctx context.Context, method string, params map[string]any, out any) error {
	const maxAttempts = 3

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return err
		}

		raw, err := c.do(ctx, method, params)
		if err == nil {
			if out == nil || len(raw) == 0 || string(raw) == "true" {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("解析 %s 的响应: %w", method, err)
			}
			return nil
		}

		lastErr = err

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.Code == 429 && attempt < maxAttempts-1 {
				wait := apiErr.RetryAfter
				if wait <= 0 {
					wait = time.Second
				}
				// 加一点抖动：多个机器人同时被限流时避免它们再次同时醒来
				wait += time.Duration(rand.Int63n(int64(250 * time.Millisecond)))
				if err := sleepCtx(ctx, wait); err != nil {
					return err
				}
				continue
			}
			// 其余业务错误直接返回，重试没有意义
			return err
		}

		// 网络层错误：可能是瞬时抖动，退避后重试
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < maxAttempts-1 {
			backoff := time.Duration(200*(1<<attempt)) * time.Millisecond
			if err := sleepCtx(ctx, backoff); err != nil {
				return err
			}
			continue
		}
	}

	return lastErr
}

func (c *Client) do(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("序列化 %s 的参数: %w", method, err)
	}

	timeout := c.timeout
	if method == "getUpdates" {
		// 长轮询：请求本身会挂起几十秒，超时必须大于服务端的 timeout 参数
		if t, ok := params["timeout"].(int); ok {
			timeout = time.Duration(t)*time.Second + 15*time.Second
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 %s: %w", method, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 的响应: %w", method, err)
	}

	// Telegram 在极端情况下会返回非 JSON（例如前置网关的错误页），
	// 此时直接按 HTTP 状态码报错，比让 json.Unmarshal 抛一个费解的语法错误好。
	if len(payload) == 0 {
		return nil, fmt.Errorf("调用 %s 返回了空响应（HTTP %d）", method, resp.StatusCode)
	}

	var envelope apiResponse
	if err := json.Unmarshal(payload, &envelope); err != nil {
		snippet := string(payload)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("调用 %s 返回了非 JSON 响应（HTTP %d）：%s", method, resp.StatusCode, snippet)
	}

	if !envelope.OK {
		apiErr := &APIError{Code: envelope.ErrorCode, Description: envelope.Description}
		if apiErr.Code == 0 {
			apiErr.Code = resp.StatusCode
		}
		if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		return nil, apiErr
	}

	return envelope.Result, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ────────────────────────────── 方法封装 ──────────────────────────────

// GetMe 校验 token 并取回机器人自身信息。
func (c *Client) GetMe(ctx context.Context) (*BotUser, error) {
	var out BotUser
	if err := c.call(ctx, "getMe", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetUpdates 长轮询拉取更新。
//
// offset 的语义：传入「上一个已确认处理的 update_id + 1」，
// Telegram 会丢弃所有更早的更新。这正是重启后不重复处理的关键。
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int, allowed []string) ([]Update, error) {
	params := map[string]any{"timeout": timeoutSec}
	if offset > 0 {
		params["offset"] = offset
	}
	if len(allowed) > 0 {
		params["allowed_updates"] = allowed
	}

	var out []Update
	if err := c.call(ctx, "getUpdates", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendMessageOptions 是发消息的可选参数。
type SendMessageOptions struct {
	ThreadID            int64
	Markdown            bool
	Keyboard            *InlineKeyboardMarkup
	DisableNotification bool
	DisableLinkPreview  bool
}

// SendMessage 发送文本消息。
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, opts SendMessageOptions) (*Message, error) {
	if text == "" {
		return nil, nil
	}
	params := map[string]any{"chat_id": chatID, "text": text}
	applySendOptions(params, opts)
	params["link_preview_options"] = LinkPreviewOptions{IsDisabled: opts.DisableLinkPreview}

	var out Message
	if err := c.call(ctx, "sendMessage", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func applySendOptions(params map[string]any, opts SendMessageOptions) {
	if opts.ThreadID != 0 {
		params["message_thread_id"] = opts.ThreadID
	}
	if opts.Markdown {
		params["parse_mode"] = "Markdown"
	}
	if opts.Keyboard != nil {
		params["reply_markup"] = opts.Keyboard
	}
	if opts.DisableNotification {
		params["disable_notification"] = true
	}
}

// CopyMessage 复制一条消息到另一个会话。
//
// 用它而不是「下载再上传」：保留了原始媒体与格式，也保留转发来源，
// 而且不需要我们经手文件内容。
func (c *Client) CopyMessage(ctx context.Context, chatID, fromChatID, messageID int64, threadID int64) (*MessageID, error) {
	params := map[string]any{
		"chat_id":      chatID,
		"from_chat_id": fromChatID,
		"message_id":   messageID,
	}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}

	var out MessageID
	if err := c.call(ctx, "copyMessage", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CopyMessages 批量复制，用于相册。
//
// 必须整组一起发才能保持相册形态 —— 逐条 copyMessage 会被 Telegram
// 渲染成 N 条独立消息，视觉上完全走样。
func (c *Client) CopyMessages(ctx context.Context, chatID, fromChatID int64, messageIDs []int64, threadID int64) ([]MessageID, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	params := map[string]any{
		"chat_id":      chatID,
		"from_chat_id": fromChatID,
		"message_ids":  messageIDs,
	}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}

	var out []MessageID
	if err := c.call(ctx, "copyMessages", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteMessage 删除一条消息。
// 只对 48 小时内的消息有效，且私聊里只能删 incoming 的消息。
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}, nil)
}

// EditMessageText 编辑文本消息。
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	return c.call(ctx, "editMessageText", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}, nil)
}

// EditMessageCaption 编辑媒体说明文字。
func (c *Client) EditMessageCaption(ctx context.Context, chatID, messageID int64, caption string) error {
	return c.call(ctx, "editMessageCaption", map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"caption":    caption,
	}, nil)
}

// CreateForumTopic 在论坛群里建一个话题。
func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (*ForumTopic, error) {
	params := map[string]any{"chat_id": chatID, "name": name}
	if iconColor != 0 {
		params["icon_color"] = iconColor
	}

	var out ForumTopic
	if err := c.call(ctx, "createForumTopic", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EditForumTopic 重命名话题。
func (c *Client) EditForumTopic(ctx context.Context, chatID, threadID int64, name string) error {
	return c.call(ctx, "editForumTopic", map[string]any{
		"chat_id":           chatID,
		"message_thread_id": threadID,
		"name":              name,
	}, nil)
}

// CloseForumTopic 关闭话题（仍可重新打开，历史保留）。
func (c *Client) CloseForumTopic(ctx context.Context, chatID, threadID int64) error {
	return c.call(ctx, "closeForumTopic", map[string]any{
		"chat_id":           chatID,
		"message_thread_id": threadID,
	}, nil)
}

// ReopenForumTopic 重新打开话题。
func (c *Client) ReopenForumTopic(ctx context.Context, chatID, threadID int64) error {
	return c.call(ctx, "reopenForumTopic", map[string]any{
		"chat_id":           chatID,
		"message_thread_id": threadID,
	}, nil)
}

// DeleteForumTopic 彻底删除话题及其全部消息。
func (c *Client) DeleteForumTopic(ctx context.Context, chatID, threadID int64) error {
	return c.call(ctx, "deleteForumTopic", map[string]any{
		"chat_id":           chatID,
		"message_thread_id": threadID,
	}, nil)
}

// PinChatMessage 置顶消息。
func (c *Client) PinChatMessage(ctx context.Context, chatID, messageID int64, silent bool) error {
	params := map[string]any{"chat_id": chatID, "message_id": messageID}
	if silent {
		params["disable_notification"] = true
	}
	return c.call(ctx, "pinChatMessage", params, nil)
}

// GetChat 取会话信息。
func (c *Client) GetChat(ctx context.Context, chatID int64) (*Chat, error) {
	var out Chat
	if err := c.call(ctx, "getChat", map[string]any{"chat_id": chatID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetChatMember 取成员状态。
func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (*ChatMember, error) {
	var out ChatMember
	if err := c.call(ctx, "getChatMember", map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnswerCallbackQuery 应答按钮回调。
//
// 必须调用 —— 否则用户端的按钮会一直转圈，管理员会以为「点了没反应」
// 并反复点击。即使后续动作失败也要先应答。
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	params := map[string]any{"callback_query_id": id}
	if text != "" {
		params["text"] = text
	}
	if alert {
		params["show_alert"] = true
	}
	return c.call(ctx, "answerCallbackQuery", params, nil)
}

// RestrictChatMember 限制群成员发言。
//
// 注意：终端用户通常**不在**管理群里，这个调用会直接报错。
// 只在「该用户恰好也是群成员」时才尝试，且失败必须静默忽略。
func (c *Client) RestrictChatMember(ctx context.Context, chatID, userID int64, until int64) error {
	permissions := map[string]bool{
		"can_send_messages": false,
	}
	params := map[string]any{
		"chat_id":     chatID,
		"user_id":     userID,
		"permissions": permissions,
	}
	if until > 0 {
		params["until_date"] = until
	}
	return c.call(ctx, "restrictChatMember", params, nil)
}

// InlineKeyboard 是构造内联键盘的辅助。
type InlineKeyboard struct {
	rows [][]InlineKeyboardButton
}

// NewInlineKeyboard 创建空键盘。
func NewInlineKeyboard() *InlineKeyboard { return &InlineKeyboard{} }

// Text 追加一个回调按钮到当前行末尾。
func (k *InlineKeyboard) Text(text, callbackData string) *InlineKeyboard {
	if len(k.rows) == 0 {
		k.rows = append(k.rows, nil)
	}
	last := len(k.rows) - 1
	k.rows[last] = append(k.rows[last], InlineKeyboardButton{Text: text, CallbackData: callbackData})
	return k
}

// Row 结束当前行，后续按钮另起一行。
func (k *InlineKeyboard) Row() *InlineKeyboard {
	k.rows = append(k.rows, nil)
	return k
}

// Markup 返回可直接放进请求的键盘结构。
func (k *InlineKeyboard) Markup() *InlineKeyboardMarkup {
	// 去掉 Row() 留下的空行，否则 Telegram 会报「按钮行为空」
	rows := make([][]InlineKeyboardButton, 0, len(k.rows))
	for _, row := range k.rows {
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return &InlineKeyboardMarkup{InlineKeyboard: rows}
}

// IsTooOldToDelete 判断错误是否表示消息已超过可删除的时间窗口。
func IsTooOldToDelete(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	d := strings.ToLower(apiErr.Description)
	return strings.Contains(d, "message can't be deleted") ||
		strings.Contains(d, "message to delete not found") ||
		strings.Contains(d, "too old")
}

// ParseRetryAfter 从错误里取出建议的重试间隔，没有则返回 0。
func ParseRetryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}
