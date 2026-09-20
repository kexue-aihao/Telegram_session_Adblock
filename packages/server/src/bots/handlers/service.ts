import type { Context } from 'grammy';
import { logger } from '../../core/logger.ts';
import { getSessionSummary } from '../../sessions.ts';
import { publish } from '../../core/bus.ts';
import { channel } from '@tgs/shared';
import { findBySource, markEdited } from '../relay.ts';
import { findTopicById } from '../topics.ts';
import type { RuntimeDeps } from '../deps.ts';

/**
 * 服务消息与编辑镜像。
 *
 * 关于「删除镜像」的一个硬事实：Bot API **不推送** `message_deleted`
 * 这类更新，机器人永远不可能知道用户或管理员在 Telegram 客户端里删了哪条消息。
 * 所以删除只能在本方主动删除时镜像（见 rules/actions.ts）。
 * 这不是实现疏漏，是平台限制，面板上也据此提示管理员。
 */

/** 编辑消息的镜像：把新文本同步到对面 */
export async function handleEditedMessage(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const message = ctx.editedMessage;
  if (!message || !message.chat) return;

  const settings = await deps.getSettings();
  if (!settings.mirrorEdits) return;

  const mapped = await findBySource(message.chat.id, message.message_id);
  if (!mapped || mapped.botId !== deps.botId) return;
  if (mapped.destChatId === null || mapped.relayedMessageId === null) return;

  const isPrivate = message.chat.type === 'private';
  const text = message.text ?? null;
  const caption = message.caption ?? null;

  // 纯媒体消息没有可编辑的文本，Telegram 侧改的只是文件本身 —— 镜像不了。
  if (text === null && caption === null) return;

  try {
    if (text !== null) {
      await deps.api.editMessageText(mapped.destChatId, mapped.relayedMessageId, text);
    } else if (caption !== null) {
      await deps.api.editMessageCaption(mapped.destChatId, mapped.relayedMessageId, { caption });
    }
  } catch (err) {
    // 常见原因：目标消息太旧、或内容与原来完全相同（Telegram 会报
    // "message is not modified"）。两者都不值得打断流程。
    logger.debug({ err, messageRowId: mapped.id }, '镜像编辑失败');
    return;
  }

  await markEdited(mapped, text, caption);

  const topic = await findTopicById(mapped.topicId);
  if (topic) {
    const summary = await getSessionSummary(topic.id);
    if (summary) publish(channel.bot(deps.botId), 'session.updated', summary);
  }

  logger.debug({ messageRowId: mapped.id, isPrivate }, '已镜像编辑');
}

/**
 * 机器人在管理群里的成员状态变化。
 *
 * 记录它是因为一个非常实际的运维问题：机器人被移出管理群后，
 * 所有中继都会静默失败，管理员却只会看到「面板上一切正常」。
 */
export async function handleMyChatMember(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const update = ctx.myChatMember;
  if (!update) return;

  const status = update.new_chat_member.status;
  const chatId = update.chat.id;

  if (chatId !== deps.adminGroupId) return;

  if (status === 'left' || status === 'kicked') {
    logger.error(
      { botId: deps.botId, chatId, status },
      '机器人已被移出管理群 —— 中继将全部失败，请在面板中重新配置',
    );
  } else if (status === 'administrator' || status === 'member') {
    logger.info({ botId: deps.botId, chatId, status }, '机器人在管理群中的状态已更新');
  }
}

/** 机器人在管理群里被提升/降权，或群被升级为超级群 */
export async function handleChatMemberUpdated(ctx: Context, deps: RuntimeDeps): Promise<void> {
  const update = ctx.chatMember;
  if (!update) return;
  if (update.chat.id !== deps.adminGroupId) return;

  // 只关心机器人自己的权限变化
  if (update.new_chat_member.user.id !== deps.botTgId) return;

  // `can_manage_topics` / `can_delete_messages` 只存在于管理员这一变体上，
  // 成员、受限成员、被封禁者都没有这些字段 —— 必须先收窄联合类型。
  const member = update.new_chat_member;
  const isAdmin = member.status === 'administrator' || member.status === 'creator';
  const canManageTopics = isAdmin && member.status === 'administrator' && member.can_manage_topics === true;
  const canDelete = isAdmin && member.status === 'administrator' && member.can_delete_messages === true;

  if (!canManageTopics || !canDelete) {
    logger.warn(
      { botId: deps.botId, canManageTopics, canDelete },
      '机器人在管理群的权限不足，话题创建或消息删除会失败',
    );
  }
}
