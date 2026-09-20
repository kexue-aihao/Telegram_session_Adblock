import type { BotHealthStatus } from '@tgs/shared';
import { channel } from '@tgs/shared';
import { apiThrottler } from '@grammyjs/transformer-throttler';
import { run, type RunnerHandle } from '@grammyjs/runner';
import { Bot, type Context } from 'grammy';
import type { Update } from 'grammy/types';
import { eq } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { botOffsets, bots } from '../db/schema.ts';
import { MediaGroupBatcher } from '../core/batcher.ts';
import type { IncomingMessage } from '../core/content.ts';
import { publish } from '../core/bus.ts';
import { openSecret } from '../core/crypto.ts';
import { logger } from '../core/logger.ts';
import { createKeyedRunner } from '../core/queue.ts';
import { getBotSettings } from '../core/settings.ts';
import type { RuntimeDeps } from './deps.ts';
import { handlePrivateMessage, handleStart, processUserMessage } from './pipeline.ts';
import { handleTopicMessage } from './handlers/topicAdmin.ts';
import { handleTopicCallback, handleUserCommand } from './handlers/commands.ts';
import { handleChatMemberUpdated, handleEditedMessage, handleMyChatMember } from './handlers/service.ts';

/**
 * 单个机器人的运行时。
 *
 * 一个 BotRuntime = 一个 grammY Bot + runner + 限速器 + 全部处理器。
 * 生命周期完全由 BotManager 掌控，WebUI 上的「启用/停用/重新加载」
 * 最终都落到这里的 start / stop —— 不需要重启进程。
 */

type BotRow = typeof bots.$inferSelect;

export class BotRuntime {
  readonly id: number;
  readonly bot: Bot;
  private readonly record: BotRow;
  private readonly deps: RuntimeDeps;
  private handle: RunnerHandle | null = null;

  constructor(record: BotRow) {
    this.id = record.id;
    this.record = record;

    const token = openSecret({
      cipher: record.tokenCipher,
      iv: record.tokenIv,
      tag: record.tokenTag,
    });

    this.bot = new Bot(token);

    // 必须装限速器：Bot API 大约是 30 条/秒全局、每群 20 条/分钟，
    // 中继场景（一个用户连发几条就要往话题发几条）极易触发 429。
    // grammY 自带的重试插件会退避重试，但没有限速器时会先把限速配额打爆。
    this.bot.api.config.use(apiThrottler());

    this.deps = {
      botId: record.id,
      botName: record.name,
      botTgId: record.telegramId,
      adminGroupId: record.adminGroupId ?? 0,
      api: this.bot.api,
      getSettings: () => getBotSettings(record.id),
      batcher: new MediaGroupBatcher<IncomingMessage>(400, async (_key, items) => {
        // 缓冲里存的是「收到更新那一刻抽好的快照」，所以这里不需要
        // 再回头找 Telegram 要消息 —— Bot API 根本没有 getMessage，
        // 而消息内容在几百毫秒后也可能已经不可获取（用户撤回、时限过期）。
        await processUserMessage(this.deps, items);
      }),
      flood: new Map(),
      contacts: new Map(),
      muteNotified: new Map(),
      queue: createKeyedRunner(),
    };

    this.installOffsetPersistence();
    this.installRouter();
  }

  /**
   * 长轮询 offset 持久化。
   *
   * runner 自己维护 offset 且不暴露给外部，重启后会从 0 重新拉取 ——
   * 结果是用户上一条消息可能被**再次中继**一次。用 transformer 在
   * getUpdates 前后各拦一次即可：请求时抬高 offset，响应时记下新的。
   */
  private installOffsetPersistence(): void {
    const botId = this.id;
    this.bot.api.config.use(async (prev, method, payload, signal) => {
      if (method !== 'getUpdates') return prev(method, payload, signal);

      // grammY 把 transformer 的 payload 类型标成「所有方法入参的联合」，
      // 无法按 method 收窄。这里的断言是必要的，也是安全的：method 已经
      // 判等过 'getUpdates'，运行时形状与 GetUpdatesParams 一致。
      const params = payload as { offset?: number };

      const saved = await loadOffset(botId);
      const patched: typeof params = { ...params };
      if (saved > 0 && (patched.offset === undefined || patched.offset < saved)) {
        patched.offset = saved;
      }

      // 断言：grammY 的 transformer payload 是「所有方法入参的联合」，
      // TypeScript 无法按 method 收窄。运行时形状与 GetUpdatesParams 一致
      // （上面已经判等过 method），所以这个断言是安全的。
      const result = await prev(method, patched as unknown as typeof payload, signal);
      if (result.ok) {
        const updates = result.result as Update[];
        if (Array.isArray(updates) && updates.length > 0) {
          const maxId = updates.reduce(
            (max: number, update: Update) => Math.max(max, update.update_id),
            0,
          );
          void saveOffset(botId, maxId + 1);
        }
      }
      return result;
    });
  }

  /**
   * 单一入口的更新路由。
   *
   * 没有用 `bot.command()` / `bot.on()` 这类过滤器：它们的匹配顺序由注册
   * 顺序决定，而这里的每种更新恰好只该被一个处理器接住，写成显式分支
   * 才能一眼看出「这条更新会走到哪」。
   */
  private installRouter(): void {
    this.bot.use(async (ctx) => {
      try {
        await this.route(ctx);
      } catch (err) {
        logger.error({ err, botId: this.id, updateId: ctx.update.update_id }, '处理更新失败');
      }
    });

    this.bot.catch((err) => {
      logger.error({ err: err.error, botId: this.id }, '机器人未捕获异常');
    });
  }

  private async route(ctx: Context): Promise<void> {
    if (ctx.callbackQuery) {
      await handleTopicCallback(ctx, this.deps);
      return;
    }
    if (ctx.myChatMember) {
      await handleMyChatMember(ctx, this.deps);
      return;
    }
    if (ctx.chatMember) {
      await handleChatMemberUpdated(ctx, this.deps);
      return;
    }
    if (ctx.editedMessage) {
      await handleEditedMessage(ctx, this.deps);
      return;
    }

    const message = ctx.message;
    if (!message) return;

    if (message.chat.type === 'private') {
      await this.routePrivate(ctx);
      return;
    }

    // 群消息只有在绑定管理群之后才有意义
    if (!this.deps.adminGroupId) return;

    if (message.chat.id === this.deps.adminGroupId) {
      await handleTopicMessage(ctx, this.deps);
      return;
    }

    // 其他群：机器人被拉进了不该在的地方。静默忽略，不回复 ——
    // 回复会在别人的群里刷存在感。
    logger.debug({ botId: this.id, chatId: message.chat.id }, '收到非管理群的消息，已忽略');
  }

  private async routePrivate(ctx: Context): Promise<void> {
    const text = ctx.message?.text;
    const from = ctx.from;
    if (!from) return;

    if (text?.startsWith('/')) {
      const handled = await handleUserCommand(ctx, this.deps);
      if (handled === 'start') {
        await handleStart(ctx, this.deps);
        return;
      }
      if (handled === 'handled') return;
      // 'pass'：不是我们认识的命令，当成普通消息走中继 ——
      // 用户发 "/price" 这类文本是很常见的
    }

    // 同一用户的更新串行：保证消息在话题里的顺序与实际发送顺序一致
    await this.deps.queue.run(`dm:${from.id}`, () => handlePrivateMessage(ctx, this.deps));
  }

  /** 启动长轮询。已在运行时是幂等的 */
  async start(): Promise<void> {
    if (this.handle) return;
    if (!this.record.adminGroupId) {
      logger.warn({ botId: this.id }, '机器人未绑定管理群，仅启用私聊指令');
    }

    await this.setHealth('starting');

    try {
      const me = await this.bot.api.getMe();
      await this.persistIdentity(me.id, me.username ?? this.record.username);

      this.handle = run(this.bot, {
        // 长轮询：自托管面板通常没有公网域名，webhook 需要 PUBLIC_URL 才可用
        runner: {
          fetch: {
            // 只订阅真正会用到的更新类型；默认全订阅会让 Telegram 推来
            // 大量我们根本不处理的更新，白白占用带宽
            allowed_updates: [
              'message',
              'edited_message',
              'callback_query',
              'my_chat_member',
              'chat_member',
            ],
          },
        },
        // 并发上限：中继任务主要是等 Telegram API，8 路足够
        sink: { concurrency: 8 },
      });

      await this.setHealth('online');
      logger.info({ botId: this.id, username: me.username }, '机器人已启动长轮询');
    } catch (err) {
      const message = (err as Error).message ?? String(err);
      await this.setHealth('error', message);
      logger.error({ err, botId: this.id }, '机器人启动失败');
      throw err;
    }
  }

  /** 停止长轮询并等待在途任务收尾 */
  async stop(): Promise<void> {
    if (!this.handle) return;
    const handle = this.handle;
    this.handle = null;

    try {
      // runner.stop() 会等到正在执行的中间件跑完
      await handle.stop();
    } catch (err) {
      logger.warn({ err, botId: this.id }, '停止长轮询时出错');
    }

    await this.deps.batcher.drain();
    await this.deps.queue.drain();
    await this.setHealth('stopped');
    logger.info({ botId: this.id }, '机器人已停止');
  }

  get running(): boolean {
    return this.handle !== null && this.handle.isRunning();
  }

  /** 更新健康状态并广播给面板 */
  async setHealth(status: BotHealthStatus, error?: string): Promise<void> {
    const { db } = getDb();
    const now = Date.now();
    await db
      .update(bots)
      .set({
        healthStatus: status,
        lastError: error ?? null,
        lastPolledAt: status === 'online' ? now : this.record.lastPolledAt,
        updatedAt: now,
      })
      .where(eq(bots.id, this.id));

    const rows = await db.select().from(bots).where(eq(bots.id, this.id)).limit(1);
    const row = rows[0];
    if (row) {
      publish(channel.bot(this.id), 'bot.status', {
        id: row.id,
        name: row.name,
        username: row.username,
        tokenMask: row.tokenMask,
        telegramId: row.telegramId,
        adminGroupId: row.adminGroupId,
        adminGroupTitle: row.adminGroupTitle,
        isEnabled: row.isEnabled,
        healthStatus: row.healthStatus as BotHealthStatus,
        lastError: row.lastError,
        lastPolledAt: row.lastPolledAt,
        createdAt: row.createdAt,
        updatedAt: row.updatedAt,
      });
    }
  }

  private async persistIdentity(telegramId: number, username: string): Promise<void> {
    const { db } = getDb();
    if (telegramId === this.record.telegramId && username === this.record.username) return;
    await db
      .update(bots)
      .set({ telegramId, username, updatedAt: Date.now() })
      .where(eq(bots.id, this.id));
  }

  /** 供 HTTP 层发消息时使用（面板代发） */
  get api() {
    return this.bot.api;
  }

  get botRecord(): BotRow {
    return this.record;
  }
}

async function loadOffset(botId: number): Promise<number> {
  try {
    const { db } = getDb();
    const rows = await db
      .select({ offset: botOffsets.offset })
      .from(botOffsets)
      .where(eq(botOffsets.botId, botId))
      .limit(1);
    return rows[0]?.offset ?? 0;
  } catch (err) {
    logger.debug({ err, botId }, '读取 offset 失败，按 0 处理');
    return 0;
  }
}

async function saveOffset(botId: number, offset: number): Promise<void> {
  try {
    const { db } = getDb();
    await db
      .insert(botOffsets)
      .values({ botId, offset, updatedAt: Date.now() })
      .onConflictDoUpdate({
        target: botOffsets.botId,
        set: { offset, updatedAt: Date.now() },
      });
  } catch (err) {
    logger.debug({ err, botId }, '持久化 offset 失败');
  }
}
