import { channel } from '@tgs/shared';
import { and, eq, isNotNull, lt } from 'drizzle-orm';
import { closeTopic, findTopicById } from './bots/topics.ts';
import { getBotManager } from './bots/manager.ts';
import { publish } from './core/bus.ts';
import { logger } from './core/logger.ts';
import { getBotSettings, getGlobalSettings } from './core/settings.ts';
import { connectDb, getDb } from './db/client.ts';
import { runMigrations } from './db/migrate.ts';
import { seed } from './db/seed.ts';
import { bots, ruleHits, topics } from './db/schema.ts';
import { loadEnv } from './env.ts';
import { pruneAuthData } from './http/auth.ts';
import { buildServer } from './http/server.ts';
import { computeOverview } from './overview.ts';
import { expireDueSanctions } from './sanctions.ts';

/**
 * 启动编排。
 *
 * 顺序是有依赖的：环境变量 → 数据库 → 迁移 → 播种 → 机器人 → HTTP。
 * 任何一步失败都必须让进程**立刻退出**并打印原因，而不是带着半截状态
 * 继续跑 —— 一个没迁移过的库配上正在轮询的机器人，会把数据写成一团乱麻。
 */

const env = loadEnv();

async function main(): Promise<void> {
  logger.info(
    { env: env.NODE_ENV, host: env.HOST, port: env.PORT },
    '正在启动 Telegram 会话中继服务',
  );

  await connectDb();
  await runMigrations();
  await seed();

  // 单个机器人启动失败不该让整个服务起不来：面板本身必须可用，
  // 否则管理员连「去修那个坏 token」的入口都没有。
  await getBotManager().startAll();

  const { app } = await buildServer();
  await app.listen({ host: env.HOST, port: env.PORT });

  logger.info(
    { url: `http://${env.HOST === '0.0.0.0' ? 'localhost' : env.HOST}:${env.PORT}` },
    '服务已就绪',
  );

  startBackgroundJobs();
  installShutdownHooks();
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

function startBackgroundJobs(): void {
  // 每分钟：解除到期的禁言
  scheduleEvery(MINUTE, '解除到期处罚', async () => {
    await expireDueSanctions();
  });

  // 每小时：清理过期会话与登录记录，并按配置清理过期的命中审计
  scheduleEvery(HOUR, '定期清理', async () => {
    await pruneAuthData();

    const settings = await getGlobalSettings();
    if (settings.auditRetentionDays === null) return;

    const { db } = getDb();
    const cutoff = Date.now() - settings.auditRetentionDays * 86_400_000;
    await db.delete(ruleHits).where(lt(ruleHits.createdAt, cutoff));
  });

  // 每 10 分钟：按各自配置归档闲置话题
  scheduleEvery(10 * MINUTE, '自动归档闲置会话', async () => {
    await archiveIdleTopics();
  });

  /**
   * 每 30 秒推一次仪表盘数据。
   * 这是唯一一个「按固定节奏推送」的事件 —— 其余全部由真实业务动作触发，
   * 因为轮询出来的实时感是假的，还会白白占带宽。
   */
  scheduleEvery(30_000, '推送仪表盘数据', async () => {
    publish(channel.global, 'stats.tick', await computeOverview());
  });
}

/** 统一的定时任务包装：吞掉异常、记录耗时，避免一个任务挂掉后整个定时器静默失效 */
function scheduleEvery(intervalMs: number, label: string, task: () => Promise<void>): void {
  const timer = setInterval(() => {
    void task().catch((err: unknown) => {
      logger.warn({ err, task: label }, '定时任务执行失败');
    });
  }, intervalMs);
  // 定时器不该拖住进程退出 —— 否则 Ctrl-C 之后要等下一个整点才结束
  timer.unref?.();
}

/**
 * 关闭闲置话题。
 *
 * 阈值是**每机器人**的设置，所以必须先按机器人分组再逐条判断。
 * 写成一条统一 SQL 会在不同机器人配置不同时给出错误的答案 ——
 * 而这个错误只在有多个机器人时才暴露，开发期很难发现。
 */
async function archiveIdleTopics(): Promise<void> {
  const { db } = getDb();
  const botRows = await db
    .select({ id: bots.id, adminGroupId: bots.adminGroupId })
    .from(bots)
    .where(eq(bots.isEnabled, true));

  const manager = getBotManager();

  for (const bot of botRows) {
    const settings = await getBotSettings(bot.id);
    if (settings.autoCloseHours === null) continue;

    const runtime = manager.get(bot.id);
    if (!runtime || !bot.adminGroupId) continue;

    const cutoff = Date.now() - settings.autoCloseHours * HOUR;
    const stale = await db
      .select({ id: topics.id })
      .from(topics)
      .where(
        and(
          eq(topics.botId, bot.id),
          eq(topics.status, 'open'),
          // 从未有过消息的话题不归档：它们通常是刚刚由 /start 建出来的，
          // lastMessageAt 还是 null，此刻归档等于让新用户一进来就发现门是关的
          isNotNull(topics.lastMessageAt),
          lt(topics.lastMessageAt, cutoff),
        ),
      );

    for (const row of stale) {
      const topic = await findTopicById(row.id);
      if (!topic) continue;
      await closeTopic(runtime.api, bot.adminGroupId, topic);
    }

    if (stale.length > 0) {
      logger.info({ botId: bot.id, count: stale.length }, '已自动归档闲置会话');
    }
  }
}

function installShutdownHooks(): void {
  let shuttingDown = false;

  const shutdown = async (signal: string): Promise<void> => {
    // 二次 Ctrl-C 不要再走一遍流程 —— 卡住的关闭过程会让人以为进程已经死掉
    if (shuttingDown) {
      logger.warn('再次收到退出信号，强制结束');
      process.exit(1);
    }
    shuttingDown = true;
    logger.info({ signal }, '正在关闭服务');

    try {
      // 先停机器人：等在途的中继任务收尾，避免关库后还有写入进来
      await getBotManager().stopAll();
      await getDb().close();
      logger.info('已安全退出');
      process.exit(0);
    } catch (err) {
      logger.error({ err }, '关闭过程中出错');
      process.exit(1);
    }
  };

  process.on('SIGINT', () => void shutdown('SIGINT'));
  process.on('SIGTERM', () => void shutdown('SIGTERM'));

  process.on('unhandledRejection', (reason) => {
    // 未处理的 rejection 是 bug，但不该直接杀掉正在中继用户消息的进程。
    // 记下来让它暴露出来，由人来决定怎么处理。
    logger.error({ reason }, '未处理的 Promise 拒绝');
  });

  process.on('uncaughtException', (err) => {
    // 与上面不同：同步异常已经打断了调用栈，继续跑下去状态不可信
    logger.fatal({ err }, '未捕获异常，进程即将退出');
    process.exit(1);
  });
}

main().catch((err: unknown) => {
  logger.fatal({ err }, '启动失败');
  process.exit(1);
});
