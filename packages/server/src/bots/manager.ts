import { BOT_HEALTH_STATUSES } from '@tgs/shared';
import { eq } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { bots } from '../db/schema.ts';
import { logger } from '../core/logger.ts';
import { openSecret } from '../core/crypto.ts';
import { BotRuntime } from './instance.ts';

/**
 * 全部机器人的生命周期。
 *
 * 面板上的「启用 / 停用 / 重新加载」最终都落到这里 —— 增删机器人与改 token
 * 都不需要重启进程，这是面板「热重载」承诺的全部内容。
 */

type BotRow = typeof bots.$inferSelect;

export class BotManager {
  private readonly runtimes = new Map<number, BotRuntime>();
  private stopped = false;

  /** 启动所有已启用的机器人。单个失败不影响其余 —— 一个坏 token 不该让全站瘫痪 */
  async startAll(): Promise<void> {
    const { db } = getDb();
    const rows = await db.select().from(bots).where(eq(bots.isEnabled, true));

    for (const row of rows) {
      try {
        await this.start(row.id);
      } catch (err) {
        logger.error({ err, botId: row.id }, '机器人启动失败，已跳过');
      }
    }

    logger.info({ started: this.runtimes.size, total: rows.length }, '机器人启动完成');
  }

  /** 启动单个机器人；已在运行时先停掉再起（等价于 reload） */
  async start(botId: number): Promise<void> {
    if (this.stopped) {
      logger.warn({ botId }, '管理器正在关闭，忽略启动请求');
      return;
    }

    await this.stop(botId);

    const { db } = getDb();
    const rows = await db.select().from(bots).where(eq(bots.id, botId)).limit(1);
    const row = rows[0];
    if (!row) throw new Error(`机器人不存在：${botId}`);
    if (!row.isEnabled) {
      logger.info({ botId }, '机器人已停用，不启动');
      return;
    }

    const runtime = new BotRuntime(row);
    this.runtimes.set(botId, runtime);

    try {
      await runtime.start();
    } catch (err) {
      // 保留在 map 里而不是删掉：面板要能看到「这个机器人存在但起不来」，
      // 以及 lastError 里的具体原因。删掉会让它在界面上凭空消失。
      logger.error({ err, botId }, '机器人启动失败');
      throw err;
    }
  }

  /** 停止单个机器人并从内存中移除运行时 */
  async stop(botId: number): Promise<void> {
    const runtime = this.runtimes.get(botId);
    if (!runtime) return;
    this.runtimes.delete(botId);
    await runtime.stop();
  }

  /**
   * 重新加载：停掉再起。
   * 更换 token、修改管理群、改动需要重建 Bot 实例的配置后调用。
   */
  async reload(botId: number): Promise<void> {
    logger.info({ botId }, '重新加载机器人');
    await this.start(botId);
  }

  /** 优雅关闭：先停轮询，等在途任务收尾 */
  async stopAll(): Promise<void> {
    this.stopped = true;
    const ids = [...this.runtimes.keys()];
    await Promise.all(ids.map((id) => this.stop(id)));
    logger.info({ stopped: ids.length }, '全部机器人已停止');
  }

  get(botId: number): BotRuntime | undefined {
    return this.runtimes.get(botId);
  }

  /** 取运行时；不存在时抛出带可执行提示的错误，供 HTTP 层直接转成 400 */
  require(botId: number): BotRuntime {
    const runtime = this.runtimes.get(botId);
    if (!runtime) {
      throw new Error(`机器人 ${botId} 当前未运行，请在面板中启用它`);
    }
    return runtime;
  }

  list(): BotRuntime[] {
    return [...this.runtimes.values()];
  }

  get size(): number {
    return this.runtimes.size;
  }

  onlineCount(): number {
    return this.list().filter((runtime) => runtime.running).length;
  }
}

/**
 * 从库里读出 token 明文（仅用于 `validate` 接口与启动前预检）。
 *
 * 单独放在这里而不是 manager 的方法上：它可能抛「MASTER_KEY 不匹配」，
 * 而那种错误需要在 HTTP 层被翻译成「请重新录入 token」的提示，
 * 不适合混在启动流程里。
 */
export function decryptToken(row: BotRow): string {
  return openSecret({ cipher: row.tokenCipher, iv: row.tokenIv, tag: row.tokenTag });
}

/** 健康状态文案，供面板与日志共用 */
export const HEALTH_LABELS: Record<(typeof BOT_HEALTH_STATUSES)[number], string> = {
  unknown: '未知',
  starting: '启动中',
  online: '在线',
  error: '异常',
  stopped: '已停止',
};

let instance: BotManager | null = null;

export function getBotManager(): BotManager {
  instance ??= new BotManager();
  return instance;
}
