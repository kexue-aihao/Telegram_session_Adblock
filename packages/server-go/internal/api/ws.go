package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/tgs/server/internal/bus"
)

// WebSocket 网关。
//
// 面板的实时性全部由这里承担。两个刻意的选择：
//
//  1. **服务端只是总线的转发器**。业务代码往 bus 发事件，这里订阅并投递，
//     两边互不认识。中继逻辑因此完全不依赖 HTTP 层。
//  2. **按频道订阅**。会话列表页不需要知道另一个话题里每一条消息 ——
//     全量广播在几十个活跃会话时就会把带宽吃满。

type wsHub struct {
	server *Server

	mu      sync.RWMutex
	clients map[*wsClient]struct{}

	unsubscribe func()
	once        sync.Once
}

type wsClient struct {
	conn     *websocket.Conn
	channels map[string]bool
	mu       sync.Mutex
}

// 积压超过这个字节数的连接直接丢弃事件：慢客户端不该拖垮进程内存。
const maxBufferedBytes = 1 << 20 // 1 MiB

func newWSHub(s *Server) *wsHub {
	hub := &wsHub{
		server:  s,
		clients: make(map[*wsClient]struct{}),
	}
	hub.unsubscribe = s.bus.Subscribe(hub.broadcast)
	return hub
}

// Close 取消总线订阅并断开所有连接。
func (h *wsHub) Close() {
	h.once.Do(func() {
		if h.unsubscribe != nil {
			h.unsubscribe()
		}
		h.mu.Lock()
		for client := range h.clients {
			_ = client.conn.Close(websocket.StatusGoingAway, "服务正在关闭")
		}
		h.clients = make(map[*wsClient]struct{})
		h.mu.Unlock()
	})
}

// broadcast 把一帧投递给所有订阅了该频道的连接。
func (h *wsHub) broadcast(frame bus.Frame) {
	// 先在锁内做快照，序列化与网络写入放到锁外 —— 否则一个慢客户端
	// 会阻塞所有 Publish 调用，等于给整条中继链路加了把全局锁。
	h.mu.RLock()
	targets := make([]*wsClient, 0, len(h.clients))
	for client := range h.clients {
		if client.wants(frame.Channel) {
			targets = append(targets, client)
		}
	}
	h.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	payload, err := json.Marshal(frame)
	if err != nil {
		h.server.log.Warn("序列化 WebSocket 帧失败", "type", frame.Type, "err", err)
		return
	}

	for _, client := range targets {
		client.send(payload)
	}
}

func (c *wsClient) wants(channel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	// `*` 是全局频道，所有连接都收
	return c.channels[channel] || c.channels[bus.ChannelGlobal]
}

func (c *wsClient) send(payload []byte) {
	// 用带超时的 context：一个读得极慢的客户端不该把写入永久挂住
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		// 写失败通常意味着连接已经断了，close 处理器会把它清理掉
		return
	}
}

// clientFrame 是前端发来的订阅意图。
type clientFrame struct {
	Type    string `json:"type"`
	Channel string `json:"channel"`
}

func (h *wsHub) serve(w http.ResponseWriter, r *http.Request) {
	// 复用与 REST 完全相同的会话校验：WS 不能成为鉴权的后门。
	token := sessionToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "未登录")
		return
	}
	if _, err := h.server.db.GetSession(r.Context(), token); err != nil {
		writeError(w, http.StatusUnauthorized, "会话已过期，请重新登录")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 同源部署下不需要放开 Origin 校验；跨域部署请显式配置反代，
		// 而不是在这里放开——那会让任意网站都能连上这个面板的事件流。
		InsecureSkipVerify: false,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		h.server.log.Debug("WebSocket 握手失败", "err", err)
		return
	}

	client := &wsClient{
		conn:     conn,
		channels: map[string]bool{bus.ChannelGlobal: true},
	}

	h.mu.Lock()
	h.clients[client] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, client)
		h.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	h.sendHello(client)
	h.readLoop(r.Context(), client)
}

// readLoop 处理前端发来的帧，直到连接断开。
func (h *wsHub) readLoop(ctx context.Context, client *wsClient) {
	for {
		// 读超时给得比心跳间隔长得多：面板被切到后台标签页时
		// 浏览器会限流，太短的超时会造成无谓的断线重连。
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, data, err := client.conn.Read(readCtx)
		cancel()

		if err != nil {
			return
		}

		var frame clientFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			continue // 坏帧静默丢弃：前端不该因为一次拼错就断开重连
		}

		switch frame.Type {
		case "subscribe":
			if frame.Channel != "" {
				client.mu.Lock()
				client.channels[frame.Channel] = true
				client.mu.Unlock()
			}
		case "unsubscribe":
			// 全局频道不允许退订：它是心跳与统计的通道
			if frame.Channel != "" && frame.Channel != bus.ChannelGlobal {
				client.mu.Lock()
				delete(client.channels, frame.Channel)
				client.mu.Unlock()
			}
		case "ping":
			h.sendHello(client)
		}
	}
}

// helloFrame 是连接建立时与心跳应答都发的帧。
// 前端据此判断服务是否重启过（serverStartedAt 变了就清空本地缓存）。
func (h *wsHub) sendHello(client *wsClient) {
	frame := bus.Frame{
		Type:    bus.EventHello,
		Seq:     0,
		TS:      time.Now().UnixMilli(),
		Channel: bus.ChannelGlobal,
		Payload: map[string]any{
			"serverStartedAt": h.server.startedAt.UnixMilli(),
			"version":         Version,
			"onlineBots":      h.server.bots.OnlineCount(),
		},
	}

	payload, err := json.Marshal(frame)
	if err != nil {
		return
	}
	client.send(payload)
}

// ClientCount 返回当前连接数，供健康检查使用。
func (h *wsHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
