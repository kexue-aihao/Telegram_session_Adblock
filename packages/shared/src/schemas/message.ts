import { z } from 'zod';
import { CONTENT_TYPES, MESSAGE_DIRECTIONS, TOPIC_STATUSES } from '../constants.ts';

export const messageMediaSchema = z.object({
  kind: z.string(),
  fileId: z.string(),
  fileUniqueId: z.string(),
  /** 相册里的顺序 */
  position: z.number().int().default(0),
});
export type MessageMedia = z.infer<typeof messageMediaSchema>;

/** 中继消息的内容。媒体只回 file_id，前端按需向 /api/media 换取临时预览地址。 */
export const messageContentSchema = z.object({
  type: z.enum(CONTENT_TYPES),
  text: z.string().nullable(),
  caption: z.string().nullable(),
  media: z.array(messageMediaSchema).default([]),
  mediaGroupId: z.string().nullable(),
  /** 是否检测到 entity 里的隐藏链接（广告最常用的藏链接手法） */
  hasHiddenLink: z.boolean().default(false),
  /** 隐藏链接的实际 URL，已脱敏展示 */
  hiddenLinks: z.array(z.string()).default([]),
});
export type MessageContent = z.infer<typeof messageContentSchema>;

/**
 * 面板聊天视图里的一条中继消息。
 * `tgMessageId` 是它在**源聊天**里的 id，用于「跳转到 Telegram」。
 */
export const relayedMessageSchema = z.object({
  id: z.number().int(),
  topicId: z.number().int(),
  direction: z.enum(MESSAGE_DIRECTIONS),
  content: messageContentSchema,
  isDeleted: z.boolean(),
  editedAt: z.number().int().nullable(),
  createdAt: z.number().int(),
  /** 该消息触发了规则的话，关联到命中记录 */
  ruleHitId: z.number().int().nullable(),
  /** admin_to_user 方向才有：发这条消息的管理员显示名 */
  senderLabel: z.string().nullable(),
});
export type RelayedMessage = z.infer<typeof relayedMessageSchema>;

/** 面板以管理员身份回消息 */
export const sendMessageInputSchema = z.object({
  text: z.string().trim().min(1).max(4096),
});
export type SendMessageInput = z.infer<typeof sendMessageInputSchema>;

/** 会话 = 一个话题 + 一个联系人 */
export const sessionSummarySchema = z.object({
  id: z.number().int(),
  botId: z.number().int(),
  botName: z.string(),
  botUsername: z.string(),
  contactId: z.number().int(),
  displayName: z.string(),
  username: z.string().nullable(),
  tgUserId: z.number().int(),
  threadId: z.number().int(),
  title: z.string(),
  status: z.enum(TOPIC_STATUSES),
  lastMessageAt: z.number().int().nullable(),
  lastMessagePreview: z.string().nullable(),
  lastMessageDirection: z.enum(MESSAGE_DIRECTIONS).nullable(),
  messageCount: z.number().int(),
  violationScore: z.number().int(),
  isBlocked: z.boolean(),
  createdAt: z.number().int(),
  closedAt: z.number().int().nullable(),
});
export type SessionSummary = z.infer<typeof sessionSummarySchema>;

export const sessionDetailSchema = z.object({
  session: sessionSummarySchema,
  activeSanctions: z.array(
    z.object({
      id: z.number().int(),
      type: z.enum(['warn', 'silence', 'mute', 'ban']),
      reason: z.enum(['rule_hit', 'manual', 'flood', 'escalation']),
      expiresAt: z.number().int().nullable(),
      createdAt: z.number().int(),
    }),
  ),
  notes: z.string().nullable(),
  recentHits: z.array(
    z.object({
      id: z.number().int(),
      ruleName: z.string(),
      matchedText: z.string(),
      outcome: z.string(),
      createdAt: z.number().int(),
    }),
  ),
});
export type SessionDetail = z.infer<typeof sessionDetailSchema>;

export const sessionQuerySchema = z.object({
  botId: z.coerce.number().int().positive().optional(),
  status: z.enum(TOPIC_STATUSES).optional(),
  /** 搜索用户名 / 昵称 / 话题标题 */
  q: z.string().trim().max(100).optional(),
  /** 只看有处罚记录的 */
  flaggedOnly: z.coerce.boolean().optional(),
  cursor: z.coerce.number().int().optional(),
  limit: z.coerce.number().int().min(1).max(100).default(30),
});
export type SessionQuery = z.infer<typeof sessionQuerySchema>;

export const messagePageQuerySchema = z.object({
  cursor: z.coerce.number().int().optional(),
  limit: z.coerce.number().int().min(1).max(200).default(50),
});
export type MessagePageQuery = z.infer<typeof messagePageQuerySchema>;
