import type {
  MessagePageQuery,
  Paginated,
  RelayedMessage,
  SessionDetail,
  SessionQuery,
  SessionSummary,
} from '@tgs/shared';
import { and, asc, desc, eq, gt, inArray, like, lt, or, sql, type SQL } from 'drizzle-orm';
import { getDb } from './db/client.ts';
import { contacts, messageMedia, messages, ruleHits, sanctions, topics } from './db/schema.ts';
import { toRelayedMessage, type MessageRow, type MediaRow } from './bots/relay.ts';

/**
 * 会话（= 一个话题 + 一个联系人）的查询层。
 *
 * 面板的会话列表与中继管线都要用同一个 DTO，放这里而不是 HTTP 路由里，
 * 是因为 WS 推送 `session.created` / `session.updated` 时也要构造同样的对象 ——
 * 两处各写一份必然漂移，前端的类型校验会在某个字段上突然开始报错。
 */

type SessionRow = {
  topic: typeof topics.$inferSelect;
  contact: typeof contacts.$inferSelect;
  botName: string;
  botUsername: string;
};

function toSummary(row: SessionRow, extras: {
  lastMessagePreview: string | null;
  lastMessageDirection: 'user_to_admin' | 'admin_to_user' | null;
  messageCount: number;
}): SessionSummary {
  const { topic, contact } = row;
  return {
    id: topic.id,
    botId: topic.botId,
    botName: row.botName,
    botUsername: row.botUsername,
    contactId: contact.id,
    displayName:
      [contact.firstName, contact.lastName].filter(Boolean).join(' ') ||
      contact.username ||
      `用户 ${contact.tgUserId}`,
    username: contact.username,
    tgUserId: contact.tgUserId,
    threadId: topic.messageThreadId,
    title: topic.title,
    status: topic.status as SessionSummary['status'],
    lastMessageAt: topic.lastMessageAt,
    lastMessagePreview: extras.lastMessagePreview,
    lastMessageDirection: extras.lastMessageDirection,
    messageCount: extras.messageCount,
    violationScore: contact.violationScore,
    isBlocked: contact.isBlocked,
    createdAt: topic.createdAt,
    closedAt: topic.closedAt,
  };
}

/**
 * 最后一条消息的预览。
 *
 * 用相关子查询而不是「先查话题列表、再逐条查最后消息」：后者是典型的
 * N+1，30 行列表就是 30 次往返。`messages_topic_recent_idx` 让这个子查询
 * 走索引倒序取一行，代价可以忽略。
 */
const previewSql = sql<string | null>`(
  select coalesce(${messages.text}, ${messages.caption})
  from ${messages}
  where ${messages.topicId} = ${topics.id}
  order by ${messages.createdAt} desc
  limit 1
)`;

const lastDirectionSql = sql<string | null>`(
  select ${messages.direction}
  from ${messages}
  where ${messages.topicId} = ${topics.id}
  order by ${messages.createdAt} desc
  limit 1
)`;

const messageCountSql = sql<number>`(
  select count(*) from ${messages} where ${messages.topicId} = ${topics.id}
)`;

function baseSelect() {
  const { db } = getDb();
  return db
    .select({
      topic: topics,
      contact: contacts,
      botName: sql<string>`(select name from bots where bots.id = ${topics.botId})`,
      botUsername: sql<string>`(select username from bots where bots.id = ${topics.botId})`,
      lastMessagePreview: previewSql,
      lastMessageDirection: lastDirectionSql,
      messageCount: messageCountSql,
    })
    .from(topics)
    .innerJoin(contacts, eq(contacts.id, topics.contactId));
}

export async function getSessionSummary(topicId: number): Promise<SessionSummary | null> {
  const rows = await baseSelect().where(eq(topics.id, topicId)).limit(1);
  const row = rows[0];
  if (!row) return null;
  return toSummary(row, {
    lastMessagePreview: row.lastMessagePreview,
    lastMessageDirection: row.lastMessageDirection as 'user_to_admin' | 'admin_to_user' | null,
    messageCount: row.messageCount,
  });
}

/**
 * 会话列表。游标分页而不是 offset 分页 —— 会话会实时新增，
 * offset 分页在翻页时必然出现重复或漏项。
 * 这里用「最后活跃时间」倒序，游标即上一页最后一行的 lastMessageAt。
 */
export async function listSessions(query: SessionQuery): Promise<Paginated<SessionSummary>> {
  const { db } = getDb();
  const conditions: SQL[] = [];

  if (query.botId !== undefined) conditions.push(eq(topics.botId, query.botId));
  if (query.status !== undefined) conditions.push(eq(topics.status, query.status));
  if (query.flaggedOnly === true) conditions.push(gt(contacts.violationScore, 0));
  if (query.q) {
    const pattern = `%${query.q}%`;
    const search = or(
      like(contacts.username, pattern),
      like(contacts.firstName, pattern),
      like(contacts.lastName, pattern),
      like(topics.title, pattern),
    );
    if (search) conditions.push(search);
  }

  const total = await db
    .select({ count: sql<number>`count(*)` })
    .from(topics)
    .innerJoin(contacts, eq(contacts.id, topics.contactId))
    .where(conditions.length > 0 ? and(...conditions) : undefined);

  // 无活跃时间的会话（刚建、还没消息）排在最后：它们对运维最不重要
  const cursorValue = query.cursor;
  if (cursorValue !== undefined) {
    const boundary = or(
      lt(topics.lastMessageAt, cursorValue),
      sql`${topics.lastMessageAt} is null`,
    );
    if (boundary) conditions.push(boundary);
  }

  const rows = await baseSelect()
    .where(conditions.length > 0 ? and(...conditions) : undefined)
    .orderBy(desc(sql`coalesce(${topics.lastMessageAt}, ${topics.createdAt})`))
    .limit(query.limit);

  const items = rows.map((row) =>
    toSummary(row, {
      lastMessagePreview: row.lastMessagePreview,
      lastMessageDirection: row.lastMessageDirection as 'user_to_admin' | 'admin_to_user' | null,
      messageCount: row.messageCount,
    }),
  );

  const last = items[items.length - 1];
  return {
    items,
    nextCursor: items.length === query.limit && last ? (last.lastMessageAt ?? last.createdAt) : null,
    total: total[0]?.count ?? null,
  };
}

export async function getSessionDetail(topicId: number): Promise<SessionDetail | null> {
  const summary = await getSessionSummary(topicId);
  if (!summary) return null;

  const { db } = getDb();
  const contact = (
    await db.select().from(contacts).where(eq(contacts.id, summary.contactId)).limit(1)
  )[0];
  if (!contact) return null;

  const active = await db
    .select({
      id: sanctions.id,
      type: sanctions.type,
      reason: sanctions.reason,
      expiresAt: sanctions.expiresAt,
      createdAt: sanctions.createdAt,
    })
    .from(sanctions)
    .where(and(eq(sanctions.contactId, contact.id), eq(sanctions.isActive, true)))
    .orderBy(desc(sanctions.createdAt))
    .limit(20);

  const recent = await db
    .select({
      id: ruleHits.id,
      ruleName: ruleHits.ruleName,
      matchedText: ruleHits.matchedText,
      outcomes: ruleHits.outcomes,
      createdAt: ruleHits.createdAt,
    })
    .from(ruleHits)
    .where(eq(ruleHits.contactId, contact.id))
    .orderBy(desc(ruleHits.createdAt))
    .limit(10);

  return {
    session: summary,
    // 过期的禁言在这里过滤掉：状态机把它留成 active 是为了保留「曾经禁言过」
    // 这个事实，但详情页顶部显示一条已经过期的禁言会让管理员误判现状。
    activeSanctions: active
      .filter((row) => row.expiresAt === null || row.expiresAt > Date.now())
      .map((row) => ({
        id: row.id,
        type: row.type as SessionDetail['activeSanctions'][number]['type'],
        reason: row.reason as SessionDetail['activeSanctions'][number]['reason'],
        expiresAt: row.expiresAt,
        createdAt: row.createdAt,
      })),
    notes: contact.notes,
    recentHits: recent.map((row) => ({
      id: row.id,
      ruleName: row.ruleName,
      matchedText: row.matchedText,
      outcome: (row.outcomes ?? []).join(' + '),
      createdAt: row.createdAt,
    })),
  };
}

/**
 * 分页拉取某个话题下的消息。
 *
 * 面板的聊天视图是「往上翻历史」的交互，所以按时间**倒序**取一页，
 * 返回给前端后再由前端反转为正序渲染 —— 这样「加载更多」永远是取更早的消息。
 */
export async function listMessages(
  topicId: number,
  query: MessagePageQuery,
): Promise<Paginated<RelayedMessage>> {
  const { db } = getDb();
  const conditions: SQL[] = [eq(messages.topicId, topicId)];
  if (query.cursor !== undefined) conditions.push(lt(messages.id, query.cursor));

  const rows = await db
    .select()
    .from(messages)
    .where(and(...conditions))
    .orderBy(desc(messages.id))
    .limit(query.limit);

  const ids = rows.map((row) => row.id);
  const media =
    ids.length > 0
      ? await db
          .select()
          .from(messageMedia)
          .where(inArray(messageMedia.messageId, ids))
          .orderBy(asc(messageMedia.position))
      : [];

  const byMessage = new Map<number, MediaRow[]>();
  for (const item of media) {
    const list = byMessage.get(item.messageId) ?? [];
    list.push(item);
    byMessage.set(item.messageId, list);
  }

  const items = rows.map((row: MessageRow) => toRelayedMessage(row, byMessage.get(row.id) ?? []));
  const last = items[items.length - 1];

  return {
    items,
    nextCursor: items.length === query.limit && last ? last.id : null,
    total: null,
  };
}

/** 取某个话题最近一条消息，用于中继失败时的上下文提示 */
export async function latestMessage(topicId: number): Promise<MessageRow | null> {
  const { db } = getDb();
  const rows = await db
    .select()
    .from(messages)
    .where(eq(messages.topicId, topicId))
    .orderBy(desc(messages.id))
    .limit(1);
  return rows[0] ?? null;
}
