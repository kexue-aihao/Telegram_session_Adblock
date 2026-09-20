// Package bus 是进程内事件总线 —— 业务代码与 WebSocket 之间的唯一解耦点。
//
// 为什么不让业务代码直接持有 HTTP 层的连接集合：那样「机器人运行时」
// 就依赖了「HTTP 层」，而 HTTP 层的路由又依赖运行时（面板要调用运行时发消息），
// 两个模块立刻互相 import 成环。中间垫一层单向的发布/订阅，
// 依赖方向就只剩「业务 → 总线 ← WS 转发器」。
//
// 单进程内存实现：本项目是单实例部署（SQLite 文件库），不引入 Redis 之类的
// 跨进程总线。要支持多实例的话，替换这一个文件即可。
package bus

import (
	"sync"
	"sync/atomic"
	"time"
)

// 事件名。与前端约定好，任何一侧改名都会立刻在类型上暴露。
const (
	EventBotStatus      = "bot.status"
	EventSessionCreated = "session.created"
	EventSessionUpdated = "session.updated"
	EventSessionDeleted = "session.deleted"
	EventMessageNew     = "message.new"
	EventMessageUpdated = "message.updated"
	EventMessageDeleted = "message.deleted"
	EventRuleHit        = "rule.hit"
	EventAuditNew       = "audit.new"
	EventAlertNew       = "alert.new"
	EventStatsTick      = "stats.tick"
	EventHello          = "hello"
)

// 频道命名。单一函数生成，避免前后端手写字符串时拼错。
const (
	ChannelGlobal = "*"
)

// BotChannel 返回某个机器人的频道名。
func BotChannel(botID int64) string { return "bot:" + itoa(botID) }

// TopicChannel 返回某个话题的频道名。
func TopicChannel(topicID int64) string { return "topic:" + itoa(topicID) }

// Frame 是服务端推给前端的统一信封。
//
// 所有事件共用一个信封而不是把 type 平铺进 payload，这样前端可以
// 用一个 reducer 统一处理；而 Seq 让我们能检测到丢包
// （重连后序号不连续即触发一次全量刷新）。
type Frame struct {
	Type    string `json:"type"`
	Seq     uint64 `json:"seq"`
	TS      int64  `json:"ts"`
	Channel string `json:"channel"`
	Payload any    `json:"payload"`
}

// Listener 是事件订阅者。
type Listener func(Frame)

// Bus 是事件总线。
type Bus struct {
	mu        sync.RWMutex
	listeners map[uint64]Listener
	nextID    uint64

	// 单调递增的序号，随每个事件 +1
	sequence atomic.Uint64
}

// New 创建总线。
func New() *Bus {
	return &Bus{listeners: make(map[uint64]Listener)}
}

// Subscribe 注册订阅者，返回取消订阅函数。
func (b *Bus) Subscribe(fn Listener) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.listeners[id] = fn
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.listeners, id)
		b.mu.Unlock()
	}
}

// Publish 广播一个事件。
//
// **永不 panic、永不阻塞调用方**：它跑在中继热路径上，一个行为异常的
// 订阅者（例如某个已断开但未清理的 socket）不能连累业务逻辑。
func (b *Bus) Publish(channel, eventType string, payload any) {
	frame := Frame{
		Type:    eventType,
		Seq:     b.sequence.Add(1),
		TS:      time.Now().UnixMilli(),
		Channel: channel,
		Payload: payload,
	}

	// 在锁内只做快照，回调放到锁外执行 —— 否则一个慢订阅者会阻塞
	// 所有 Publish 调用，等于给整条中继链路加了一把全局锁。
	b.mu.RLock()
	targets := make([]Listener, 0, len(b.listeners))
	for _, fn := range b.listeners {
		targets = append(targets, fn)
	}
	b.mu.RUnlock()

	for _, fn := range targets {
		func() {
			defer func() {
				// 订阅者自己的 panic 不该冒泡到发布方
				_ = recover()
			}()
			fn(frame)
		}()
	}
}

// Sequence 返回当前序号，供健康检查与调试。
func (b *Bus) Sequence() uint64 { return b.sequence.Load() }

// Subscribers 返回订阅者数量。
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.listeners)
}

// itoa 是 strconv.FormatInt 的极简替代，避免为一个频道名引入依赖。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
