import { z } from 'zod';
import { ACTOR_TYPES, HIT_OUTCOMES } from '../constants.ts';

/**
 * 一次规则命中 —— 广告审计的核心记录。
 *
 * 刻意把 `rulePattern` / `ruleFlags` 冗余存下来：规则可能事后被改甚至被删，
 * 但审计记录必须永远能还原「当时是按什么规则判定的」。
 */
export const ruleHitSchema = z.object({
  id: z.number().int(),
  ruleId: z.number().int().nullable(),
  ruleName: z.string(),
  rulePattern: z.string(),
  ruleFlags: z.string(),
  botId: z.number().int(),
  botName: z.string(),
  contactId: z.number().int(),
  contactName: z.string(),
  contactUsername: z.string().nullable(),
  tgUserId: z.number().int(),
  topicId: z.number().int().nullable(),
  threadId: z.number().int().nullable(),
  /** 命中的原始文本片段 */
  matchedText: z.string(),
  /** 归一化后的上下文片段，用于解释「为什么这也能命中」 */
  normalizedExcerpt: z.string().nullable(),
  /** 实际执行了哪些动作 */
  outcomes: z.array(z.enum(HIT_OUTCOMES)),
  severity: z.number().int(),
  createdAt: z.number().int(),
});
export type RuleHit = z.infer<typeof ruleHitSchema>;

export const ruleHitQuerySchema = z.object({
  botId: z.coerce.number().int().positive().optional(),
  ruleId: z.coerce.number().int().positive().optional(),
  contactId: z.coerce.number().int().positive().optional(),
  outcome: z.enum(HIT_OUTCOMES).optional(),
  /** 命中文本 / 用户名 模糊搜索 */
  q: z.string().trim().max(100).optional(),
  from: z.coerce.number().int().optional(),
  to: z.coerce.number().int().optional(),
  cursor: z.coerce.number().int().optional(),
  limit: z.coerce.number().int().min(1).max(100).default(30),
});
export type RuleHitQuery = z.infer<typeof ruleHitQuerySchema>;

/** 面板操作审计（登录、改规则、拉黑……），与规则命中分开存放 */
export const auditLogEntrySchema = z.object({
  id: z.number().int(),
  actorType: z.enum(ACTOR_TYPES),
  actorId: z.string().nullable(),
  action: z.string(),
  targetType: z.string().nullable(),
  targetId: z.string().nullable(),
  detail: z.record(z.string(), z.unknown()).nullable(),
  ip: z.string().nullable(),
  createdAt: z.number().int(),
});
export type AuditLogEntry = z.infer<typeof auditLogEntrySchema>;

export const auditLogQuerySchema = z.object({
  action: z.string().trim().max(64).optional(),
  actorType: z.enum(ACTOR_TYPES).optional(),
  q: z.string().trim().max(100).optional(),
  from: z.coerce.number().int().optional(),
  to: z.coerce.number().int().optional(),
  cursor: z.coerce.number().int().optional(),
  limit: z.coerce.number().int().min(1).max(100).default(30),
});
export type AuditLogQuery = z.infer<typeof auditLogQuerySchema>;

/** 统一的游标分页响应 */
export function paginatedSchema<T extends z.ZodType>(item: T) {
  return z.object({
    items: z.array(item),
    nextCursor: z.number().int().nullable(),
    total: z.number().int().nullable(),
  });
}
export type Paginated<T> = {
  items: T[];
  nextCursor: number | null;
  total: number | null;
};
