package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/domain"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/secret"
	"github.com/tgs/server/internal/store"
	"github.com/tgs/server/internal/tgapi"
)

// Runtime 是单个机器人的运行时。
//
// 一个 Runtime = 一个 API 客户端 + 一个长轮询协程 + 若干中继协程。
// 生命周期完全由 Manager 掌控，面板上的「启用 / 停用 / 重新加载」
// 最终都落到这里的 Start / Stop —— 不需要重启进程。
type Runtime struct {
	botID        int64
	botName      string
	botTgID      int64
	adminGroupID int64

	api      *tgapi.Client
	db       *store.Store
	engine   *rules.Engine
	sanction *sanction.Engine
	bus      *bus.Bus
	log      *slog.Logger
	loc      *time.Location

	// 长轮询的并发度。中继任务主要是等 Telegram API，8 路足够；
	// 再高只会让限速器成为瓶颈。
	workers int

	// 每个联系人的消息串行执行，保证话题里的顺序与实际发送顺序一致。
	queues *keyedQueue

	// 客户端侧状态，不需要持久化
	floodMu     sync.Mutex
	floodWindow map[int64][]time.Time

	muteMu        sync.Mutex
	mutedNotified map[int64]*int64

	// 相册缓冲
	albumMu sync.Mutex
	albums  map[string]*albumBuffer

	// 管理机器人相关
	//
	// manager 是指回 Manager 的引用。需要它是因为管理命令要能
	// 开通/删除别的机器人，而那是 Manager 的职责 —— Runtime 自己
	// 做不了（它只持有自己那一个 api 客户端）。
	manager   *Manager
	isManager bool
	// 添加机器人的对话状态，key 是管理员的私聊 chat id
	mgrMu     sync.Mutex
	mgrStates map[int64]*addFlow

	cancel  context.CancelFunc
	stopped chan struct{}
	running bool
	mu      sync.Mutex
}

// Deps 是构造 Runtime 需要的依赖。
type Deps struct {
	DB       *store.Store
	Engine   *rules.Engine
	Sanction *sanction.Engine
	Bus      *bus.Bus
	Log      *slog.Logger
	Loc      *time.Location
	Workers  int
}

// NewRuntime 构造一个机器人运行时。
func NewRuntime(row store.BotRow, masterKey []byte, deps Deps) (*Runtime, error) {
	token, err := secret.Open(masterKey, row.Sealed())
	if err != nil {
		return nil, err
	}

	workers := deps.Workers
	if workers <= 0 {
		workers = 8
	}

	adminGroupID := int64(0)
	if row.AdminGroupID != nil {
		adminGroupID = *row.AdminGroupID
	}

	return &Runtime{
		botID:         row.ID,
		botName:       row.Name,
		botTgID:       derefInt(row.TelegramID),
		adminGroupID:  adminGroupID,
		api:           tgapi.New(token, tgapi.Options{}),
		db:            deps.DB,
		engine:        deps.Engine,
		sanction:      deps.Sanction,
		bus:           deps.Bus,
		log:           deps.Log.With("botId", row.ID, "bot", row.Username),
		loc:           deps.Loc,
		workers:       workers,
		queues:        newKeyedQueue(),
		floodWindow:   make(map[int64][]time.Time),
		mutedNotified: make(map[int64]*int64),
		albums:        make(map[string]*albumBuffer),
		isManager:     row.IsManager,
		mgrStates:     make(map[int64]*addFlow),
	}, nil
}

// BotID 返回机器人 id。
func (r *Runtime) BotID() int64 { return r.botID }

// IsRunning 报告运行时是否在跑。
func (r *Runtime) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// Start 启动长轮询。已在运行时是幂等的。
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	_ = r.db.UpdateBotHealth(ctx, r.botID, domain.HealthStarting, nil)

	// getMe 一次：既校验 token，也拿到真实的 telegram id 与用户名。
	// token 可能是几周前存的，期间用户可能已经在 BotFather 那里重置过。
	me, err := r.api.GetMe(ctx)
	if err != nil {
		msg := DescribeTelegramError(err)
		_ = r.db.UpdateBotHealth(ctx, r.botID, domain.HealthError, &msg)
		r.publishStatus()
		return fmt.Errorf("机器人启动失败: %s", msg)
	}

	if err := r.db.UpdateBotIdentity(ctx, r.botID, me.ID, me.Username); err != nil {
		r.log.Warn("更新机器人身份失败", "err", err)
	}
	r.mu.Lock()
	r.botTgID = me.ID
	r.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})

	r.mu.Lock()
	r.cancel = cancel
	r.stopped = stopped
	r.running = true
	r.mu.Unlock()

	if err := r.db.UpdateBotHealth(ctx, r.botID, domain.HealthOnline, nil); err != nil {
		r.log.Warn("更新健康状态失败", "err", err)
	}
	r.publishStatus()

	r.log.Info("机器人已启动长轮询", "username", me.Username)
	go r.pollLoop(runCtx, stopped)
	return nil
}

// Stop 停止长轮询并等待在途任务收尾。
func (r *Runtime) Stop(ctx context.Context) {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	cancel := r.cancel
	stopped := r.stopped
	r.running = false
	r.mu.Unlock()

	cancel()
	<-stopped

	// 等在途的中继任务跑完，避免关库后还有写入进来
	r.queues.drain()

	_ = r.db.UpdateBotHealth(ctx, r.botID, domain.HealthStopped, nil)
	r.publishStatus()
	r.log.Info("机器人已停止")
}

// ────────────────────────────── 长轮询 ──────────────────────────────

// allowedUpdates 只订阅真正会用到的更新类型。
// 默认全订阅会让 Telegram 推来大量我们根本不处理的更新，白白占用带宽。
var allowedUpdates = []string{
	"message",
	"edited_message",
	"callback_query",
	"my_chat_member",
	"chat_member",
}

func (r *Runtime) pollLoop(ctx context.Context, stopped chan<- struct{}) {
	defer close(stopped)

	const (
		pollTimeoutSec = 30
		// 每处理一批就落一次 offset 太浪费：丢了最多重复处理一次更新，
		// 而每次 getUpdates 都写一次库会明显增加 WAL 体积。
		saveEvery = 5 * time.Second
	)

	// offset 持久化：重启后不重复处理更新。
	//
	// 这不只是个优化 —— 没有它，用户在机器人重启期间发的消息会被
	// **再中继一次**，话题里出现重复内容，而管理员完全不知道为什么。
	offset, err := r.db.LoadOffset(ctx, r.botID)
	if err != nil {
		r.log.Warn("读取 offset 失败，从 0 开始", "err", err)
		offset = 0
	}

	var (
		pendingOffset int64
		lastSave      = time.Now()
		backoff       = time.Second
	)

	for {
		if ctx.Err() != nil {
			r.flushOffset(ctx, pendingOffset)
			return
		}

		updates, err := r.api.GetUpdates(ctx, offset, pollTimeoutSec, allowedUpdates)
		if err != nil {
			if ctx.Err() != nil {
				r.flushOffset(ctx, pendingOffset)
				return
			}
			// 409 表示同一个 token 在别处也在轮询 —— 重试没用，
			// 必须让管理员知道，而不是无限重连刷日志。
			var apiErr *tgapi.APIError
			if errors.As(err, &apiErr) && apiErr.Code == 409 {
				msg := "另一个进程正在用同一个 token 轮询（可能是重复启动，或 Telegram 侧 webhook 未清除）"
				r.log.Error(msg)
				_ = r.db.UpdateBotHealth(context.WithoutCancel(ctx), r.botID, domain.HealthError, &msg)
				r.publishStatus()
				return
			}

			r.log.Warn("长轮询失败，稍后重试", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				r.flushOffset(ctx, pendingOffset)
				return
			case <-time.After(backoff):
			}
			// 指数退避，封顶 30 秒
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}

		backoff = time.Second

		if len(updates) == 0 {
			r.flushOffset(ctx, pendingOffset)
			continue
		}

		for _, update := range updates {
			offset = update.UpdateID + 1
			pendingOffset = offset
			r.dispatch(ctx, update)
		}

		if time.Since(lastSave) > saveEvery {
			r.flushOffset(ctx, pendingOffset)
			lastSave = time.Now()
		}
	}
}

// flushOffset 持久化 offset。
func (r *Runtime) flushOffset(ctx context.Context, offset int64) {
	if offset <= 0 {
		return
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := r.db.SaveOffset(saveCtx, r.botID, offset); err != nil {
		r.log.Warn("持久化 offset 失败", "err", err)
	}
}

// ────────────────────────────── 更新分发 ──────────────────────────────

// dispatch 把一条更新路由到对应的处理器。
//
// 每个更新类型恰好只该被一个处理器接住，因此写成显式分支而不是
// 「注册一堆过滤器」—— 后者的匹配顺序由注册顺序决定，
// 从代码上完全看不出「这条更新会走到哪」。
func (r *Runtime) dispatch(ctx context.Context, u tgapi.Update) {
	// 每条更新用独立的超时，避免一条卡住的任务把整个批次拖死。
	// 长轮询本身没有超时（它要挂 30 秒），但处理更新必须有。
	handlerCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 管理机器人**不是**中继机器人，它只有两个入口：管理员的私聊命令，
	// 与菜单按钮的回调。其余更新一律丢弃。
	//
	// 分流放在分发的最前面，而不是散进各个 handler：这是一条全局性质
	// （控制台永不建话题、永不转发），漏掉任何一个分支都会表现为
	// 「陌生人给控制台发了条消息，管理群里却冒出一个话题」。
	if r.isManager {
		r.dispatchManager(handlerCtx, u)
		return
	}

	switch {
	case u.CallbackQuery != nil:
		r.handleCallback(handlerCtx, u.CallbackQuery)
	case u.MyChatMember != nil:
		r.handleMyChatMember(handlerCtx, u.MyChatMember)
	case u.ChatMember != nil:
		r.handleChatMember(handlerCtx, u.ChatMember)
	case u.EditedMessage != nil:
		// 编辑镜像不涉及顺序问题，直接跑
		r.handleEditedMessage(handlerCtx, u.EditedMessage)
	case u.Message != nil:
		r.handleMessage(handlerCtx, u.Message)
	}
}

// dispatchManager 是控制台机器人的分发，只有管理命令与按钮两条路。
//
// 群消息、编辑、成员变动这些对控制台都没有意义：它不该被拉进任何群
// 去转发，也不该出现在话题里。因此群里的消息一律不看 —— 否则有人在
// 群里发一句「/status」，机器人会私聊回一份系统概况，而群里的人
// 完全不知道发生了什么。
func (r *Runtime) dispatchManager(ctx context.Context, u tgapi.Update) {
	switch {
	case u.CallbackQuery != nil:
		r.handleManagerCallback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.Chat.Type == "private":
		r.handleManagerPrivate(ctx, u.Message)
	}
}

// handleMessage 分发普通消息。
func (r *Runtime) handleMessage(ctx context.Context, m *tgapi.Message) {
	if m.From == nil || m.From.IsBot {
		return
	}

	if m.Chat.Type == "private" {
		r.handlePrivate(ctx, m)
		return
	}

	if r.adminGroupID == 0 {
		return
	}
	if m.Chat.ID == r.adminGroupID {
		r.handleTopicMessage(ctx, m)
		return
	}

	// 其他群：机器人被拉进了不该在的地方。静默忽略，不回复 ——
	// 回复会在别人的群里刷存在感。
	r.log.Debug("收到非管理群的消息，已忽略", "chatId", m.Chat.ID)
}

// handlePrivate 处理私聊消息。
//
// 注意这里**不**判断管理机器人：控制台在 dispatch 就已经分流走了
// （见 dispatchManager），走不到中继管线来。
func (r *Runtime) handlePrivate(ctx context.Context, m *tgapi.Message) {
	// 命令分流。注意 /start 之外以 / 开头的文本**仍然走中继** ——
	// 用户发 "/price"、"/订单123" 这类内容非常常见，
	// 一刀切当成「未知命令」忽略掉，用户会觉得机器人坏了。
	if cmd := parseCommand(m.Text); cmd != "" {
		switch cmd {
		case "/start":
			r.runSerial(ctx, m.From.ID, func(c context.Context) { r.handleStart(c, m) })
			return
		case "/help":
			_, _ = r.sendToUser(ctx, m.From.ID, "直接发送消息即可 —— 管理员会在后台看到并回复你。\n\n支持发送文字、图片、语音、文件与相册。")
			return
		}
	}

	if !relayable(m) {
		return
	}

	// 相册：先缓冲，等价齐再一次性转发，才能保持相册形态
	if m.MediaGroupID != "" {
		r.bufferAlbum(ctx, m)
		return
	}

	r.runSerial(ctx, m.From.ID, func(c context.Context) {
		r.processUserMessage(c, []*tgapi.Message{m})
	})
}

// runSerial 让同一个用户的消息串行执行。
//
// 长轮询是**并发**处理更新的（这里用 goroutine 池），同一个用户连发的
// 两条消息会同时跑完「建话题 → 复制消息」，先发的完全可能后落地，
// 话题里的顺序就是乱的。人工客服场景里顺序即语义。
func (r *Runtime) runSerial(ctx context.Context, key int64, fn func(context.Context)) {
	sem := r.queues.forKey(key)
	sem <- struct{}{}
	go func() {
		defer func() { <-sem }()
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("中继任务 panic", "key", key, "panic", rec)
			}
		}()
		fn(ctx)
	}()
}

// handleTopicMessage 处理管理群话题里的消息。
func (r *Runtime) handleTopicMessage(ctx context.Context, m *tgapi.Message) {
	// 话题里只有带 message_thread_id 且 is_topic_message 的消息才是
	// 某个用户的会话。General 话题与机器人自己的消息都要排除 ——
	// 漏掉这个判断，管理群里的每一句话都会被喷给某个用户。
	if m.MessageThreadID == 0 || !m.IsTopicMessage {
		return
	}
	if m.From != nil && m.From.ID == r.botTgID {
		return
	}

	// 命令
	if cmd := parseCommand(m.Text); cmd != "" {
		if r.handleAdminCommand(ctx, m, cmd) {
			return
		}
	}

	topic, err := r.db.GetTopicByThread(ctx, r.botID, m.MessageThreadID)
	if err != nil {
		// General 话题或我们未创建的话题。静默忽略是正确的 ——
		// 在这里提示「未知话题」只会污染管理群的正常对话。
		return
	}

	r.runSerial(ctx, -topic.ID, func(c context.Context) {
		r.relayAdminReply(c, topic, m)
	})
}

// ────────────────────────────── 相册缓冲 ──────────────────────────────

type albumBuffer struct {
	chatID     int64
	fromUserID int64
	messages   []*tgapi.Message
	timer      *time.Timer
}

// bufferAlbum 缓冲一条相册消息。
//
// 用「每次 push 重置计时器」的 debounce 而不是固定窗口：
// 网络抖动会让同一组的消息间隔忽长忽短，固定窗口总有一批会被切成两半。
func (r *Runtime) bufferAlbum(ctx context.Context, m *tgapi.Message) {
	settings, err := r.db.GetBotSettings(ctx, r.botID)
	if err != nil {
		return
	}
	if settings.CoalesceWindowMs <= 0 {
		r.runSerial(ctx, m.From.ID, func(c context.Context) {
			r.processUserMessage(c, []*tgapi.Message{m})
		})
		return
	}

	window := time.Duration(settings.CoalesceWindowMs) * time.Millisecond

	r.albumMu.Lock()
	defer r.albumMu.Unlock()

	buf, ok := r.albums[m.MediaGroupID]
	if !ok {
		buf = &albumBuffer{chatID: m.Chat.ID, fromUserID: m.From.ID}
		r.albums[m.MediaGroupID] = buf
	}
	buf.messages = append(buf.messages, m)

	if buf.timer != nil {
		buf.timer.Stop()
	}
	// 用 context.Background 而不是请求的 ctx：批次可能在请求超时之后
	// 才到齐，用请求 ctx 会让最后一张图凭空消失。
	buf.timer = time.AfterFunc(window, func() {
		r.flushAlbum(m.MediaGroupID)
	})
}

func (r *Runtime) flushAlbum(groupID string) {
	r.albumMu.Lock()
	buf, ok := r.albums[groupID]
	if ok {
		delete(r.albums, groupID)
	}
	r.albumMu.Unlock()

	if !ok || len(buf.messages) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	messages := buf.messages
	key := buf.fromUserID
	r.runSerial(ctx, key, func(c context.Context) {
		r.processUserMessage(c, messages)
	})
}

// ────────────────────────────── 按钮回调 ──────────────────────────────

func (r *Runtime) handleCallback(ctx context.Context, q *tgapi.CallbackQuery) {
	// 必须先应答 —— 否则用户端的按钮会一直转圈，
	// 管理员会以为「点了没反应」并反复点击。
	defer func() {
		_ = r.api.AnswerCallbackQuery(context.WithoutCancel(ctx), q.ID, "", false)
	}()

	// 控制台机器人的按钮走 dispatchManager，这里只处理话题按钮（tgs: 前缀）。
	parsed, ok := parseCallback(q.Data)
	if !ok {
		return
	}

	topic, err := r.db.GetTopic(ctx, parsed.TopicID)
	if err != nil || topic.BotID != r.botID {
		_ = r.api.AnswerCallbackQuery(context.WithoutCancel(ctx), q.ID, "这个会话已经不存在了", true)
		return
	}

	actor := "admin"
	if q.From != nil {
		actor = q.From.DisplayName()
		if q.From.Username != "" {
			actor = "@" + q.From.Username
		}
	}

	switch parsed.Action {
	case "close":
		r.doClose(ctx, topic, actor)
	case "reopen":
		r.doReopen(ctx, topic, actor)
	case "ban":
		r.doBan(ctx, topic, actor)
	case "unban":
		r.doUnban(ctx, topic, actor)
	case "reset":
		r.doReset(ctx, topic, actor)
	}
}

// ────────────────────────────── 成员状态 ──────────────────────────────

// handleMyChatMember 记录机器人在管理群里的成员状态变化。
//
// 记录它是因为一个非常实际的运维问题：机器人被移出管理群后，
// 所有中继都会静默失败，管理员却只会看到「面板上一切正常」。
func (r *Runtime) handleMyChatMember(ctx context.Context, u *tgapi.ChatMemberUpdated) {
	if u.Chat.ID != r.adminGroupID {
		return
	}

	switch u.NewChatMember.Status {
	case "left", "kicked":
		msg := "机器人已被移出管理群 —— 中继将全部失败，请在面板中重新配置"
		r.log.Error(msg)
		_ = r.db.UpdateBotHealth(context.WithoutCancel(ctx), r.botID, domain.HealthError, &msg)
		r.publishStatus()
	case "administrator", "member":
		r.log.Info("机器人在管理群中的状态已更新", "status", u.NewChatMember.Status)
		_ = r.db.UpdateBotHealth(context.WithoutCancel(ctx), r.botID, domain.HealthOnline, nil)
		r.publishStatus()
	}
}

// handleChatMember 记录机器人自身的权限变化。
func (r *Runtime) handleChatMember(ctx context.Context, u *tgapi.ChatMemberUpdated) {
	if u.Chat.ID != r.adminGroupID {
		return
	}
	if u.NewChatMember.User == nil || u.NewChatMember.User.ID != r.botTgID {
		return
	}

	member := u.NewChatMember
	isAdmin := member.Status == "creator" || member.Status == "administrator"
	canManageTopics := member.Status == "creator" || (isAdmin && member.CanManageTopics)
	canDelete := member.Status == "creator" || (isAdmin && member.CanDeleteMessages)

	if !canManageTopics || !canDelete {
		r.log.Warn("机器人在管理群的权限不足，话题创建或消息删除会失败",
			"canManageTopics", canManageTopics, "canDeleteMessages", canDelete)
	}
}

// ────────────────────────────── 状态广播 ──────────────────────────────

func (r *Runtime) publishStatus() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	row, err := r.db.GetBot(ctx, r.botID)
	if err != nil {
		return
	}
	r.bus.Publish(bus.BotChannel(r.botID), bus.EventBotStatus, row.ToDTO())
}

// ────────────────────────────── 辅助 ──────────────────────────────

// parseCommand 从文本里解析出命令名（含前导斜杠），非命令返回空串。
func parseCommand(text string) string {
	if text == "" || text[0] != '/' {
		return ""
	}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case ' ', '\n', '@':
			return text[:i]
		}
	}
	return text
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// rulesContext 组装一次规则匹配的输入。
func rulesContext(botID int64, scope string, raw, normalized map[string]string) rules.Context {
	return rules.Context{
		BotID:      botID,
		Scope:      scope,
		Raw:        raw,
		Normalized: normalized,
	}
}

// ────────────────────────────── 串行队列 ──────────────────────────────

// keyedQueue 按 key 串行化任务。
//
// 用「每个 key 一个容量 1 的信号量」而不是一个任务列表：
// 中继任务本身是长跑的，我们只需要保证同一 key 上不会有两个同时在跑，
// 不需要排队 —— 排太长反而会让用户的最新消息等到天荒地老。
type keyedQueue struct {
	mu   sync.Mutex
	sems map[int64]chan struct{}
	live sync.WaitGroup
}

func newKeyedQueue() *keyedQueue {
	return &keyedQueue{sems: make(map[int64]chan struct{})}
}

func (q *keyedQueue) forKey(key int64) chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()

	sem, ok := q.sems[key]
	if !ok {
		sem = make(chan struct{}, 1)
		q.sems[key] = sem
	}
	return sem
}

// drain 等待所有在途任务结束。用于优雅关闭。
func (q *keyedQueue) drain() {
	q.live.Wait()
}
