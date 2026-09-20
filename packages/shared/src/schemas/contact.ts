import { z } from 'zod';

/** 终端用户（私聊机器人的人） */
export const contactSchema = z.object({
  id: z.number().int(),
  botId: z.number().int(),
  tgUserId: z.number().int(),
  username: z.string().nullable(),
  firstName: z.string().nullable(),
  lastName: z.string().nullable(),
  languageCode: z.string().nullable(),
  /** 是否被拉黑 */
  isBlocked: z.boolean(),
  /** 用户是否把机器人屏蔽了（发送时报 403 后置位） */
  isUnreachable: z.boolean(),
  /** 违规分，规则命中时按 severity 累加，按 settings.escalation.decayDays 衰减 */
  violationScore: z.number().int(),
  lastViolationAt: z.number().int().nullable(),
  firstSeenAt: z.number().int(),
  lastSeenAt: z.number().int(),
  notes: z.string().nullable(),
});
export type Contact = z.infer<typeof contactSchema>;

export const updateContactInputSchema = z.object({
  notes: z.string().max(4000).nullable().optional(),
});
export type UpdateContactInput = z.infer<typeof updateContactInputSchema>;

/** 处罚记录 */
export const sanctionSchema = z.object({
  id: z.number().int(),
  botId: z.number().int(),
  contactId: z.number().int(),
  type: z.enum(['warn', 'silence', 'mute', 'ban']),
  reason: z.enum(['rule_hit', 'manual', 'flood', 'escalation']),
  ruleId: z.number().int().nullable(),
  expiresAt: z.number().int().nullable(),
  isActive: z.boolean(),
  createdBy: z.string().nullable(),
  createdAt: z.number().int(),
  liftedAt: z.number().int().nullable(),
});
export type Sanction = z.infer<typeof sanctionSchema>;

/** 当前生效的处罚快照，用于会话详情页顶部横幅 */
export const activeSanctionsSchema = z.object({
  contact: contactSchema,
  active: z.array(sanctionSchema),
  /** 下一档处罚的门槛，用于「再违规 N 分将被禁言」这类提示 */
  nextStep: z
    .object({
      atScore: z.number().int(),
      type: z.enum(['warn', 'silence', 'mute', 'ban']),
      remaining: z.number().int(),
    })
    .nullable(),
});
export type ActiveSanctions = z.infer<typeof activeSanctionsSchema>;
