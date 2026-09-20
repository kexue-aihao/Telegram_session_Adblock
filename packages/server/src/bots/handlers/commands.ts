import type { Context } from 'grammy';
import { channel } from '@tgs/shared';
import { eq } from 'drizzle-orm';
import { getDb } from '../../db/client.ts';
import { contacts } from '../../db/schema.ts';
import { logger } from '../../core/logger.ts';
import { publish } from '../../core/bus.ts';
import { recordAudit } from '../../core/audit.ts';
import { resetViolations } from '../../sanctions.ts';
import { getSessionSummary } from '../../sessions.ts';
import { ruleStats } from '../../rules/engine.ts';
import {
  closeTopic,
  findContactById,
  findTopicById,
  findTopicByThread,
  parseTopicCallback,
  reopenTopic,
  sendToTopic,
  sendToUser,
  type ContactRow,
  type TopicRow,
} from '../topics.ts';
import type { RuntimeDeps } from '../deps.ts';

/**
 * 话题内的管理与快捷操作。
 *
 * 每个动作都有两条入口：命令（`/close`）与置顶消息上的按钮。
 * 两者最终都走下面这几个 `do*` 函数 —— 分成两套实现早晚会出现
 * 「命令版本修好了、按钮版本还有老 bug」。
 */

const HELP_TEXT = `**可用命令**

/close — 关闭当前话题
/reopen — 重新打开当前话题
/ban — 拉黑该用户并关闭话题
/unban — 解除拉黑
/reset — 清零违规分并解除全部处罚
/info — 查看该用户档案
/rules — 规则引擎概况

置顶消息上的按钮与这些命令等价。`;

/** 命令只在管理群里生效；私聊里发命令交给别处处理 */
export async function handleAdminCommand(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const message = ctx.message;
  if (!message?.text) return;

  const threadId = message.message_thread_id;
  if (threadId === undefined) return;

  const topic = await findTopicByThread(deps.botId, threadId);
  const command = (message.text.split(/[\s@]/)[0] ?? '').toLowerCase();

  if (command === '/help' || command === '/start') {
    await sendToTopic(deps.api, deps.adminGroupId, threadId, HELP_TEXT);
    return;
  }

  if (!topic) {
    // /rules、/stats 这类全局命令不依赖当前话题
    if (command === '/rules' || command === '/stats') {
      await handleGlobalCommand(ctx, deps, threadId, command);
    }
    return;
  }

  const contact = await findContactById(topic.contactId);
  if (!contact) return;

  switch (command) {
    case '/close':
      await doClose(deps, topic, ctx.from?.username ?? 'admin');
      break;
    case '/reopen':
      await doReopen(deps, topic, ctx.from?.username ?? 'admin');
      break;
    case '/ban':
      await doBan(deps, topic, contact, ctx.from?.username ?? 'admin');
      break;
    case '/unban':
      await doUnban(deps, topic, contact, ctx.from?.username ?? 'admin');
      break;
    case '/reset':
      await doReset(deps, topic, contact, ctx.from?.username ?? 'admin');
      break;
    case '/info':
      await doInfo(deps, topic, contact);
      break;
    case '/rules':
    case '/stats':
      await handleGlobalCommand(ctx, deps, threadId, command);
      break;
    default:
      break;
  }
}

async function handleGlobalCommand(
  ctx: Context,
  deps: RuntimeDeps,
  threadId: number,
  command: string,
): Promise<void> {
  if (command === '/rules') {
    const stats = await ruleStats();
    await sendToTopic(
      deps.api,
      deps.adminGroupId,
      threadId,
      `**规则引擎**\n\n规则总数：${stats.total}\n已启用：${stats.enabled}\n因超时被自动停用：${stats.autoDisabled}\n\n在面板的「规则」页可以进行增删改与正则测试。`,
    );
    return;
  }

  await sendToTopic(
    deps.api,
    deps.adminGroupId,
    threadId,
    '完整统计请查看面板的仪表盘页。',
  );
  void ctx;
}

async function doClose(deps: RuntimeDeps, topic: TopicRow, actor: string): Promise<void> {
  await closeTopic(deps.api, deps.adminGroupId, topic);
  await recordAudit({
    actorType: 'admin',
    actorId: actor,
    action: 'session.closed',
    targetType: 'topic',
    targetId: topic.id,
  });
  const summary = await getSessionSummary(topic.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

async function doReopen(deps: RuntimeDeps, topic: TopicRow, actor: string): Promise<void> {
  await reopenTopic(deps.api, deps.adminGroupId, topic);
  await recordAudit({
    actorType: 'admin',
    actorId: actor,
    action: 'session.reopened',
    targetType: 'topic',
    targetId: topic.id,
  });
  const summary = await getSessionSummary(topic.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

async function doBan(
  deps: RuntimeDeps,
  topic: TopicRow,
  contact: ContactRow,
  actor: string,
): Promise<void> {
  const { db } = getDb();
  await db.update(contacts).set({ isBlocked: true }).where(eq(contacts.id, contact.id));
  await resetViolations(contact.id);
  await closeTopic(deps.api, deps.adminGroupId, topic);

  await recordAudit({
    actorType: 'admin',
    actorId: actor,
    action: 'contact.banned',
    targetType: 'contact',
    targetId: contact.id,
    detail: { tgUserId: contact.tgUserId, username: contact.username },
  });

  await sendToTopic(
    deps.api,
    deps.adminGroupId,
    topic.messageThreadId,
    `🚫 已拉黑 **${contact.firstName ?? contact.tgUserId}**，其消息不会再进入本话题。`,
  );

  const summary = await getSessionSummary(topic.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

async function doUnban(
  deps: RuntimeDeps,
  topic: TopicRow,
  contact: ContactRow,
  actor: string,
): Promise<void> {
  const { db } = getDb();
  await db.update(contacts).set({ isBlocked: false }).where(eq(contacts.id, contact.id));
  await reopenTopic(deps.api, deps.adminGroupId, topic);

  await recordAudit({
    actorType: 'admin',
    actorId: actor,
    action: 'contact.unbanned',
    targetType: 'contact',
    targetId: contact.id,
  });

  await sendToTopic(deps.api, deps.adminGroupId, topic.messageThreadId, '♻️ 已解除拉黑。');
  const summary = await getSessionSummary(topic.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

async function doReset(
  deps: RuntimeDeps,
  topic: TopicRow,
  contact: ContactRow,
  actor: string,
): Promise<void> {
  await resetViolations(contact.id);
  await recordAudit({
    actorType: 'admin',
    actorId: actor,
    action: 'contact.violations_reset',
    targetType: 'contact',
    targetId: contact.id,
  });
  await sendToTopic(
    deps.api,
    deps.adminGroupId,
    topic.messageThreadId,
    '🧹 已清零违规分并解除全部处罚。',
  );
  const summary = await getSessionSummary(topic.id);
  if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
}

async function doInfo(deps: RuntimeDeps, topic: TopicRow, contact: ContactRow): Promise<void> {
  const lines = [
    `👤 **${contact.firstName ?? ''} ${contact.lastName ?? ''}**`.trim(),
    `用户名：${contact.username ? `@${contact.username}` : '（无）'}`,
    `Telegram ID：\`${contact.tgUserId}\``,
    `违规分：**${contact.violationScore}**`,
    `拉黑：${contact.isBlocked ? '是' : '否'}`,
    `可达：${contact.isUnreachable ? '否（已屏蔽机器人）' : '是'}`,
    `首次接触：${new Date(contact.firstSeenAt).toLocaleString('zh-CN', { hour12: false })}`,
  ];
  if (contact.notes) lines.push(`备注：${contact.notes}`);
  await sendToTopic(deps.api, deps.adminGroupId, topic.messageThreadId, lines.join('\n'));
}

/**
 * 置顶消息上的按钮回调。
 *
 * `answerCallbackQuery` 必须被调用 —— 否则用户端的按钮会一直转圈，
 * 管理员会以为「点了没反应」并反复点击。即使后续动作失败也要先应答。
 */
export async function handleTopicCallback(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const data = ctx.callbackQuery?.data;
  if (!data) return;

  const parsed = parseTopicCallback(data);
  if (!parsed) return;

  await ctx.answerCallbackQuery().catch(() => undefined);

  const topic = await findTopicById(parsed.topicId);
  if (!topic || topic.botId !== deps.botId) {
    await ctx.answerCallbackQuery({ text: '这个会话已经不存在了', show_alert: true }).catch(
      () => undefined,
    );
    return;
  }

  const contact = await findContactById(topic.contactId);
  if (!contact) return;

  const actor = ctx.from?.username ?? String(ctx.from?.id ?? 'admin');

  switch (parsed.action) {
    case 'close':
      await doClose(deps, topic, actor);
      break;
    case 'reopen':
      await doReopen(deps, topic, actor);
      break;
    case 'ban':
      await doBan(deps, topic, contact, actor);
      break;
    case 'unban':
      await doUnban(deps, topic, contact, actor);
      break;
    case 'reset':
      await doReset(deps, topic, contact, actor);
      break;
    default:
      logger.debug({ action: parsed.action }, '未知的按钮回调');
      break;
  }
}

/**
 * 私聊里的命令分流。返回值是三态的，因为三种情况的后继动作完全不同：
 *   - `'start'`   → 交回 pipeline 发欢迎语并建档
 *   - `'handled'` → 已经处理完，结束
 *   - `'pass'`    → 不是我们认识的命令，当作普通消息走中继
 *
 * 最后一种很重要：用户发 `/price`、`/订单123` 这类文本非常常见，
 * 一刀切当成「未知命令」忽略掉，用户会觉得机器人坏了。
 */
export async function handleUserCommand(
  ctx: Context,
  deps: RuntimeDeps,
): Promise<'start' | 'handled' | 'pass'> {
  const text = ctx.message?.text;
  if (!text || !text.startsWith('/')) return 'pass';

  const command = (text.split(/[\s@]/)[0] ?? '').toLowerCase();
  const from = ctx.from;
  if (!from) return 'handled';

  if (command === '/start') return 'start';

  if (command === '/help') {
    await sendToUser(
      deps.api,
      deps.botId,
      from.id,
      '直接发送消息即可 —— 管理员会在后台看到并回复你。\n\n支持发送文字、图片、语音、文件与相册。',
    );
    return 'handled';
  }

  return 'pass';
}
