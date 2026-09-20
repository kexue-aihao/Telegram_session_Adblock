import type { Context } from 'grammy';
import { channel } from '@tgs/shared';
import { eq } from 'drizzle-orm';
import { getDb } from '../../db/client.ts';
import { contacts } from '../../db/schema.ts';
import { buildHaystacks, extractMessage } from '../../core/content.ts';
import { logger } from '../../core/logger.ts';
import { publish } from '../../core/bus.ts';
import { bumpStats } from '../../core/stats.ts';
import { evaluateRules } from '../../rules/engine.ts';
import { applyRuleHits } from '../../rules/actions.ts';
import { getSessionSummary } from '../../sessions.ts';
import { findByDestination, publishMessage, recordMessage, relayOne } from '../relay.ts';
import {
  findContactById,
  findTopicByThread,
  sendToTopic,
  sendToUser,
  touchTopic,
  type ContactRow,
  type TopicRow,
} from '../topics.ts';
import type { RuntimeDeps } from '../deps.ts';

/**
 * 管理群话题 → 用户私聊 的中继。
 *
 * 只在 `message_thread_id` 命中 `topics` 表时才处理：
 * 管理群里的 General 话题、机器人自己的消息、以及管理员之间的闲聊
 * 都不该被转发给任何用户。这个判断是整条链路的安全阀 ——
 * 漏掉它，管理群里的每一句话都会喷给某个用户。
 */

export interface AdminRelayOptions {
  /** 命令处理器已经处理过了，跳过普通中继 */
  skip?: boolean;
}

export async function handleTopicMessage(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const message = ctx.message;
  if (!message) return;

  // 话题里只有带 message_thread_id 的消息才是某个用户的会话
  const threadId = message.message_thread_id;
  if (threadId === undefined || message.is_topic_message !== true) return;

  // 机器人自己的消息（比如告警卡片）绝不能再被中继回去
  if (ctx.from?.is_bot === true) return;

  const topic = await findTopicByThread(deps.botId, threadId);
  if (!topic) {
    // General 话题或我们未创建的话题。静默忽略是正确的 ——
    // 在这里提示「未知话题」只会污染管理群的正常对话。
    return;
  }

  const contact = await findContactById(topic.contactId);
  if (!contact) {
    logger.warn({ topicId: topic.id }, '话题对应的联系人已不存在');
    return;
  }

  await relayToUser(ctx, deps, topic, contact, message);
}

async function relayToUser(
  ctx: Context,
  deps: RuntimeDeps,
  topic: TopicRow,
  contact: ContactRow,
  message: NonNullable<Context['message']>,
): Promise<void> {
  const settings = await deps.getSettings();

  // 被拉黑的用户：告诉管理员而不是默默丢弃 —— 否则管理员会以为
  // 「消息发出去了但对方没回」，反复重发。
  if (contact.isBlocked) {
    await sendToTopic(
      deps.api,
      deps.adminGroupId,
      topic.messageThreadId,
      `🚫 **该用户已被拉黑**，你的消息没有发送给他。\n如需恢复通信，请点击置顶消息里的「解除拉黑」。`,
    );
    return;
  }

  if (contact.isUnreachable) {
    await sendToTopic(
      deps.api,
      deps.adminGroupId,
      topic.messageThreadId,
      `📵 **该用户已屏蔽机器人**，消息无法送达。`,
    );
    // 不 return：用户可能只是暂时屏蔽，消息仍值得尝试一次
  }

  // 规则在此**仅审计不处罚** —— 规则是给终端用户定的，
  // 让管理员被自己的规则静默掉是荒谬的。
  const haystacks = buildHaystacks(message);
  const evaluation = await evaluateRules({
    botId: deps.botId,
    scope: 'admin',
    haystacks: haystacks.raw,
    normalized: haystacks.normalized,
  });

  if (evaluation.hits.length > 0) {
    await applyRuleHits(
      {
        api: deps.api,
        botId: deps.botId,
        botName: deps.botName,
        adminGroupId: deps.adminGroupId,
        settings,
        contact,
        scope: 'admin',
        sourceChatId: message.chat.id,
        sourceMessageId: message.message_id,
        messageRowId: null,
        topicId: topic.id,
        threadId: topic.messageThreadId,
        rawText: message.text ?? message.caption ?? '',
      },
      evaluation,
    );
  }

  // 引用回复：管理员在话题里长按某条用户消息回复时，把被引的内容
  // 一并带过去，用户才知道自己在回答哪一句。
  const quoted = await resolveQuoted(message, topic);

  const content = extractMessage(message);

  if (quoted && content.contentType === 'text' && content.text) {
    const text = `> ${quoted.replace(/\n/g, '\n> ')}\n\n${content.text}`;
    const sent = await sendToUser(deps.api, deps.botId, contact.tgUserId, text);
    if (sent === null) {
      await reportUnreachable(deps, topic);
      return;
    }
    // 这条是我们**自己拼出来的文本**，不是 copyMessage 的副本，
    // 所以只能手工落库。这里千万不能再调 relayOne —— 它会再 copy 一次，
    // 用户会收到两条一模一样的消息。
    await recordMessage({
      botId: deps.botId,
      topicId: topic.id,
      direction: 'admin_to_user',
      sourceChatId: message.chat.id,
      tgMessageId: message.message_id,
      destChatId: contact.tgUserId,
      relayedMessageId: sent,
      content: { ...content, text },
      senderLabel: senderLabel(ctx),
    }).then((row) => publishMessage(row));
  } else {
    const result = await relayOne(deps.api, {
      botId: deps.botId,
      topicId: topic.id,
      direction: 'admin_to_user',
      sourceChatId: message.chat.id,
      tgMessageId: message.message_id,
      destChatId: contact.tgUserId,
      content,
      senderLabel: senderLabel(ctx),
    });
    if (result.error) {
      await reportUnreachable(deps, topic);
      return;
    }
  }

  await touchTopic(topic.id);
  await bumpStats(deps.botId, { messagesOut: 1 });

  const summary = await getSessionSummary(topic.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

/**
 * 找出被回复的那条消息在用户侧对应的原文。
 *
 * 只有「回复一条我们转发过的消息」才成立 —— 管理员回复自己的话、
 * 或回复告警卡片，都不该带引用。
 */
async function resolveQuoted(
  message: NonNullable<Context['message']>,
  topic: TopicRow,
): Promise<string | null> {
  const replyTo = message.reply_to_message;
  if (!replyTo) return null;

  const mapped = await findByDestination(message.chat.id, replyTo.message_id);
  if (!mapped || mapped.topicId !== topic.id) return null;
  if (mapped.direction !== 'user_to_admin') return null;

  const text = mapped.text ?? mapped.caption;
  if (!text) return null;
  return text.length > 200 ? `${text.slice(0, 200)}…` : text;
}

function senderLabel(ctx: Context): string {
  const from = ctx.from;
  if (!from) return '管理员';
  return from.username ? `@${from.username}` : from.first_name;
}

/** 发送失败时在话题里说明原因，并标记联系人不可达 */
async function reportUnreachable(deps: RuntimeDeps, topic: TopicRow): Promise<void> {
  const contact = await findContactById(topic.contactId);
  if (contact && !contact.isUnreachable) {
    const { db } = getDb();
    await db.update(contacts).set({ isUnreachable: true }).where(eq(contacts.id, contact.id));
  }

  await sendToTopic(
    deps.api,
    deps.adminGroupId,
    topic.messageThreadId,
    '📵 **消息发送失败**：对方可能已经屏蔽了这个机器人。',
  );
}
