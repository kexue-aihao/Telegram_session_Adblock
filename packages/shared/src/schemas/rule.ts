import { z } from 'zod';
import { MATCH_MODES, RULE_ACTIONS, RULE_TARGETS } from '../constants.ts';

export const adRuleSchema = z.object({
  id: z.number().int(),
  /** null = 全局规则，对所有机器人生效 */
  botId: z.number().int().nullable(),
  name: z.string(),
  pattern: z.string(),
  flags: z.string(),
  matchMode: z.enum(MATCH_MODES),
  target: z.enum(RULE_TARGETS),
  action: z.enum(RULE_ACTIONS),
  /** 命中后累加的违规分 */
  severity: z.number().int(),
  /** 越小越先匹配 */
  priority: z.number().int(),
  isEnabled: z.boolean(),
  /** 预置规则，允许停用但不建议删除 */
  isSystem: z.boolean(),
  note: z.string().nullable(),
  hitCount: z.number().int(),
  lastHitAt: z.number().int().nullable(),
  createdAt: z.number().int(),
  updatedAt: z.number().int(),
});
export type AdRule = z.infer<typeof adRuleSchema>;

export const createRuleInputSchema = z.object({
  name: z.string().trim().min(1).max(100),
  pattern: z.string().min(1).max(2000),
  flags: z
    .string()
    .max(10)
    .regex(/^[gimsuy]*$/, '只允许 g i m s u y 这几个标志')
    .default('iu'),
  matchMode: z.enum(MATCH_MODES).default('regex'),
  target: z.enum(RULE_TARGETS).default('all'),
  action: z.enum(RULE_ACTIONS).default('delete'),
  severity: z.number().int().min(0).max(1000).default(10),
  priority: z.number().int().min(0).max(10_000).default(100),
  isEnabled: z.boolean().default(true),
  botId: z.number().int().positive().nullable().default(null),
  note: z.string().max(500).nullable().default(null),
});
export type CreateRuleInput = z.infer<typeof createRuleInputSchema>;

export const updateRuleInputSchema = createRuleInputSchema.partial().omit({ botId: true });
export type UpdateRuleInput = z.infer<typeof updateRuleInputSchema>;

export const reorderRulesInputSchema = z.object({
  /** 按新顺序排列的规则 id，服务端按下标重写 priority */
  orderedIds: z.array(z.number().int().positive()).min(1).max(500),
});
export type ReorderRulesInput = z.infer<typeof reorderRulesInputSchema>;

/** 沙盒测试入参 —— 让管理员在保存前就看到命中效果 */
export const ruleTestInputSchema = z.object({
  pattern: z.string().min(1).max(2000),
  flags: z.string().max(10).default('iu'),
  matchMode: z.enum(MATCH_MODES).default('regex'),
  /** 待测样本 */
  sample: z.string().max(20_000),
});
export type RuleTestInput = z.infer<typeof ruleTestInputSchema>;

export const ruleTestMatchSchema = z.object({
  start: z.number().int(),
  end: z.number().int(),
  text: z.string(),
  groups: z.array(z.string().nullable()),
});
export type RuleTestMatch = z.infer<typeof ruleTestMatchSchema>;

export const ruleTestResultSchema = z.object({
  ok: z.boolean(),
  /** 语法错误或 safe-regex 拒绝的原因 */
  error: z.string().nullable(),
  /** safe-regex 静态校验结果 */
  safeRegex: z.object({
    safe: z.boolean(),
    reason: z.string().nullable(),
  }),
  matches: z.array(ruleTestMatchSchema),
  /** 命中数超过上限被截断 */
  truncated: z.boolean(),
  /** 匹配超时（疑似 ReDoS），此时 matches 为空 */
  timedOut: z.boolean(),
  durationMs: z.number(),
  /** 归一化后的文本，便于解释「为什么这条命中了」 */
  normalizedSample: z.string().nullable(),
});
export type RuleTestResult = z.infer<typeof ruleTestResultSchema>;
