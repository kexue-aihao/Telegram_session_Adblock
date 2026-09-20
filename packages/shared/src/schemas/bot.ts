import { z } from 'zod';
import { BOT_HEALTH_STATUSES } from '../constants.ts';
import { botSettingsSchema } from './settings.ts';

/**
 * 机器人在面板上的表示。
 *
 * 注意这里**没有 token 字段** —— 明文 token 在任何接口都不会回传，
 * 只给出 `tokenMask`（例如 `123456789:AAF...xY3`）供管理员辨认。
 */
export const botSchema = z.object({
  id: z.number().int(),
  name: z.string(),
  username: z.string(),
  tokenMask: z.string(),
  /** 由 getMe 得到的机器人自身 user id */
  telegramId: z.number().int().nullable(),
  adminGroupId: z.number().int().nullable(),
  adminGroupTitle: z.string().nullable(),
  isEnabled: z.boolean(),
  healthStatus: z.enum(BOT_HEALTH_STATUSES),
  lastError: z.string().nullable(),
  lastPolledAt: z.number().int().nullable(),
  createdAt: z.number().int(),
  updatedAt: z.number().int(),
});
export type Bot = z.infer<typeof botSchema>;

/** 创建机器人：粘贴 BotFather 给的 token 即可 */
export const createBotInputSchema = z.object({
  token: z
    .string()
    .trim()
    .regex(/^\d{6,}:[A-Za-z0-9_-]{30,}$/, '这不像是 BotFather 签发的 token 格式'),
  /** 管理群（超级群）ID，形如 -1001234567890；可在向导第二步再补 */
  adminGroupId: z.number().int().min(-1_000_000_000_000).max(-1).nullable().default(null),
  name: z.string().trim().min(1).max(64).optional(),
});
export type CreateBotInput = z.infer<typeof createBotInputSchema>;

export const updateBotInputSchema = z.object({
  name: z.string().trim().min(1).max(64).optional(),
  adminGroupId: z.number().int().min(-1_000_000_000_000).max(-1).nullable().optional(),
  isEnabled: z.boolean().optional(),
  /** 重新录入 token（例如换了 MASTER_KEY 之后） */
  token: z
    .string()
    .trim()
    .regex(/^\d{6,}:[A-Za-z0-9_-]{30,}$/, 'token 格式不正确')
    .optional(),
});
export type UpdateBotInput = z.infer<typeof updateBotInputSchema>;

/** 校验 token 的返回值 —— 对应向导里的「测试连接」 */
export const botValidationSchema = z.object({
  ok: z.boolean(),
  telegramId: z.number().int().nullable(),
  name: z.string().nullable(),
  username: z.string().nullable(),
  canJoinGroups: z.boolean().nullable(),
  canReadAllGroupMessages: z.boolean().nullable(),
  error: z.string().nullable(),
});
export type BotValidation = z.infer<typeof botValidationSchema>;

/** 管理群体检结果 —— 直接告诉管理员缺哪一项权限 */
export const groupCheckSchema = z.object({
  ok: z.boolean(),
  chatId: z.number().int(),
  title: z.string().nullable(),
  isForum: z.boolean(),
  isAdmin: z.boolean(),
  canManageTopics: z.boolean(),
  canDeleteMessages: z.boolean(),
  canRestrictMembers: z.boolean(),
  /** 面向用户的、可执行的修复建议 */
  problems: z.array(z.string()),
});
export type GroupCheck = z.infer<typeof groupCheckSchema>;

export const botDetailSchema = z.object({
  bot: botSchema,
  settings: botSettingsSchema,
});
export type BotDetail = z.infer<typeof botDetailSchema>;
