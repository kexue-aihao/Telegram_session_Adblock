import { channel, type MessageContent, type RelayedMessage } from '@tgs/shared';
import type { Api } from 'grammy';
import { and, desc, eq } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { messageMedia, messages } from '../db/schema.ts';
import { publish } from '../core/bus.ts';
import { logger } from '../core/logger.ts';
import type { ExtractedMessage } from '../core/content.ts';

/**
 * 中继映射表。
 *
 * 整条中继链路的地基：源消息与转发副本的对应关系。没有它，编辑镜像、
 * 删除镜像、面板里「点这条消息跳到 Telegram」全部做不到 —— 因为
 * `copyMessage` 返回的 id 是**新消息**的 id，与源消息毫无关联。
 */

export type MessageRow = typeof messages.$inferSelect;
export type MediaRow = typeof messageMedia.$inferSelect;

export interface RecordMessageInput {
  botId: number;
  topicId: number;
  direction: 'user_to_admin' | 'admin_to_user';
  sourceChatId: number;
  tgMessageId: number;
  destChatId: number | null;
  relayedMessageId: number | null;
  content: ExtractedMessage;
  senderLabel?: string | null;
  ruleHitId?: number | null;
}

/** 写入一条中继记录并广播。返回落库后的行（含自增 id）。 */
export async function recordMessage(input: RecordMessageInput): Promise<MessageRow> {
  const { db } = getDb();
  const now = Date.now();

  const inserted = await db
    .insert(messages)
    .values({
      botId: input.botId,
      topicId: input.topicId,
      direction: input.direction,
      sourceChatId: input.sourceChatId,
      tgMessageId: input.tgMessageId,
      destChatId: input.destChatId,
      relayedMessageId: input.relayedMessageId,
      contentType: input.content.contentType,
      text: input.content.text,
      caption: input.content.caption,
      mediaGroupId: input.content.mediaGroupId,
      hasHiddenLink: input.content.hasHiddenLink,
      hiddenLinks: input.content.hiddenLinks,
      ruleHitId: input.ruleHitId ?? null,
      senderLabel: input.senderLabel ?? null,
      createdAt: now,
      editedAt: null,
      deletedAt: null,
    })
    .returning();

  const row = inserted[0];
  if (!row) throw new Error('写入消息记录失败');

  if (input.content.media.length > 0) {
    await db.insert(messageMedia).values(
      input.content.media.map((item, index) => ({
        messageId: row.id,
        kind: item.kind,
        fileId: item.fileId,
        fileUniqueId: item.fileUniqueId,
        // 相册里同一个 media_group 的多条消息各自带一份媒体，
        // position 用 index 而不是 item.position，避免不同消息间串号
        position: index,
      })),
    );
  }

  return row;
}

/** 组装成面板用的 DTO */
export function toRelayedMessage(row: MessageRow, media: MediaRow[]): RelayedMessage {
  const content: MessageContent = {
    type: row.contentType as MessageContent['type'],
    text: row.text,
    caption: row.caption,
    media: media.map((m) => ({
      kind: m.kind,
      fileId: m.fileId,
      fileUniqueId: m.fileUniqueId,
      position: m.position,
    })),
    mediaGroupId: row.mediaGroupId,
    hasHiddenLink: row.hasHiddenLink,
    hiddenLinks: row.hiddenLinks ?? [],
  };

  return {
    id: row.id,
    topicId: row.topicId,
    direction: row.direction as RelayedMessage['direction'],
    content,
    isDeleted: row.deletedAt !== null,
    editedAt: row.editedAt,
    createdAt: row.createdAt,
    ruleHitId: row.ruleHitId,
    senderLabel: row.senderLabel,
  };
}

/** 落库后广播给面板；媒体行单独查一次，因为它是低频事件，不值得为它做联表 */
export async function publishMessage(row: MessageRow, event: 'message.new' | 'message.updated' = 'message.new'): Promise<void> {
  const { db } = getDb();
  const media = await db.select().from(messageMedia).where(eq(messageMedia.messageId, row.id));
  publish(channel.topic(row.topicId), event, toRelayedMessage(row, media));
}

/** 按源消息反查映射：编辑 / 删除镜像的入口 */
export async function findBySource(
  sourceChatId: number,
  tgMessageId: number,
): Promise<MessageRow | null> {
  const { db } = getDb();
  const rows = await db
    .select()
    .from(messages)
    .where(and(eq(messages.sourceChatId, sourceChatId), eq(messages.tgMessageId, tgMessageId)))
    .orderBy(desc(messages.id))
    .limit(1);
  return rows[0] ?? null;
}

/** 按落地消息反查映射：管理员回复时定位是哪一个话题 */
export async function findByDestination(
  destChatId: number,
  relayedMessageId: number,
): Promise<MessageRow | null> {
  const { db } = getDb();
  const rows = await db
    .select()
    .from(messages)
    .where(and(eq(messages.destChatId, destChatId), eq(messages.relayedMessageId, relayedMessageId)))
    .orderBy(desc(messages.id))
    .limit(1);
  return rows[0] ?? null;
}

/** 同一相册下的全部映射，用于整组镜像编辑/删除 */
export async function findByMediaGroup(topicId: number, mediaGroupId: string): Promise<MessageRow[]> {
  const { db } = getDb();
  return db
    .select()
    .from(messages)
    .where(and(eq(messages.topicId, topicId), eq(messages.mediaGroupId, mediaGroupId)))
    .orderBy(desc(messages.id));
}

export interface RelayResult {
  row: MessageRow | null;
  /** 复制失败的原因；调用方据此决定要不要在话题里提示管理员 */
  error: string | null;
}

/**
 * 转发单条消息。
 *
 * 用 `copyMessage` 而不是重新 `sendMessage`：它保留了原始媒体、格式，
 * 也会带上「转发自」的来源，且不需要我们把 file_id 再下载一遍。
 */
export async function relayOne(
  api: Api,
  params: {
    botId: number;
    topicId: number;
    direction: 'user_to_admin' | 'admin_to_user';
    sourceChatId: number;
    tgMessageId: number;
    destChatId: number;
    /** 话题中继必须带；私聊中继不能带 */
    threadId?: number;
    content: ExtractedMessage;
    senderLabel?: string | null;
    ruleHitId?: number | null;
  },
): Promise<RelayResult> {
  try {
    const copied = await api.copyMessage(params.destChatId, params.sourceChatId, params.tgMessageId, {
      ...(params.threadId !== undefined ? { message_thread_id: params.threadId } : {}),
    });

    const row = await recordMessage({
      botId: params.botId,
      topicId: params.topicId,
      direction: params.direction,
      sourceChatId: params.sourceChatId,
      tgMessageId: params.tgMessageId,
      destChatId: params.destChatId,
      relayedMessageId: copied.message_id,
      content: params.content,
      senderLabel: params.senderLabel ?? null,
      ruleHitId: params.ruleHitId ?? null,
    });

    await publishMessage(row);
    return { row, error: null };
  } catch (err) {
    const message = (err as Error).message ?? String(err);
    logger.error(
      { err, sourceChatId: params.sourceChatId, tgMessageId: params.tgMessageId },
      '消息转发失败',
    );
    return { row: null, error: message };
  }
}

/**
 * 转发一个相册（媒体组）。
 *
 * `copyMessages` 一次最多 100 条，且**必须**整组一起发才能保持相册形态 ——
 * 逐条 copyMessage 会被 Telegram 渲染成 N 条独立消息，视觉上完全走样。
 */
export async function relayMediaGroup(
  api: Api,
  params: {
    botId: number;
    topicId: number;
    direction: 'user_to_admin' | 'admin_to_user';
    sourceChatId: number;
    messageIds: number[];
    destChatId: number;
    threadId?: number;
    /** 与 messageIds 一一对应的内容提取结果 */
    contents: ExtractedMessage[];
    senderLabel?: string | null;
    ruleHitId?: number | null;
  },
): Promise<{ rows: MessageRow[]; error: string | null }> {
  if (params.messageIds.length === 0) return { rows: [], error: null };

  try {
    const copied = await api.copyMessages(params.destChatId, params.sourceChatId, params.messageIds, {
      ...(params.threadId !== undefined ? { message_thread_id: params.threadId } : {}),
    });

    const rows: MessageRow[] = [];
    for (const [index, messageId] of params.messageIds.entries()) {
      const content = params.contents[index];
      if (!content) continue;
      const relayed = copied[index];
      const row = await recordMessage({
        botId: params.botId,
        topicId: params.topicId,
        direction: params.direction,
        sourceChatId: params.sourceChatId,
        tgMessageId: messageId,
        destChatId: params.destChatId,
        relayedMessageId: relayed?.message_id ?? null,
        content,
        senderLabel: params.senderLabel ?? null,
        ruleHitId: params.ruleHitId ?? null,
      });
      rows.push(row);
      await publishMessage(row);
    }

    return { rows, error: null };
  } catch (err) {
    const message = (err as Error).message ?? String(err);
    logger.error({ err, sourceChatId: params.sourceChatId }, '相册转发失败');
    return { rows: [], error: message };
  }
}

/**
 * 删除一条消息的镜像副本。
 *
 * 用在两个地方：规则命中后撤回已转发进话题的副本；用户在 Telegram 里
 * 删掉了自己的消息。Telegram 只允许删除 48 小时内的消息，超时会报错，
 * 那种情况下把库里标记成已删即可 —— 面板不该显示一条「其实还在」的消息。
 */
export async function deleteMirror(
  api: Api,
  destChatId: number,
  relayedMessageId: number,
): Promise<boolean> {
  try {
    await api.deleteMessage(destChatId, relayedMessageId);
    return true;
  } catch (err) {
    logger.debug({ err, destChatId, relayedMessageId }, '删除镜像失败（通常超过 48 小时限制）');
    return false;
  }
}

/** 标记为已删除并广播 */
export async function markDeleted(row: MessageRow): Promise<void> {
  const { db } = getDb();
  await db.update(messages).set({ deletedAt: Date.now() }).where(eq(messages.id, row.id));
  publish(channel.topic(row.topicId), 'message.deleted', {
    messageId: row.id,
    topicId: row.topicId,
  });
}

/** 标记为已编辑并广播 */
export async function markEdited(row: MessageRow, text: string | null, caption: string | null): Promise<void> {
  const { db } = getDb();
  const now = Date.now();
  await db
    .update(messages)
    .set({ editedAt: now, text: text ?? row.text, caption: caption ?? row.caption })
    .where(eq(messages.id, row.id));

  const updated: MessageRow = { ...row, editedAt: now, text: text ?? row.text, caption: caption ?? row.caption };
  await publishMessage(updated, 'message.updated');
}

/** 把消息标记为「被规则命中」（命中先于转发，因此是补写） */
export async function attachRuleHit(messageId: number, ruleHitId: number): Promise<void> {
  const { db } = getDb();
  await db.update(messages).set({ ruleHitId }).where(eq(messages.id, messageId));
}

/** 面板「以管理员身份发消息」：落库 + 广播，不经过 Telegram */
export async function recordAdminOutgoing(params: {
  botId: number;
  topicId: number;
  contactTgId: number;
  botTgId: number;
  text: string;
}): Promise<MessageRow> {
  const row = await recordMessage({
    botId: params.botId,
    topicId: params.topicId,
    direction: 'admin_to_user',
    sourceChatId: params.botTgId,
    // 面板发出的消息在 Telegram 侧没有对应的源消息，用 0 占位。
    // 反查连接（findBySource）只用于真实 Telegram 消息，不会撞上它。
    tgMessageId: 0,
    destChatId: params.contactTgId,
    relayedMessageId: null,
    content: {
      contentType: 'text',
      text: params.text,
      caption: null,
      media: [],
      mediaGroupId: null,
      hasHiddenLink: false,
      hiddenLinks: [],
      relayable: true,
    },
    senderLabel: '面板',
  });
  await publishMessage(row);
  return row;
}
