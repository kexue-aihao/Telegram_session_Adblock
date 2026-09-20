package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/store"
)

// Manager 持有全部机器人的运行时。
//
// 面板上的「启用 / 停用 / 重新加载」最终都落到这里 —— 增删机器人与改 token
// 都不需要重启进程，这是面板「热重载」承诺的全部内容。
type Manager struct {
	mu       sync.RWMutex
	runtimes map[int64]*Runtime

	db       *store.Store
	engine   *rules.Engine
	sanction *sanction.Engine
	bus      *bus.Bus
	log      *slog.Logger
	loc      *time.Location
	key      []byte
	workers  int

	stopped bool
}

// ManagerOptions 是构造 Manager 的依赖。
type ManagerOptions struct {
	DB            *store.Store
	Engine        *rules.Engine
	Sanction      *sanction.Engine
	Bus           *bus.Bus
	Log           *slog.Logger
	Location      *time.Location
	MasterKey     []byte
	WorkersPerBot int
}

// NewManager 构造管理器。
func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		runtimes: make(map[int64]*Runtime),
		db:       opts.DB,
		engine:   opts.Engine,
		sanction: opts.Sanction,
		bus:      opts.Bus,
		log:      opts.Log,
		loc:      opts.Location,
		key:      opts.MasterKey,
		workers:  opts.WorkersPerBot,
	}
}

// StartAll 启动所有已启用的机器人。
//
// 单个失败不影响其余：一个坏 token 不该让全站瘫痪 ——
// 面板本身必须可用，否则管理员连「去修那个坏 token」的入口都没有。
func (m *Manager) StartAll(ctx context.Context) {
	rows, err := m.db.ListEnabledBots(ctx)
	if err != nil {
		m.log.Error("读取机器人列表失败", "err", err)
		return
	}

	started := 0
	for _, row := range rows {
		if err := m.Start(ctx, row.ID); err != nil {
			m.log.Error("机器人启动失败，已跳过", "botId", row.ID, "err", err)
			continue
		}
		started++
	}
	m.log.Info("机器人启动完成", "started", started, "total", len(rows))
}

// Start 启动单个机器人；已在运行时先停掉再起（等价于 reload）。
func (m *Manager) Start(ctx context.Context, botID int64) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return errors.New("管理器正在关闭")
	}
	existing := m.runtimes[botID]
	m.mu.Unlock()

	// 停掉旧的（可能是换了 token）
	if existing != nil {
		existing.Stop(ctx)
	}

	row, err := m.db.GetBot(ctx, botID)
	if err != nil {
		return fmt.Errorf("读取机器人: %w", err)
	}
	if !row.IsEnabled {
		m.log.Info("机器人已停用，不启动", "botId", botID)
		m.mu.Lock()
		delete(m.runtimes, botID)
		m.mu.Unlock()
		return nil
	}

	runtime, err := NewRuntime(row, m.key, Deps{
		DB:       m.db,
		Engine:   m.engine,
		Sanction: m.sanction,
		Bus:      m.bus,
		Log:      m.log,
		Loc:      m.loc,
		Workers:  m.workers,
	})
	if err != nil {
		// 解密失败通常意味着 MASTER_KEY 换了 —— 这个错误必须原样传给面板，
		// 它已经写成了一句可执行的指引。
		return err
	}

	// 回指：管理机器人需要开通/删除别的机器人，那是 Manager 的职责。
	// 必须在 Start 之前设好 —— Start 会注册处理器，而处理器一收到
	// 管理命令就会用到它。
	runtime.manager = m

	// 保留在 map 里，让面板能看到「这个机器人存在但起不来」以及具体原因。
	// 删掉会让它在界面上凭空消失。
	m.mu.Lock()
	m.runtimes[botID] = runtime
	m.mu.Unlock()

	if err := runtime.Start(ctx); err != nil {
		return err
	}
	return nil
}

// Stop 停止单个机器人并从内存中移除运行时。
func (m *Manager) Stop(ctx context.Context, botID int64) {
	m.mu.Lock()
	runtime := m.runtimes[botID]
	delete(m.runtimes, botID)
	m.mu.Unlock()

	if runtime != nil {
		runtime.Stop(ctx)
	}
}

// Reload 停掉再起。换过 token、改过管理群、或怀疑轮询卡住时用。
func (m *Manager) Reload(ctx context.Context, botID int64) error {
	m.log.Info("重新加载机器人", "botId", botID)
	return m.Start(ctx, botID)
}

// StopAll 优雅关闭：先停轮询，等在途任务收尾。
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	m.stopped = true
	runtimes := make([]*Runtime, 0, len(m.runtimes))
	for _, r := range m.runtimes {
		runtimes = append(runtimes, r)
	}
	m.runtimes = make(map[int64]*Runtime)
	m.mu.Unlock()

	// 并行停：每个 Stop 要等在途任务收尾，串行会让关闭时间线性增长
	var wg sync.WaitGroup
	for _, r := range runtimes {
		wg.Add(1)
		go func(rt *Runtime) {
			defer wg.Done()
			rt.Stop(ctx)
		}(r)
	}
	wg.Wait()

	m.log.Info("全部机器人已停止", "stopped", len(runtimes))
}

// Get 取运行时；不存在返回 nil。
func (m *Manager) Get(botID int64) *Runtime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runtimes[botID]
}

// Require 取运行时，不存在时返回带可执行提示的错误。
// 供 HTTP 层直接转成 400 响应。
func (m *Manager) Require(botID int64) (*Runtime, error) {
	rt := m.Get(botID)
	if rt == nil {
		return nil, fmt.Errorf("机器人 %d 当前未运行，请在面板中启用它", botID)
	}
	return rt, nil
}

// OnlineCount 返回正在运行的机器人数。
func (m *Manager) OnlineCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, r := range m.runtimes {
		if r.IsRunning() {
			count++
		}
	}
	return count
}

// Size 返回已加载的运行时数量（含启动失败的）。
func (m *Manager) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.runtimes)
}
