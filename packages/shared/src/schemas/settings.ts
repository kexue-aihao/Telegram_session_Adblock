import { z } from 'zod';
import {
  MATCH_MODES,
  RULE_ACTIONS,
  RULE_TARGETS,
  SANCTION_TYPES,
} from '../constants.ts';

/** createForumTopic 的 icon_color 只接受这几个固定值（Bot API 限制） */
export const FORUM_TOPIC_ICON_COLORS = [
  0x6fb9f0, // 蓝
  0xffd67e, // 黄
  0xcb86db, // 紫
  0x8eee98, // 绿
  0xff93b2, // 粉
  0xfb6f5f, // 红
] as const;

/**
 * 阶梯处罚的一级。
 *
 * `atScore` 是「违规分达到该值时触发」。违规分由规则命中的 severity 累加，
 * 并按 `decayDays` 衰减，避免用户被永久钉在最高档。
 */
export const escalationStepSchema = z.object({
  id: z.string().min(1).max(64),
  atScore: z.number().int().min(1).max(10_000),
  type: z.enum(SANCTION_TYPES),
  /** 仅 mute 使用；null 表示永久 */
  durationMinutes: z.number().int().min(1).max(525_600).nullable().default(null),
  deleteMessage: z.boolean().default(true),
  notifyAdmins: z.boolean().default(true),
  enabled: z.boolean().default(true),
});
export type EscalationStep = z.infer<typeof escalationStepSchema>;

export const escalationConfigSchema = z.object({
  steps: z.array(escalationStepSchema).min(1).max(20),
  /** 多少天无违规后违规分减半；null = 永不衰减 */
  decayDays: z.number().int().min(1).max(3650).nullable().default(7),
  /** 违规分上限，防止无限累加 */
  maxScore: z.number().int().min(1).max(100_000).default(999),
});
export type EscalationConfig = z.infer<typeof escalationConfigSchema>;

/** 单个机器人的全部可调配置 */
export const botSettingsSchema = z.object({
  botId: z.number().int().positive(),

  // —— 话题 ——
  topicNameTemplate: z.string().min(1).max(128),
  topicIconColor: z
    .number()
    .int()
    .refine((v) => (FORUM_TOPIC_ICON_COLORS as readonly number[]).includes(v), {
      message: '不是合法的 Telegram 话题图标颜色',
    }),
  /** 话题静默多少小时后自动关闭；null = 不自动关闭 */
  autoCloseHours: z.number().int().min(1).max(8760).nullable().default(72),
  /** 为话题置顶一条含用户信息与快捷操作按钮的头部消息 */
  pinTopicHeader: z.boolean().default(true),

  // —— 文案模板 ——
  greetingText: z.string().max(4096),
  warnTemplate: z.string().max(4096),
  muteTemplate: z.string().max(4096),
  banTemplate: z.string().max(4096),
  silenceTemplate: z.string().max(4096),
  alertCardTemplate: z.string().max(4096),
  topicHeaderTemplate: z.string().max(4096),

  // —— 规则与处罚 ——
  rulesEnabled: z.boolean().default(true),
  notifyAdmins: z.boolean().default(true),
  escalation: escalationConfigSchema,
  /** 命中后是否撤回用户私聊里的原消息（Bot API 允许删私聊 incoming 消息） */
  deleteOriginMessage: z.boolean().default(true),

  // —— 中继行为 ——
  mirrorEdits: z.boolean().default(true),
  mirrorDeletes: z.boolean().default(true),
  /** 同一用户连发消息的合并窗口（毫秒）；0 = 不合并 */
  coalesceWindowMs: z.number().int().min(0).max(5000).default(400),
  /** 窗口内超过该条数才触发合并，避免把正常对话合并掉 */
  coalesceThreshold: z.number().int().min(2).max(50).default(5),
  /** 用户 3 秒内连发超过该条数时视为刷屏 */
  floodThreshold: z.number().int().min(3).max(100).default(8),
  /** 用户拉黑机器人（发送 403）时，是否在话题内提示管理员 */
  notifyOnUnreachable: z.boolean().default(true),
});
export type BotSettings = z.infer<typeof botSettingsSchema>;

/** 全局设置（跨机器人） */
export const globalSettingsSchema = z.object({
  /** 面板时区，仅影响展示 */
  timezone: z.string().min(1).max(64).default('Asia/Shanghai'),
  /** 审计日志保留天数；null = 永久 */
  auditRetentionDays: z.number().int().min(1).max(3650).nullable().default(180),
  /** 规则命中后自动禁用超时正则 */
  autoDisableOnRegexTimeout: z.boolean().default(true),
  /** 正则单次匹配的硬超时（毫秒） */
  regexTimeoutMs: z.number().int().min(5).max(2000).default(50),
});
export type GlobalSettings = z.infer<typeof globalSettingsSchema>;

/** 规则的可视化匹配配置（用于 WebUI 沙盒回显） */
export const ruleMatcherSchema = z.object({
  pattern: z.string().min(1).max(2000),
  flags: z.string().max(10).default('iu'),
  matchMode: z.enum(MATCH_MODES).default('regex'),
  target: z.enum(RULE_TARGETS).default('all'),
  action: z.enum(RULE_ACTIONS).default('delete'),
  /** 命中后累加的违规分 */
  severity: z.number().int().min(0).max(1000).default(10),
});
export type RuleMatcher = z.infer<typeof ruleMatcherSchema>;
