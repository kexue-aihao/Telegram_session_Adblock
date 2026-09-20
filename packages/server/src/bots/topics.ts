import {
  FORUM_TOPIC_ICON_COLORS,
  type BotSettings,
  type TopicStatus,
} from '@tgs/shared';
import type { Api } from 'grammy';
import { InlineKeyboard } from 'grammy';
import { and, eq } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { contacts, topics } from '../db/schema.ts';
import { logger } from '../core/logger.ts';
import { renderTemplate } from '../core/template.ts';
import { escapeMarkdown } from '../core/text.ts';

/**
 * 话题（一人一会话）的生命周期管理。
 *
 * 这个模块的每个函数都假设调用方已经确认过「管理群配置正确」——
 * 见 `bots/manager.ts` 的启动前体检。在这里重复校验会让中继热路径
 * 多出几次无谓的分支。
 */

export type ContactRow = typeof contacts.$inferSelect;
export type TopicRow = typeof topics.$inferSelect;

/** 话题头部消息上的快捷操作按钮，回调数据统一前缀 `tgs:` 避免与其他 bot 冲突 */
export const CALLBACK_PREFIX = 'tgs';

export function topicCallback(action: string, topicId: number): string {
  return `${CALLBACK_PREFIX}:${action}:${topicId}`;
}

export interface ParsedCallback {
  action: string;
  topicId: number;
}

export function parseTopicCallback(data: string): ParsedCallback | null {
  if (!data.startsWith(`${CALLBACK_PREFIX}:`)) return null;
  const [, action, rawId] = data.split(':');
  if (!action || !rawId) return null;
  const topicId = Number.parseInt(rawId, 10);
  if (!Number.isFinite(topicId)) return null;
  return { action, topicId };
}

/** 取（不存在则创建）联系人档案。每次收到消息都会调用，所以只做必要的读写 */
export async function ensureContact(
  botId: number,
  user: {
    id: number;
    username?: string | undefined;
    first_name: string;
    last_name?: string | undefined;
    language_code?: string | undefined;
  },
): Promise<ContactRow> {
  const { db } = getDb();
  const now = Date.now();

  const existing = await db
    .select()
    .from(contacts)
    .where(and(eq(contacts.botId, botId), eq(contacts.tgUserId, user.id)))
    .limit(1);

  const found = existing[0];
  if (found) {
    // 昵称/用户名会变，每次消息都同步一次；但只在真的变了才写库，
    // 否则每条消息都产生一次无意义的 UPDATE 与 WAL 写入。
    const changed =
      found.username !== (user.username ?? null) ||
      found.firstName !== user.first_name ||
      found.lastName !== (user.last_name ?? null);

    if (changed) {
      await db
        .update(contacts)
        .set({
          username: user.username ?? null,
          firstName: user.first_name,
          lastName: user.last_name ?? null,
          languageCode: user.language_code ?? found.languageCode,
          lastSeenAt: now,
        })
        .where(eq(contacts.id, found.id));
    } else {
      await db.update(contacts).set({ lastSeenAt: now }).where(eq(contacts.id, found.id));
    }

    return {
      ...found,
      username: user.username ?? null,
      firstName: user.first_name,
      lastName: user.last_name ?? null,
      lastSeenAt: now,
    };
  }

  const inserted = await db
    .insert(contacts)
    .values({
      botId,
      tgUserId: user.id,
      username: user.username ?? null,
      firstName: user.first_name,
      lastName: user.last_name ?? null,
      languageCode: user.language_code ?? null,
      isBlocked: false,
      isUnreachable: false,
      violationScore: 0,
      lastViolationAt: null,
      firstSeenAt: now,
      lastSeenAt: now,
      notes: null,
    })
    .returning();

  const row = inserted[0];
  if (!row) throw new Error('创建联系人档案失败');
  return row;
}

/** 按 `message_thread_id` 反查话题 —— 管理员回复路径的第一跳 */
export async function findTopicByThread(botId: number, threadId: number): Promise<TopicRow | null> {
  const { db } = getDb();
  const rows = await db
    .select()
    .from(topics)
    .where(and(eq(topics.botId, botId), eq(topics.messageThreadId, threadId)))
    .limit(1);
  return rows[0] ?? null;
}

export async function findTopicById(topicId: number): Promise<TopicRow | null> {
  const { db } = getDb();
  const rows = await db.select().from(topics).where(eq(topics.id, topicId)).limit(1);
  return rows[0] ?? null;
}

export async function findContactById(contactId: number): Promise<ContactRow | null> {
  const { db } = getDb();
  const rows = await db.select().from(contacts).where(eq(contacts.id, contactId)).limit(1);
  return rows[0] ?? null;
}

export async function findTopicByContact(botId: number, contactId: number): Promise<TopicRow | null> {
  const { db } = getDb();
  const rows = await db
    .select()
    .from(topics)
    .where(and(eq(topics.botId, botId), eq(topics.contactId, contactId)))
    .limit(1);
  return rows[0] ?? null;
}

/**
 * grammY 把 `icon_color` 声明成六个字面量的联合类型，而不是 `number`。
 * 库里的配置是 number（面板可以填任意整数），所以这里收窄一次；
 * 不在白名单里就回落到第一个颜色，而不是让创建话题直接失败。
 */
export function resolveIconColor(value: number): (typeof FORUM_TOPIC_ICON_COLORS)[number] {
  return (FORUM_TOPIC_ICON_COLORS as readonly number[]).includes(value)
    ? (value as (typeof FORUM_TOPIC_ICON_COLORS)[number])
    : FORUM_TOPIC_ICON_COLORS[0];
}

/** 话题标题：按模板渲染，并裁剪到 Telegram 的 128 字符上限 */
export function buildTopicTitle(settings: BotSettings, contact: ContactRow, botName: string): string {
  const rendered = renderTemplate(settings.topicNameTemplate, {
    name: contact.firstName ?? contact.username ?? '未知用户',
    username: contact.username ? `@${contact.username}` : '',
    id: contact.tgUserId,
    firstName: contact.firstName,
    lastName: contact.lastName,
    botName,
  });
  // 模板本身不含 Markdown 语义，所以这里把转义还原成普通字符 ——
  // 话题标题不接受 Markdown，带反斜杠反而会让管理员看到 `\_`
  const plain = rendered.replace(/\\([_*`[\]])/g, '$1');
  return plain.slice(0, 128) || `用户 ${contact.tgUserId}`;
}

/**
 * 头部置顶消息的内联键盘。
 *
 * 「关闭话题 / 拉黑 / 重置违规分」这三个动作是管理员在话题里最常用的，
 * 放在置顶消息上比记命令更现实 —— 管理员未必记得住 `/reset` 这类命令，
 * 但一定点得到按钮。
 */
export function headerKeyboard(topicId: number, contact: ContactRow, status: TopicStatus): InlineKeyboard {
  const kb = new InlineKeyboard();

  if (status === 'open') {
    kb.text('🔒 关闭话题', topicCallback('close', topicId));
  } else {
    kb.text('🔓 重新打开', topicCallback('reopen', topicId));
  }

  if (contact.isBlocked) {
    kb.text('♻️ 解除拉黑', topicCallback('unban', topicId));
  } else {
    kb.text('🚫 拉黑', topicCallback('ban', topicId));
  }

  kb.row();
  kb.text('🧹 重置违规分', topicCallback('reset', topicId));
  return kb;
}

export interface SendToTopicOptions {
  /** 媒体组一并转发时不需要额外的 reply */
  replyToMessageId?: number;
}

/**
 * 往话题里发一条 Markdown 消息。
 *
 * 失败时**降级为纯文本重发**：文案模板里的占位符已经被转义过，
 * 但管理员自己写的模板完全可能带一个没用反斜杠转义的 `*`。
 * 一条文案写错不该让整条中继链路失败。
 */
export async function sendToTopic(
  api: Api,
  chatId: number,
  threadId: number,
  text: string,
  options: {
    keyboard?: InlineKeyboard;
    disableNotification?: boolean;
    linkPreview?: boolean;
  } = {},
): Promise<number | null> {
  if (!text) return null;

  const payload = {
    message_thread_id: threadId,
    ...(options.keyboard ? { reply_markup: options.keyboard } : {}),
    ...(options.disableNotification ? { disable_notification: true } : {}),
    link_preview_options: { is_disabled: options.linkPreview !== true },
  };

  try {
    const sent = await api.sendMessage(chatId, text, { ...payload, parse_mode: 'Markdown' });
    return sent.message_id;
  } catch (err) {
    logger.warn({ err, threadId }, 'Markdown 发送失败，降级为纯文本重发');
    try {
      const sent = await api.sendMessage(chatId, text, payload);
      return sent.message_id;
    } catch (retryErr) {
      logger.error({ err: retryErr, threadId }, '往话题发送消息失败');
      return null;
    }
  }
}

/** 同上，但发给用户私聊。需要 botId 是因为「用户屏蔽了机器人」是按 bot 记录的 */
export async function sendToUser(
  api: Api,
  botId: number,
  userId: number,
  text: string,
  options: { keyboard?: InlineKeyboard } = {},
): Promise<number | null> {
  if (!text) return null;
  const payload = options.keyboard ? { reply_markup: options.keyboard } : {};

  try {
    const sent = await api.sendMessage(userId, text, { ...payload, parse_mode: 'Markdown' });
    return sent.message_id;
  } catch (err) {
    logger.warn({ err, userId }, 'Markdown 发送失败，降级为纯文本重发');
    try {
      const sent = await api.sendMessage(userId, text, payload);
      return sent.message_id;
    } catch (retryErr) {
      // 403 = 用户把机器人屏蔽了。这是一个需要被记录的业务状态，不是异常。
      const message = String((retryErr as Error).message ?? '');
      if (message.includes('blocked') || message.includes('403')) {
        await markUnreachable(botId, userId);
      } else {
        logger.error({ err: retryErr, userId }, '发送私聊消息失败');
      }
      return null;
    }
  }
}

/**
 * 标记用户不可达（屏蔽了机器人）。
 *
 * 必须带 botId：同一个 Telegram 用户可能同时私聊多个被托管的机器人，
 * 只按 tgUserId 更新会把「屏蔽了 A」错记到 B 头上。
 */
async function markUnreachable(botId: number, tgUserId: number): Promise<void> {
  const { db } = getDb();
  const now = Date.now();
  await db
    .update(contacts)
    .set({ isUnreachable: true, lastSeenAt: now })
    .where(and(eq(contacts.botId, botId), eq(contacts.tgUserId, tgUserId)));
}

export interface EnsureTopicResult {
  topic: TopicRow;
  /** 本次是否新建 —— 新建后需要额外做「置顶头部消息」「统计 +1」等动作 */
  created: boolean;
}

/**
 * 取（不存在则在管理群中创建）该用户的话题。
 *
 * 并发安全：同一用户的两条消息可能同时在两个协程里走到这里。
 * 依赖 `topics_bot_contact_uniq` 唯一索引 —— 第二个写入者会拿到
 * 唯一约束错误，捕获后重新查一次即可，代价远低于加锁。
 */
export async function ensureTopic(
  api: Api,
  botId: number,
  botName: string,
  adminGroupId: number,
  settings: BotSettings,
  contact: ContactRow,
): Promise<EnsureTopicResult> {
  const existing = await findTopicByContact(botId, contact.id);
  if (existing) {
    if (existing.status === 'deleted') {
      // 话题被删掉了（管理员手动删的），重新建一个，复用同一行记录
      return recreateTopic(api, botId, botName, adminGroupId, settings, contact, existing);
    }
    if (existing.status === 'closed') {
      // 用户又来消息了 —— 自动重开比让消息石沉大海更符合预期
      await reopenTopic(api, adminGroupId, existing);
      return { topic: { ...existing, status: 'open', closedAt: null }, created: false };
    }
    return { topic: existing, created: false };
  }

  const title = buildTopicTitle(settings, contact, botName);
  const iconColor = resolveIconColor(settings.topicIconColor);

  let threadId: number;
  try {
    const created = await api.createForumTopic(adminGroupId, title, { icon_color: iconColor });
    threadId = created.message_thread_id;
  } catch (err) {
    logger.error({ err, adminGroupId, contactId: contact.id }, '创建话题失败');
    throw err;
  }

  const { db } = getDb();
  const now = Date.now();

  try {
    const inserted = await db
      .insert(topics)
      .values({
        botId,
        contactId: contact.id,
        messageThreadId: threadId,
        title,
        iconColor,
        status: 'open',
        lastMessageAt: null,
        pinnedHeaderId: null,
        createdAt: now,
        closedAt: null,
      })
      .returning();

    const row = inserted[0];
    if (!row) throw new Error('写入话题记录失败');

    const withHeader = await pinHeader(api, adminGroupId, row, contact, settings, botName);
    return { topic: withHeader, created: true };
  } catch (err) {
    // 唯一约束冲突 = 另一个协程抢先建好了。用它的记录，并把我们多建的话题删掉。
    const raced = await findTopicByContact(botId, contact.id);
    if (raced) {
      logger.debug({ contactId: contact.id }, '话题创建竞态，复用已有记录并清理重复话题');
      await api.deleteForumTopic(adminGroupId, threadId).catch(() => undefined);
      return { topic: raced, created: false };
    }
    throw err;
  }
}

async function recreateTopic(
  api: Api,
  botId: number,
  botName: string,
  adminGroupId: number,
  settings: BotSettings,
  contact: ContactRow,
  stale: TopicRow,
): Promise<EnsureTopicResult> {
  const title = buildTopicTitle(settings, contact, botName);
  const iconColor = resolveIconColor(settings.topicIconColor);

  const created = await api.createForumTopic(adminGroupId, title, { icon_color: iconColor });
  const { db } = getDb();

  await db
    .update(topics)
    .set({
      messageThreadId: created.message_thread_id,
      title,
      iconColor,
      status: 'open',
      closedAt: null,
      pinnedHeaderId: null,
      lastMessageAt: null,
    })
    .where(eq(topics.id, stale.id));

  const fresh: TopicRow = {
    ...stale,
    messageThreadId: created.message_thread_id,
    title,
    iconColor,
    status: 'open',
    closedAt: null,
    pinnedHeaderId: null,
  };
  const withHeader = await pinHeader(api, adminGroupId, fresh, contact, settings, botName);
  logger.info({ topicId: stale.id, threadId: created.message_thread_id }, '话题已重建');
  return { topic: withHeader, created: true };
}

/**
 * 置顶头部消息。
 *
 * 内容里含用户名与 id，编辑消息、查库时都用得上。置顶失败（缺少
 * can_pin_messages 权限）不应影响话题可用性，所以整体吞掉异常只记日志。
 */
async function pinHeader(
  api: Api,
  adminGroupId: number,
  topic: TopicRow,
  contact: ContactRow,
  settings: BotSettings,
  botName: string,
): Promise<TopicRow> {
  if (!settings.pinTopicHeader) return topic;

  const text = renderTemplate(settings.topicHeaderTemplate, {
    name: [contact.firstName, contact.lastName].filter(Boolean).join(' ') || '未知用户',
    username: contact.username ? `@${contact.username}` : '（无用户名）',
    id: contact.tgUserId,
    firstName: contact.firstName,
    lastName: contact.lastName,
    botName,
  });

  try {
    const messageId = await sendToTopic(api, adminGroupId, topic.messageThreadId, text, {
      keyboard: headerKeyboard(topic.id, contact, topic.status as TopicStatus),
      disableNotification: true,
    });
    if (messageId === null) return topic;

    await api.pinChatMessage(adminGroupId, messageId, { disable_notification: true }).catch((err) => {
      logger.warn({ err, topicId: topic.id }, '置顶头部消息失败（通常是缺少 can_pin_messages 权限）');
    });

    const { db } = getDb();
    await db.update(topics).set({ pinnedHeaderId: messageId }).where(eq(topics.id, topic.id));
    return { ...topic, pinnedHeaderId: messageId };
  } catch (err) {
    logger.warn({ err, topicId: topic.id }, '创建头部消息失败，话题仍可正常使用');
    return topic;
  }
}

/** 用户改名后同步话题标题与头部消息；失败不影响中继 */
export async function refreshTopicIdentity(
  api: Api,
  adminGroupId: number,
  botName: string,
  settings: BotSettings,
  topic: TopicRow,
  contact: ContactRow,
): Promise<void> {
  const title = buildTopicTitle(settings, contact, botName);
  if (title !== topic.title) {
    try {
      await api.editForumTopic(adminGroupId, topic.messageThreadId, { name: title });
      const { db } = getDb();
      await db.update(topics).set({ title }).where(eq(topics.id, topic.id));
    } catch (err) {
      logger.warn({ err, topicId: topic.id }, '重命名话题失败');
    }
  }
}

/**
 * 只需要 id 与 threadId 就够 —— 处罚路径上调用方未必持有完整的话题行
 * （例如拉黑时是从规则引擎里拿到的 topicId），为它多查一次库毫无意义。
 */
export type TopicRef = Pick<TopicRow, 'id' | 'messageThreadId'>;

export async function closeTopic(api: Api, adminGroupId: number, topic: TopicRef): Promise<void> {
  try {
    await api.closeForumTopic(adminGroupId, topic.messageThreadId);
  } catch (err) {
    logger.warn({ err, topicId: topic.id }, '关闭话题失败（可能已被手动关闭）');
  }
  const { db } = getDb();
  await db
    .update(topics)
    .set({ status: 'closed', closedAt: Date.now() })
    .where(eq(topics.id, topic.id));
}

export async function reopenTopic(api: Api, adminGroupId: number, topic: TopicRef): Promise<void> {
  try {
    await api.reopenForumTopic(adminGroupId, topic.messageThreadId);
  } catch (err) {
    logger.warn({ err, topicId: topic.id }, '重开话题失败（可能已被手动重开）');
  }
  const { db } = getDb();
  await db.update(topics).set({ status: 'open', closedAt: null }).where(eq(topics.id, topic.id));
}

/** 标记话题为已删除；Telegram 侧真正删除由调用方决定（拉黑时可选） */
export async function markTopicDeleted(topicId: number): Promise<void> {
  const { db } = getDb();
  await db
    .update(topics)
    .set({ status: 'deleted', closedAt: Date.now() })
    .where(eq(topics.id, topicId));
}

/**
 * 真的在 Telegram 里删掉话题，本地同步标记为 deleted。
 *
 * 返回是否删除成功。刻意**不抛出**：Telegram 侧删除会因为缺少
 * can_delete_messages 权限而失败，但管理员在面板上表达的意图是
 * 「这个会话我不要了」，本地状态仍应当跟上 —— 否则下一次同步会把它
 * 又变成一个「正常」的会话，看起来像是删除没生效。
 */
export async function forwardTopicDelete(
  api: Api,
  adminGroupId: number,
  topic: TopicRef,
): Promise<boolean> {
  let ok = true;
  try {
    await api.deleteForumTopic(adminGroupId, topic.messageThreadId);
  } catch (err) {
    ok = false;
    logger.warn({ err, topicId: topic.id }, '删除话题失败（通常是缺少 can_delete_messages 权限）');
  }
  await markTopicDeleted(topic.id);
  return ok;
}

/** 刷新话题的最后活跃时间；仪表盘与「自动归档」都依赖它 */
export async function touchTopic(topicId: number, at = Date.now()): Promise<void> {
  const { db } = getDb();
  await db.update(topics).set({ lastMessageAt: at }).where(eq(topics.id, topicId));
}

/** 供 `{name}` 之外的地方引用：把昵称里的 Markdown 特殊字符转义好 */
export { escapeMarkdown };
