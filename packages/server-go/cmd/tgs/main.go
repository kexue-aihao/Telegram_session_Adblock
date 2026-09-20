// Command tgs 是服务入口。
//
// 启动顺序是有依赖的：环境变量 → 数据库 → 表结构 → 播种 → 机器人 → HTTP。
// 任何一步失败都必须让进程**立刻退出**并打印原因，而不是带着半截状态
// 继续跑 —— 一个没建表的库配上正在轮询的机器人，会把数据写成一团乱麻。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// 把 IANA 时区库**编进二进制**，而不是依赖运行时的 /usr/share/zoneinfo。
	//
	// 这一行决定了镜像能不能用 scratch：scratch 里没有任何文件，
	// 没有它的话 time.LoadLocation("Asia/Shanghai") 会失败 ——
	// 而 env.Load 里对时区失败是「记一个问题然后回落到 UTC」，
	// 于是统计会在 UTC 切日，面板上「今日拦截数」在北京时间早上 8 点前
	// 显示的是昨天的数字。这类偏差不会报错，只会静默地给出错误答案。
	//
	// 代价是二进制大约多 450KB。
	_ "time/tzdata"

	"github.com/tgs/server/internal/api"
	"github.com/tgs/server/internal/bot"
	"github.com/tgs/server/internal/bus"
	"github.com/tgs/server/internal/config"
	"github.com/tgs/server/internal/logging"
	"github.com/tgs/server/internal/rules"
	"github.com/tgs/server/internal/sanction"
	"github.com/tgs/server/internal/store"
)

func main() {
	// 自检模式：`tgs -healthcheck`
	//
	// scratch 镜像里没有 shell、没有 curl、没有 wget ——
	// HEALTHCHECK 唯一能执行的就是这个二进制自己。
	// 没有它的话，容器编排层就只能「端口通了就算活着」，
	// 而端口通、数据库连不上的服务是完全不可用的。
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(runHealthcheck())
	}

	if err := run(); err != nil {
		// 用 fmt 而不是 logger：配置本身可能就没加载成功，
		// 那时 logger 还不存在。
		fmt.Fprintf(os.Stderr, "\n启动失败：\n%v\n\n", err)
		os.Exit(1)
	}
}

// runHealthcheck 请求自己的就绪探针，返回进程退出码。
//
// 用 /api/health/ready 而不是 /api/health：前者会真的 ping 一次数据库。
// 只检查存活的话，「进程还在但库挂了」会被判定为健康 ——
// 那正是最需要被重启的状态。
func runHealthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/api/health/ready", nil)
	if err != nil {
		return 1
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	// ── 配置 ──────────────────────────────────────────────────
	env, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(env.LogLevel)
	log.Info("正在启动 Telegram 会话中继服务",
		"env", env.AppEnv,
		"host", env.Host,
		"port", env.Port,
		"timezone", env.Location.String())
	if env.EnvFile != "" {
		log.Debug("已加载环境变量文件", "path", env.EnvFile)
	}

	// PUBLIC_URL / WEBHOOK_* 目前只是占位：配置层会解析它们，
	// 但运行时只有长轮询，webhook 尚未实现。
	//
	// 显式告警而不是静默忽略：这几个变量的名字本身就在承诺
	// 「设了我就会切到 webhook」，而用户设完之后唯一能观察到的现象是
	// 「没什么变化」—— 那比报错更难排查。
	if env.PublicURL != "" {
		log.Warn("PUBLIC_URL 已设置，但当前版本只有长轮询，webhook 模式尚未实现；该变量会被忽略",
			"publicUrl", env.PublicURL)
	}

	// ── 数据库 ────────────────────────────────────────────────
	startCtx, cancelStart := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStart()

	db, err := store.Open(startCtx, env.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	log.Info("数据库已连接", "path", db.Path())

	// ── 播种 ──────────────────────────────────────────────────
	if err := db.Seed(startCtx, env.AdminUsername, env.AdminPassword); err != nil {
		if errors.Is(err, store.ErrNoAdminPassword) {
			return fmt.Errorf("%w\n\n生成一个强密码后填入 .env 的 ADMIN_PASSWORD", err)
		}
		return err
	}

	// ── 业务组件 ──────────────────────────────────────────────
	eventBus := bus.New()
	engine := rules.New(db.Read())
	sanctionEngine := sanction.New(db, log)

	manager := bot.NewManager(bot.ManagerOptions{
		DB:            db,
		Engine:        engine,
		Sanction:      sanctionEngine,
		Bus:           eventBus,
		Log:           log,
		Location:      env.Location,
		MasterKey:     env.MasterKey,
		WorkersPerBot: 8,
	})

	// 单个机器人失败不影响其余：一个坏 token 不该让全站瘫痪 ——
	// 面板本身必须可用，否则管理员连「去修那个坏 token」的入口都没有。
	manager.StartAll(startCtx)

	// ── HTTP ──────────────────────────────────────────────────
	webDist := env.WebDist
	if webDist == "" {
		webDist = defaultWebDist()
	}

	server := api.New(api.Options{
		DB:            db,
		Engine:        engine,
		Sanction:      sanctionEngine,
		Bots:          manager,
		Bus:           eventBus,
		Log:           log,
		Location:      env.Location,
		SessionSecret: env.SessionSecret,
		MasterKey:     env.MasterKey,
		IsProd:        env.IsProd,
		WebDist:       webDist,
	})

	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", env.Host, env.Port),
		Handler: server.Handler(),
		// 长轮询由机器人自己发起，HTTP 服务只处理面板请求 ——
		// 超时可以给得比较短。但 WebSocket 需要长连接，所以 WriteTimeout
		// 必须是 0（由 WS 自己管超时），否则连接会被反复掐断。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	// ── 后台任务 ──────────────────────────────────────────────
	bgCtx, cancelBg := context.WithCancel(context.Background())
	defer cancelBg()
	startBackgroundJobs(bgCtx, db, sanctionEngine, engine, eventBus, log, env.Location)

	// ── 监听 ──────────────────────────────────────────────────
	listenErr := make(chan error, 1)
	go func() {
		display := env.Host
		if display == "0.0.0.0" {
			display = "localhost"
		}
		log.Info("服务已就绪", "url", fmt.Sprintf("http://%s:%d", display, env.Port))

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErr <- err
		}
	}()

	// ── 等待退出信号 ──────────────────────────────────────────
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-listenErr:
		return fmt.Errorf("HTTP 服务异常退出：%w", err)

	case sig := <-signals:
		log.Info("收到退出信号，正在关闭", "signal", sig.String())
	}

	return shutdown(db, manager, httpServer, eventBus, log)
}

// shutdown 优雅关闭。
func shutdown(
	db *store.Store,
	manager *bot.Manager,
	httpServer *http.Server,
	eventBus *bus.Bus,
	log *slog.Logger,
) error {
	// 给整个关闭过程一个上限：卡住的关闭比不关闭更糟，
	// 因为运维会以为进程已经死了。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// 1. 先停 HTTP：不再接受新的面板请求
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Warn("关闭 HTTP 服务出错", "err", err)
	}

	// 2. 停机器人：等在途的中继任务收尾，避免关库后还有写入进来
	manager.StopAll(ctx)

	// 3. 关数据库
	if err := db.Close(); err != nil {
		log.Warn("关闭数据库出错", "err", err)
	}

	_ = eventBus
	log.Info("已安全退出")
	return nil
}

// startBackgroundJobs 启动定时任务。
//
// 全部挤在同一个地方而不是各起一个 goroutine：这样它们的执行节奏
// 是确定的，日志里也一眼能看出「这一轮都干了什么」。
func startBackgroundJobs(
	ctx context.Context,
	db *store.Store,
	sanctionEngine *sanction.Engine,
	engine *rules.Engine,
	eventBus *bus.Bus,
	log *slog.Logger,
	loc *time.Location,
) {
	// 每分钟：解除到期的禁言
	go every(ctx, time.Minute, "解除到期处罚", func(ctx context.Context) error {
		_, err := sanctionEngine.ExpireDue(ctx)
		return err
	})

	// 每小时：清理过期会话、登录记录与超期的命中审计
	go every(ctx, time.Hour, "定期清理", func(ctx context.Context) error {
		if err := db.PruneAuthData(ctx); err != nil {
			return err
		}

		settings, err := db.GetGlobalSettings(ctx)
		if err != nil {
			return err
		}
		if settings.AuditRetentionDays == nil {
			return nil
		}

		removed, err := db.PruneRuleHits(ctx, *settings.AuditRetentionDays)
		if err != nil {
			return err
		}
		if removed > 0 {
			log.Debug("已清理过期命中审计", "removed", removed)
		}
		return nil
	})

	// 每 10 分钟：按各自配置归档闲置话题
	go every(ctx, 10*time.Minute, "自动归档闲置会话", func(ctx context.Context) error {
		return archiveIdleTopics(ctx, db, log, loc)
	})

	// 每 30 秒：推送仪表盘数据。
	//
	// 这是唯一一个「按固定节奏推送」的事件 —— 其余全部由真实业务动作触发，
	// 因为轮询出来的实时感是假的，还会白白占带宽。
	go every(ctx, 30*time.Second, "推送仪表盘数据", func(ctx context.Context) error {
		stats, err := engine.Stats(ctx)
		if err != nil {
			return err
		}
		overview, err := db.ComputeOverview(ctx, loc, store.OverviewRules{
			Total:        int(stats.Total),
			Enabled:      int(stats.Enabled),
			AutoDisabled: int(stats.AutoDisabled),
		})
		if err != nil {
			return err
		}
		eventBus.Publish(bus.ChannelGlobal, "stats.tick", overview)
		return nil
	})
}

// every 是一个通用的定时任务包装。
//
// 立即执行一次再进入周期：面板刚启动时就能看到数据，
// 而不是盯着空白等 30 秒。
func every(ctx context.Context, interval time.Duration, name string, task func(context.Context) error) {
	runOnce := func() {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if err := task(runCtx); err != nil && ctx.Err() == nil {
			// 一个任务失败不该让整个定时器静默失效，记下来继续跑
			slog.Default().Warn("定时任务执行失败", "task", name, "err", err)
		}
	}

	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// archiveIdleTopics 关闭闲置话题。
//
// 阈值是**每机器人**的设置，所以必须先按机器人分组再逐条判断。
// 写成一条统一 SQL 会在不同机器人配置不同时给出错误的答案 ——
// 而这个错误只在有多个机器人时才暴露，开发期很难发现。
func archiveIdleTopics(
	ctx context.Context,
	db *store.Store,
	log *slog.Logger,
	loc *time.Location,
) error {
	_ = loc
	bots, err := db.ListEnabledBots(ctx)
	if err != nil {
		return err
	}

	for _, b := range bots {
		if b.AdminGroupID == nil {
			continue
		}

		settings, err := db.GetBotSettings(ctx, b.ID)
		if err != nil {
			log.Warn("读取机器人设置失败，跳过归档", "botId", b.ID, "err", err)
			continue
		}
		if settings.AutoCloseHours == nil {
			continue
		}

		cutoff := time.Now().Add(-time.Duration(*settings.AutoCloseHours) * time.Hour).UnixMilli()
		stale, err := db.ListIdleTopics(ctx, b.ID, cutoff)
		if err != nil {
			log.Warn("查询闲置话题失败", "botId", b.ID, "err", err)
			continue
		}

		for _, topic := range stale {
			// 直接改状态，不走 Runtime：归档时机器人可能没在运行，
			// 而「数据库里标记为已关闭」是无论如何都该完成的一步。
			if err := db.UpdateTopicStatus(ctx, topic.ID, "closed"); err != nil {
				log.Warn("归档话题失败", "topicId", topic.ID, "err", err)
			}
		}

		if len(stale) > 0 {
			log.Info("已自动归档闲置会话", "botId", b.ID, "count", len(stale))
		}
	}
	return nil
}

// defaultWebDist 是前端构建产物的默认位置。
func defaultWebDist() string {
	// 相对可执行文件定位：部署时前端产物通常与二进制放在一起。
	// 开发期从源码目录跑时，这个相对路径也能指向 packages/web/dist。
	candidates := []string{
		"web-dist",
		"../web/dist",
		"../../packages/web/dist",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return "web-dist"
}
