import {
  botDetailSchema,
  botSchema,
  botValidationSchema,
  createBotInputSchema,
  groupCheckSchema,
  updateBotInputSchema,
  type Bot,
  type BotDetail,
  type BotValidation,
  type GroupCheck,
} from '@tgs/shared';
import { Bot as GrammyBot } from 'grammy';
import { and, eq } from 'drizzle-orm';
import type { App } from '../app.ts';
import { getDb } from '../../db/client.ts';
import { adRules, botSettings, bots, topics } from '../../db/schema.ts';
import { recordAudit } from '../../core/audit.ts';
import { maskToken, openSecret, sealSecret } from '../../core/crypto.ts';
import { logger } from '../../core/logger.ts';
import { DEFAULT_BOT_SETTINGS } from '../../db/seed.ts';
import { DEFAULT_ESCALATION } from '@tgs/shared';
import { getBotManager } from '../../bots/manager.ts';
import { invalidateBotSettings, getBotSettings, updateBotSettings } from '../../core/settings.ts';
import { invalidateRules } from '../../rules/engine.ts';
import { parseBody, parseQuery } from '../validate.ts';
import { z } from 'zod';
import { requireAuth } from '../auth.ts';

/**
 * 机器人管理接口。
 *
 * 「测试连接」与「群体检」在这里而不是在 BotRuntime 里，是因为它们要能用
 * **尚未入库**的 token 跑：向导第二步的整个意义就是「先验证，通过了再保存」。
 */

type BotRow = typeof bots.$inferSelect;

export function toBotDto(row: BotRow): Bot {
  return botSchema.parse({
    id: row.id,
    name: row.name,
    username: row.username,
    tokenMask: row.tokenMask,
    telegramId: row.telegramId,
    adminGroupId: row.adminGroupId,
    adminGroupTitle: row.adminGroupTitle,
    isEnabled: row.isEnabled,
    healthStatus: row.healthStatus,
    lastError: row.lastError,
    lastPolledAt: row.lastPolledAt,
    createdAt: row.createdAt,
    updatedAt: row.updatedAt,
  });
}

/**
 * 用 token 调 getMe。
 *
 * 刻意用一次性的 grammY 实例而不是复用运行时：这里要能验证一个
 * 完全陌生、甚至格式都不对的 token，复用运行时会污染它的限速器与重试状态。
 */
export async function validateToken(token: string): Promise<BotValidation> {
  const probe = new GrammyBot(token, {
    // 不要因为一次 getMe 失败就无限重试 —— 这是个同步接口
    client: { timeoutSeconds: 15 },
  });

  try {
    const me = await probe.api.getMe();
    return botValidationSchema.parse({
      ok: true,
      telegramId: me.id,
      name: me.first_name,
      username: me.username ?? null,
      canJoinGroups: me.can_join_groups ?? null,
      // BotFather 里默认开启 privacy mode，此时机器人**读不到**群里的普通消息，
      // 话题中继会完全失效。这是最常见的一个坑，必须明确回传给面板。
      canReadAllGroupMessages: me.can_read_all_group_messages ?? null,
      error: null,
    });
  } catch (err) {
    return botValidationSchema.parse({
      ok: false,
      telegramId: null,
      name: null,
      username: null,
      canJoinGroups: null,
      canReadAllGroupMessages: null,
      error: describeTelegramError(err),
    });
  }
}

/** 把 Telegram 的错误翻译成能指导操作的中文，而不是把英文原文抛给管理员 */
function describeTelegramError(err: unknown): string {
  const message = (err as Error).message ?? String(err);
  if (message.includes('401') || message.includes('Unauthorized')) {
    return 'token 无效或已失效，请到 @BotFather 重新获取';
  }
  if (message.includes('403') || message.includes('Forbidden')) {
    return '机器人被限制，无法调用该接口';
  }
  if (message.includes('timeout') || message.includes('ETIMEDOUT')) {
    return '连接 Telegram 超时 —— 请检查服务器网络能否访问 api.telegram.org';
  }
  return message.slice(0, 300);
}

/** 管理群体检：直接告诉管理员缺哪一项，而不是让他自己猜 */
export async function checkGroup(token: string, chatId: number): Promise<GroupCheck> {
  const probe = new GrammyBot(token, { client: { timeoutSeconds: 15 } });
  const problems: string[] = [];

  try {
    const [chat, me] = await Promise.all([probe.api.getChat(chatId), probe.api.getMe()]);
    const isForum = 'is_forum' in chat ? chat.is_forum === true : false;
    if (!isForum) {
      problems.push('该群没有开启「话题（Topics）」功能：群设置 → 话题 → 开启');
    }

    const member = await probe.api.getChatMember(chatId, me.id);
    const isAdmin = member.status === 'administrator' || member.status === 'creator';
    if (!isAdmin) {
      problems.push('机器人还不是群管理员，请把它提升为管理员');
    }

    const canManageTopics =
      member.status === 'creator' ||
      (member.status === 'administrator' && member.can_manage_topics === true);
    const canDeleteMessages =
      member.status === 'creator' ||
      (member.status === 'administrator' && member.can_delete_messages === true);
    const canRestrictMembers =
      member.status === 'creator' ||
      (member.status === 'administrator' && member.can_restrict_members === true);

    if (!canManageTopics) problems.push('缺少「管理话题」权限，无法为用户创建话题');
    if (!canDeleteMessages) problems.push('缺少「删除消息」权限，命中规则时无法撤回消息');
    if (!canRestrictMembers) {
      // 这一项是可选增强而非必需：终端用户通常不在管理群里，
      // 真正的处罚在机器人层级实现，这一条只影响「恰好是群成员」的用户。
      problems.push('缺少「封禁用户」权限（可选）：仅影响额外叠加 Telegram 原生处罚');
    }

    return groupCheckSchema.parse({
      ok: problems.length === 0,
      chatId,
      title: 'title' in chat ? (chat.title ?? null) : null,
      isForum,
      isAdmin,
      canManageTopics,
      canDeleteMessages,
      canRestrictMembers,
      problems,
    });
  } catch (err) {
    return groupCheckSchema.parse({
      ok: false,
      chatId,
      title: null,
      isForum: false,
      isAdmin: false,
      canManageTopics: false,
      canDeleteMessages: false,
      canRestrictMembers: false,
      problems: [`无法读取该群信息：${describeTelegramError(err)}`],
    });
  }
}

export async function registerBotRoutes(app: App): Promise<void> {

  /** 列表 */
  app.get('/api/bots', { preHandler: requireAuth }, async () => {
    const { db } = getDb();
    const rows = await db.select().from(bots).orderBy(bots.id);
    return { items: rows.map(toBotDto) };
  });

  /** 详情（含设置） */
  app.get('/api/bots/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const { db } = getDb();
    const rows = await db.select().from(bots).where(eq(bots.id, id)).limit(1);
    const row = rows[0];
    if (!row) return reply.code(404).send({ error: '机器人不存在' });

    const detail: BotDetail = { bot: toBotDto(row), settings: await getBotSettings(id) };
    return detail;
  });

  /** 创建：粘贴 token → 校验 → 入库 → 启动 */
  app.post('/api/bots', { preHandler: requireAuth }, async (request, reply) => {
    const input = parseBody(createBotInputSchema, request.body);
    const validation = await validateToken(input.token);
    if (!validation.ok || validation.telegramId === null || validation.username === null) {
      return reply.code(400).send({ error: validation.error ?? 'token 校验失败' });
    }

    const { db } = getDb();
    const existing = await db
      .select({ id: bots.id })
      .from(bots)
      .where(eq(bots.username, validation.username))
      .limit(1);

    if (existing[0]) {
      return reply
        .code(409)
        .send({ error: `机器人 @${validation.username} 已经添加过了（id=${existing[0].id}）` });
    }

    const sealed = sealSecret(input.token);
    const now = Date.now();

    let groupTitle: string | null = null;
    if (input.adminGroupId !== null) {
      const check = await checkGroup(input.token, input.adminGroupId);
      groupTitle = check.title;
    }

    const inserted = await db
      .insert(bots)
      .values({
        name: input.name ?? validation.name ?? validation.username,
        username: validation.username,
        tokenCipher: sealed.cipher,
        tokenIv: sealed.iv,
        tokenTag: sealed.tag,
        tokenMask: maskToken(input.token),
        telegramId: validation.telegramId,
        adminGroupId: input.adminGroupId,
        adminGroupTitle: groupTitle,
        isEnabled: true,
        healthStatus: 'unknown',
        lastError: null,
        lastPolledAt: null,
        createdAt: now,
        updatedAt: now,
      })
      .returning();

    const row = inserted[0];
    if (!row) return reply.code(500).send({ error: '写入机器人失败' });

    await db
      .insert(botSettings)
      .values({ botId: row.id, ...DEFAULT_BOT_SETTINGS, escalation: DEFAULT_ESCALATION })
      .onConflictDoNothing();

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'bot.created',
      targetType: 'bot',
      targetId: row.id,
      detail: { username: row.username },
      ip: request.ip,
    });

    // 立即启动，管理员不用手动点一次「启用」
    if (row.adminGroupId !== null) {
      try {
        await getBotManager().start(row.id);
      } catch (err) {
        logger.warn({ err, botId: row.id }, '机器人创建成功但启动失败');
      }
    }

    return reply.code(201).send({
      bot: toBotDto(row),
      validation,
      /** 把体检查出的问题一并回给向导，避免管理员「加完了才发现少权限」 */
      groupCheck: input.adminGroupId !== null ? await checkGroup(input.token, input.adminGroupId) : null,
    });
  });

  /** 更新：改名 / 换管理群 / 换 token / 启停 */
  app.patch('/api/bots/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const input = parseBody(updateBotInputSchema, request.body);
    const { db } = getDb();

    const rows = await db.select().from(bots).where(eq(bots.id, id)).limit(1);
    const row = rows[0];
    if (!row) return reply.code(404).send({ error: '机器人不存在' });

    const patch: Partial<BotRow> = { updatedAt: Date.now() };
    let needsRestart = false;

    if (input.name !== undefined) patch.name = input.name;

    if (input.adminGroupId !== undefined && input.adminGroupId !== row.adminGroupId) {
      patch.adminGroupId = input.adminGroupId;
      // 顺带把群名问出来，缺权限也不该阻断保存
      try {
        const token = openSecret({ cipher: row.tokenCipher, iv: row.tokenIv, tag: row.tokenTag });
        const check = await checkGroup(token, input.adminGroupId ?? 0);
        patch.adminGroupTitle = check.title;
        if (!check.ok) {
          logger.warn({ botId: id, problems: check.problems }, '绑定的管理群体检未通过');
        }
      } catch (err) {
        logger.warn({ err, botId: id }, '读取管理群信息失败');
        patch.adminGroupTitle = null;
      }
      needsRestart = true;
    }

    if (input.token !== undefined) {
      const validation = await validateToken(input.token);
      if (!validation.ok) return reply.code(400).send({ error: validation.error ?? 'token 校验失败' });
      const sealed = sealSecret(input.token);
      patch.tokenCipher = sealed.cipher;
      patch.tokenIv = sealed.iv;
      patch.tokenTag = sealed.tag;
      patch.tokenMask = maskToken(input.token);
      patch.telegramId = validation.telegramId;
      needsRestart = true;
    }

    if (input.isEnabled !== undefined) {
      patch.isEnabled = input.isEnabled;
      needsRestart = true;
      await recordAudit({
        actorType: 'admin',
        actorId: request.auth?.username ?? null,
        action: input.isEnabled ? 'bot.enabled' : 'bot.disabled',
        targetType: 'bot',
        targetId: id,
        ip: request.ip,
      });
    }

    await db.update(bots).set(patch).where(eq(bots.id, id));

    if (needsRestart) {
      const manager = getBotManager();
      try {
        if (patch.isEnabled === false) {
          await manager.stop(id);
        } else {
          await manager.start(id);
        }
      } catch (err) {
        logger.warn({ err, botId: id }, '机器人重启失败，状态已更新');
      }
    }

    const updated = (await db.select().from(bots).where(eq(bots.id, id)).limit(1))[0];
    if (!updated) return reply.code(404).send({ error: '机器人不存在' });
    return { bot: toBotDto(updated) };
  });

  /** 删除：连带清掉它的规则、话题与全部中继记录（外键 cascade） */
  app.delete('/api/bots/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const { db } = getDb();

    await getBotManager().stop(id);

    const rows = await db.select({ username: bots.username }).from(bots).where(eq(bots.id, id)).limit(1);
    if (!rows[0]) return reply.code(404).send({ error: '机器人不存在' });

    await db.delete(bots).where(eq(bots.id, id));

    // 专属规则用外键 cascade 清掉了，但规则缓存还留着旧的行
    invalidateBotSettings(id);
    invalidateRules();

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'bot.deleted',
      targetType: 'bot',
      targetId: id,
      detail: { username: rows[0].username },
      ip: request.ip,
    });

    return { ok: true };
  });

  /** 手动重载：换过 MASTER_KEY、或怀疑轮询卡住时用 */
  app.post('/api/bots/:id/reload', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    try {
      await getBotManager().reload(id);
    } catch (err) {
      return reply.code(400).send({ error: (err as Error).message });
    }

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'bot.reloaded',
      targetType: 'bot',
      targetId: id,
      ip: request.ip,
    });

    return { ok: true };
  });

  /** 用已入库的 token 复检管理群 —— 面板上的「重新体检」按钮 */
  app.post('/api/bots/:id/check-group', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const { db } = getDb();
    const rows = await db.select().from(bots).where(eq(bots.id, id)).limit(1);
    const row = rows[0];
    if (!row) return reply.code(404).send({ error: '机器人不存在' });
    if (row.adminGroupId === null) {
      return reply.code(400).send({ error: '尚未绑定管理群' });
    }

    try {
      const token = openSecret({ cipher: row.tokenCipher, iv: row.tokenIv, tag: row.tokenTag });
      const check = await checkGroup(token, row.adminGroupId);
      if (check.title !== row.adminGroupTitle) {
        await db.update(bots).set({ adminGroupTitle: check.title }).where(eq(bots.id, id));
      }
      return check;
    } catch (err) {
      return reply.code(400).send({ error: (err as Error).message });
    }
  });

  /** 只校验 token，不入库 —— 向导第一步 */
  app.post('/api/bots/validate', { preHandler: requireAuth }, async (request) => {
    const input = parseBody(
      z.object({ token: z.string().trim().min(10).max(200) }),
      request.body,
    );
    return validateToken(input.token);
  });

  /** 校验管理群，不需要 token（用临时 token 或已入库的） */
  app.post('/api/bots/check-group', { preHandler: requireAuth }, async (request) => {
    const input = parseBody(
      z.object({
        token: z.string().trim().min(10).max(200),
        chatId: z.number().int().min(-1_000_000_000_000).max(-1),
      }),
      request.body,
    );
    return checkGroup(input.token, input.chatId);
  });

  /** 读取/更新机器人设置 */
  app.get('/api/bots/:id/settings', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const { db } = getDb();
    const rows = await db.select({ id: bots.id }).from(bots).where(eq(bots.id, id)).limit(1);
    if (!rows[0]) return reply.code(404).send({ error: '机器人不存在' });
    return getBotSettings(id);
  });

  app.patch('/api/bots/:id/settings', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const patch = (request.body ?? {}) as Record<string, unknown>;

    try {
      const next = await updateBotSettings(id, patch);
      await recordAudit({
        actorType: 'admin',
        actorId: request.auth?.username ?? null,
        action: 'settings.updated',
        targetType: 'bot',
        targetId: id,
        detail: { keys: Object.keys(patch) },
        ip: request.ip,
      });
      return next;
    } catch (err) {
      return reply.code(400).send({ error: (err as Error).message });
    }
  });

  /** 某个机器人下的话题数量，用于删除前的二次确认文案 */
  app.get('/api/bots/:id/impact', { preHandler: requireAuth }, async (request) => {
    const { id } = parseQuery(z.object({ id: z.coerce.number().int().positive() }), request.params);
    const { db } = getDb();
    const rows = await db
      .select({ id: topics.id })
      .from(topics)
      .where(and(eq(topics.botId, id)));
    const rules = await db.select({ id: adRules.id }).from(adRules).where(eq(adRules.botId, id));
    return { topics: rows.length, rules: rules.length };
  });
}
