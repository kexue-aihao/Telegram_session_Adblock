import type { Context } from 'grammy';
import { channel, type BotSettings } from '@tgs/shared';
import { toIncoming, type IncomingMessage } from '../core/content.ts';
import { logger } from '../core/logger.ts';
import { SlidingWindowCounter } from '../core/batcher.ts';
import { formatUntil, renderTemplate } from '../core/template.ts';
import { blockingState, recordViolation } from '../sanctions.ts';
import { evaluateRules } from '../rules/engine.ts';
import { applyRuleHits } from '../rules/actions.ts';
import { publish } from '../core/bus.ts';
import { relayMediaGroup, relayOne } from './relay.ts';
import { bumpStats } from '../core/stats.ts';
import { getSessionSummary } from '../sessions.ts';
import {
  ensureContact,
  ensureTopic,
  sendToTopic,
  sendToUser,
  touchTopic,
  type ContactRow,
  type TopicRow,
} from './topics.ts';
import type { RuntimeDeps } from './deps.ts';

/**
 * 用户私聊 → 管理群话题 的中继管线。
 *
 * 这是整个产品的主干道，每条用户消息都要走一遍，所以分支顺序是按
 * 「拦截成本从低到高」排的：拉黑 → 处罚状态 → 刷屏 → 规则引擎 → 转发。
 * 把最贵的规则匹配放在最前面，会让被封禁用户的垃圾消息也吃满 CPU。
 */

/** 刷屏判定窗口：与 botSettings.floodThreshold 配合使用 */
const FLOOD_WINDOW_MS = 3_000;

function counterFor(deps: RuntimeDeps, contactId: number, threshold: number): SlidingWindowCounter {
  const existing = deps.flood.get(contactId);
  if (existing) {
    // 阈值可以在面板上随时改，必须同步到已存在的计数器上，
    // 否则「改了没生效」要等到下一次进程重启才自愈。
    existing.setThreshold(threshold);
    return existing;
  }
  const counter = new SlidingWindowCounter(FLOOD_WINDOW_MS, threshold);
  deps.flood.set(contactId, counter);
  return counter;
}

/**
 * 静默 / 禁言 / 拉黑期间的处理。
 *
 * 三种阻断型处罚对用户的**可见性**完全不同，这里必须区分：
 *   - silence：什么都不做，消息石沉大海（这是它的全部意义 —— 一旦回复，
 *     就等于告诉对方「你被静默了，换个号再来」）
 *   - mute：回一次禁言提示与解禁时间，之后不再重复
 *   - ban：机器人完全不再响应
 * 三者一律不转发进话题，差异只体现在是否回复。
 */
async function handleBlocked(
  deps: RuntimeDeps,
  settings: BotSettings,
  contact: ContactRow,
  kind: string | null,
  expiresAt: number | null,
): Promise<void> {
  if (kind !== 'mute') return;

  // 记的是「那一次禁言的解禁时刻」：同一个人第二次被禁言时应当再提示一次，
  // 用布尔标记会把第二次提示吞掉。
  if (deps.muteNotified.get(contact.id) === expiresAt) return;
  deps.muteNotified.set(contact.id, expiresAt);

  const text = renderTemplate(settings.muteTemplate, {
    name: contact.firstName ?? '',
    username: contact.username ?? '',
    id: contact.tgUserId,
    until: formatUntil(expiresAt),
    score: contact.violationScore,
    botName: deps.botName,
  });
  if (text) await sendToUser(deps.api, deps.botId, contact.tgUserId, text);
}

/**
 * 单条消息（或一个已聚合的相册）走完整条管线。
 *
 * 相册作为一个整体处理：整组只判一次规则、只建一次话题、只发一张告警卡片 ——
 * 否则发 10 张图会往话题里灌 10 条告警。
 */
export async function processUserMessage(
  deps: RuntimeDeps,
  messages: IncomingMessage[],
): Promise<void> {
  const first = messages[0];
  if (!first) return;

  const settings = await deps.getSettings();
  const contact = await ensureContact(deps.botId, first.from);
  deps.contacts.set(first.from.id, contact);

  // 1. 被管理员拉黑：机器人不再响应，连「你被拉黑了」都不回
  if (contact.isBlocked) return;

  // 2. 阶梯处罚的阻断态
  const state = await blockingState(contact.id);
  if (state.blocked) {
    await handleBlocked(deps, settings, contact, state.type, state.expiresAt);
    return;
  }

  // 3. 刷屏判定。命中后走 'flood' 原因的处罚，但**不删消息** ——
  //    刷屏判定比关键词规则粗糙得多，误判的代价必须小。
  const counter = counterFor(deps, contact.id, settings.floodThreshold);
  if (counter.hit()) {
    counter.reset();
    const violation = await recordViolation({
      botId: deps.botId,
      contactId: contact.id,
      ruleId: null,
      severity: 1,
      reason: 'flood',
    });
    logger.warn(
      { contactId: contact.id, threshold: settings.floodThreshold, escalated: violation.escalated },
      '检测到刷屏',
    );
    if (settings.notifyAdmins) {
      const { topic } = await ensureTopic(
        deps.api,
        deps.botId,
        deps.botName,
        deps.adminGroupId,
        settings,
        contact,
      );
      await sendToTopic(
        deps.api,
        deps.adminGroupId,
        topic.messageThreadId,
        `🌊 **疑似刷屏**\n\n用户 ${contact.firstName ?? ''}（\`#${contact.tgUserId}\`）在 3 秒内连续发送了超过 ${settings.floodThreshold} 条消息。\n当前违规分：**${violation.score}**`,
      );
    }
    return;
  }

  // 4. 规则引擎。命中即拦截 —— 先判后转，避免「先发出去再撤回」留下
  //    一个能被截图的时间窗。
  const evaluation = await evaluateRules({
    botId: deps.botId,
    scope: 'user',
    // 相册取第一条的文本即可：规则判定的是这一组消息的整体意图
    haystacks: first.haystacks.raw,
    normalized: first.haystacks.normalized,
  });

  const hasFindings = evaluation.hits.length > 0 || evaluation.timedOutRuleIds.length > 0;

  // 触发规则时先建话题：告警卡片要落在话题里，否则管理员在一个空话题里
  // 看到一条孤零零的告警，连上下文都没有。
  let topic: TopicRow | null = null;
  if (hasFindings) {
    topic = (
      await ensureTopic(deps.api, deps.botId, deps.botName, deps.adminGroupId, settings, contact)
    ).topic;

    const report = await applyRuleHits(
      {
        api: deps.api,
        botId: deps.botId,
        botName: deps.botName,
        adminGroupId: deps.adminGroupId,
        settings,
        contact,
        scope: 'user',
        sourceChatId: first.chatId,
        sourceMessageId: first.messageId,
        messageRowId: null,
        topicId: topic.id,
        threadId: topic.messageThreadId,
        rawText: first.rawText,
      },
      evaluation,
    );

    await bumpStats(deps.botId, { adsBlocked: 1 });

    if (report.blocked) {
      // 静默 / 禁言 / 拉黑：到此为止，消息不进话题
      await touchTopic(topic.id);
      return;
    }
  }

  // 5. 正常中继
  const ensured =
    topic ??
    (await ensureTopic(deps.api, deps.botId, deps.botName, deps.adminGroupId, settings, contact))
      .topic;

  if (messages.length > 1) {
    const result = await relayMediaGroup(deps.api, {
      botId: deps.botId,
      topicId: ensured.id,
      direction: 'user_to_admin',
      sourceChatId: first.chatId,
      messageIds: messages.map((m) => m.messageId),
      destChatId: deps.adminGroupId,
      threadId: ensured.messageThreadId,
      contents: messages.map((m) => m.content),
      senderLabel: null,
    });
    if (result.error) await notifyRelayFailure(deps, ensured, result.error);
  } else {
    const result = await relayOne(deps.api, {
      botId: deps.botId,
      topicId: ensured.id,
      direction: 'user_to_admin',
      sourceChatId: first.chatId,
      tgMessageId: first.messageId,
      destChatId: deps.adminGroupId,
      threadId: ensured.messageThreadId,
      content: first.content,
      senderLabel: null,
    });
    if (result.error) await notifyRelayFailure(deps, ensured, result.error);
  }

  await touchTopic(ensured.id);
  await bumpStats(deps.botId, { messagesIn: 1 });

  // 会话列表要实时刷新「最后一条消息」。广播的是**查询后的真实对象**
  // 而不是本地拼的：前端拿它直接替换列表行，本地拼装一旦与查询层有出入，
  // 就会出现「刷新一下页面就变了样」的诡异 bug。
  const summary = await getSessionSummary(ensured.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

/** 转发失败时告诉管理员，而不是让消息凭空消失 */
async function notifyRelayFailure(deps: RuntimeDeps, topic: TopicRow, error: string): Promise<void> {
  await sendToTopic(
    deps.api,
    deps.adminGroupId,
    topic.messageThreadId,
    `⚠️ **消息转发失败**\n\n\`${error.slice(0, 300)}\`\n\n常见原因：机器人不是管理群管理员、缺少 can_delete_messages 权限、或消息类型不支持转发。`,
  );
}

/**
 * 私聊消息入口。
 *
 * 相册的消息会先进缓冲：Telegram 把同一相册拆成多条独立更新，
 * 只有等这一组到齐再 `copyMessages` 才能保持相册形态。
 */
export async function handlePrivateMessage(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const message = ctx.message;
  if (!message || !ctx.from || ctx.from.is_bot) return;

  const incoming = toIncoming(message);
  if (!incoming) return;

  const settings = await deps.getSettings();

  if (message.media_group_id && settings.coalesceWindowMs > 0) {
    // key 用 media_group_id：它本身就在全局唯一，不需要再拼 chatId
    deps.batcher.push(message.media_group_id, incoming);
    return;
  }

  await processUserMessage(deps, [incoming]);
}

/** 用户在私聊里发 /start —— 回欢迎语并建档，而不是把 "/start" 中继过去 */
export async function handleStart(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const from = ctx.from;
  if (!from || !ctx.chat) return;

  const settings = await deps.getSettings();
  const contact = await ensureContact(deps.botId, from);
  deps.contacts.set(from.id, contact);

  if (contact.isBlocked) return;

  const text = renderTemplate(settings.greetingText, {
    name: from.first_name,
    username: from.username ? `@${from.username}` : '',
    id: from.id,
    firstName: from.first_name,
    lastName: from.last_name,
    botName: deps.botName,
  });

  await sendToUser(deps.api, deps.botId, from.id, text);

  // /start 是唯一一个「用户还没说话就已经需要建话题」的时机，
  // 先把话题备好，管理员就能在用户开口前看到这个人。
  try {
    const { topic, created } = await ensureTopic(
      deps.api,
      deps.botId,
      deps.botName,
      deps.adminGroupId,
      settings,
      contact,
    );
    if (created) {
      await bumpStats(deps.botId, { topicsCreated: 1 });
      const summary = await getSessionSummary(topic.id);
      if (summary) publish(channel.bot(deps.botId), 'session.created', summary);
    }
    await touchTopic(topic.id);
  } catch (err) {
    logger.error({ err, contactId: contact.id }, '为用户创建话题失败');
  }
}
