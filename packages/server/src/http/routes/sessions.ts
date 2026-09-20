import {
  messagePageQuerySchema,
  sendMessageInputSchema,
  sessionQuerySchema,
  updateContactInputSchema,
} from '@tgs/shared';
import { eq } from 'drizzle-orm';
import type { App } from '../app.ts';
import { z } from 'zod';
import { getDb } from '../../db/client.ts';
import { contacts, topics } from '../../db/schema.ts';
import { recordAudit } from '../../core/audit.ts';
import { logger } from '../../core/logger.ts';
import { bumpStats } from '../../core/stats.ts';
import { resetViolations } from '../../sanctions.ts';
import { getBotManager } from '../../bots/manager.ts';
import { recordAdminOutgoing } from '../../bots/relay.ts';
import {
  closeTopic,
  findContactById,
  findTopicById,
  forwardTopicDelete,
  reopenTopic,
  sendToUser,
} from '../../bots/topics.ts';
import { getSessionDetail, getSessionSummary, listMessages, listSessions } from '../../sessions.ts';
import { parseBody, parseQuery, HttpError } from '../validate.ts';
import { channel } from '@tgs/shared';
import { publish } from '../../core/bus.ts';
import { requireAuth } from '../auth.ts';

/**
 * 会话（话题）与终端用户的操作接口。
 *
 * 面板在这里扮演的是「管理员的另一个客户端」：所有动作与在 Telegram 话题里
 * 点按钮完全等价，走的是同一套 bots/topics 函数，因此不会出现
 * 「面板上关闭了，Telegram 里还开着」这种状态分叉。
 */

const idParam = z.object({ id: z.coerce.number().int().positive() });

export async function registerSessionRoutes(app: App): Promise<void> {

  /** 会话列表 */
  app.get('/api/sessions', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(sessionQuerySchema, request.query);
    return listSessions(query);
  });

  /** 会话详情：包含生效中的处罚与近期命中 */
  app.get('/api/sessions/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const detail = await getSessionDetail(id);
    if (!detail) return reply.code(404).send({ error: '会话不存在' });
    return detail;
  });

  /** 分页拉取消息（倒序，前端反转后渲染） */
  app.get('/api/sessions/:id/messages', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const query = parseQuery(messagePageQuerySchema, request.query);

    const topic = await findTopicById(id);
    if (!topic) return reply.code(404).send({ error: '会话不存在' });

    return listMessages(id, query);
  });

  /**
   * 以管理员身份发消息 —— 面板里直接回复用户。
   *
   * 落库是**先于**发送的：面板的聊天视图要立刻出现这条消息（乐观渲染），
   * 而如果发送失败，那条记录会被标记出来而不是凭空消失。
   */
  app.post('/api/sessions/:id/messages', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const input = parseBody(sendMessageInputSchema, request.body);

    const topic = await findTopicById(id);
    if (!topic) return reply.code(404).send({ error: '会话不存在' });

    const contact = await findContactById(topic.contactId);
    if (!contact) return reply.code(404).send({ error: '联系人不存在' });

    if (contact.isBlocked) {
      return reply.code(400).send({ error: '该用户已被拉黑，请先解除拉黑再发送' });
    }

    let runtime;
    try {
      runtime = getBotManager().require(topic.botId);
    } catch (err) {
      return reply.code(400).send({ error: (err as Error).message });
    }

    const sent = await sendToUser(runtime.api, topic.botId, contact.tgUserId, input.text);
    if (sent === null) {
      return reply.code(502).send({ error: '发送失败：对方可能已屏蔽机器人' });
    }

    const row = await recordAdminOutgoing({
      botId: topic.botId,
      topicId: topic.id,
      contactTgId: contact.tgUserId,
      botTgId: runtime.botRecord.telegramId ?? 0,
      text: input.text,
    });

    await bumpStats(topic.botId, { messagesOut: 1 });

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'message.sent',
      targetType: 'topic',
      targetId: id,
      detail: { length: input.text.length },
      ip: request.ip,
    });

    const summary = await getSessionSummary(topic.id);
    if (summary) publish(channel.bot(topic.botId), 'session.updated', summary);

    return { ok: true, messageId: row.id };
  });

  app.post('/api/sessions/:id/close', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const topic = await findTopicById(id);
    if (!topic) return reply.code(404).send({ error: '会话不存在' });

    await closeTopic(getBotManager().require(topic.botId).api, requireGroup(topic.botId), topic);
    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'session.closed',
      targetType: 'topic',
      targetId: id,
      ip: request.ip,
    });

    const summary = await getSessionSummary(id);
    if (summary) publish(channel.bot(topic.botId), 'session.updated', summary);
    return { ok: true };
  });

  app.post('/api/sessions/:id/reopen', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const topic = await findTopicById(id);
    if (!topic) return reply.code(404).send({ error: '会话不存在' });

    await reopenTopic(getBotManager().require(topic.botId).api, requireGroup(topic.botId), topic);
    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'session.reopened',
      targetType: 'topic',
      targetId: id,
      ip: request.ip,
    });

    const summary = await getSessionSummary(id);
    if (summary) publish(channel.bot(topic.botId), 'session.updated', summary);
    return { ok: true };
  });

  /**
   * 删除会话：真的在 Telegram 里删掉话题，并把本地记录标记为 deleted。
   *
   * 与「关闭」的区别很关键：关闭只是让用户发不进来，删除会连历史一起消失。
   * 因此面板上这个操作需要二次确认。
   */
  app.delete('/api/sessions/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const topic = await findTopicById(id);
    if (!topic) return reply.code(404).send({ error: '会话不存在' });

    const ok = await forwardTopicDelete(
      getBotManager().require(topic.botId).api,
      requireGroup(topic.botId),
      topic,
    );
    if (!ok) {
      logger.warn({ topicId: id }, 'Telegram 侧删除话题失败，仍标记为已删除');
    }

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'session.deleted',
      targetType: 'topic',
      targetId: id,
      ip: request.ip,
    });

    publish(channel.bot(topic.botId), 'session.deleted', { sessionId: id, botId: topic.botId });
    return { ok: true };
  });

  // ────────────────────────── 终端用户操作 ──────────────────────────

  app.post('/api/contacts/:id/ban', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const contact = await findContactById(id);
    if (!contact) return reply.code(404).send({ error: '联系人不存在' });

    const { db } = getDb();
    await db.update(contacts).set({ isBlocked: true }).where(eq(contacts.id, id));

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'contact.banned',
      targetType: 'contact',
      targetId: id,
      ip: request.ip,
    });

    await refreshContactSessions(contact.id);
    return { ok: true };
  });

  app.post('/api/contacts/:id/unban', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const contact = await findContactById(id);
    if (!contact) return reply.code(404).send({ error: '联系人不存在' });

    const { db } = getDb();
    await db
      .update(contacts)
      .set({ isBlocked: false, isUnreachable: false })
      .where(eq(contacts.id, id));

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'contact.unbanned',
      targetType: 'contact',
      targetId: id,
      ip: request.ip,
    });

    await refreshContactSessions(contact.id);
    return { ok: true };
  });

  app.post('/api/contacts/:id/reset-violations', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const contact = await findContactById(id);
    if (!contact) return reply.code(404).send({ error: '联系人不存在' });

    await resetViolations(id);

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'contact.violations_reset',
      targetType: 'contact',
      targetId: id,
      ip: request.ip,
    });

    await refreshContactSessions(id);
    return { ok: true };
  });

  app.patch('/api/contacts/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const input = parseBody(updateContactInputSchema, request.body);

    const contact = await findContactById(id);
    if (!contact) return reply.code(404).send({ error: '联系人不存在' });

    if (input.notes !== undefined) {
      const { db } = getDb();
      await db.update(contacts).set({ notes: input.notes }).where(eq(contacts.id, id));
      await recordAudit({
        actorType: 'admin',
        actorId: request.auth?.username ?? null,
        action: 'contact.notes_updated',
        targetType: 'contact',
        targetId: id,
        ip: request.ip,
      });
    }

    return { ok: true };
  });
}

/** 取机器人绑定的管理群 id；未绑定时报一个可执行的错误 */
function requireGroup(botId: number): number {
  const runtime = getBotManager().get(botId);
  const groupId = runtime?.botRecord.adminGroupId ?? null;
  if (groupId === null) {
    throw new HttpError(400, '该机器人尚未绑定管理群');
  }
  return groupId;
}

/** 联系人状态变了，它名下的会话摘要也要跟着刷新 */
async function refreshContactSessions(contactId: number): Promise<void> {
  const { db } = getDb();
  const rows = await db
    .select({ id: topics.id, botId: topics.botId })
    .from(topics)
    .where(eq(topics.contactId, contactId));

  for (const row of rows) {
    const summary = await getSessionSummary(row.id);
    if (summary) publish(channel.bot(row.botId), 'session.updated', summary);
  }
}
