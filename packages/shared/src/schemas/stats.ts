import { z } from 'zod';

export const statsOverviewSchema = z.object({
  bots: z.object({
    total: z.number().int(),
    online: z.number().int(),
    error: z.number().int(),
    disabled: z.number().int(),
  }),
  sessions: z.object({
    open: z.number().int(),
    closed: z.number().int(),
    createdToday: z.number().int(),
  }),
  contacts: z.object({
    total: z.number().int(),
    blocked: z.number().int(),
    flagged: z.number().int(),
  }),
  messages: z.object({
    inToday: z.number().int(),
    outToday: z.number().int(),
    total: z.number().int(),
  }),
  ads: z.object({
    blockedToday: z.number().int(),
    blocked24h: z.number().int(),
    blockedTotal: z.number().int(),
  }),
  rules: z.object({
    total: z.number().int(),
    enabled: z.number().int(),
    /** 因正则超时被自动停用的规则数 —— 值得管理员关注 */
    autoDisabled: z.number().int(),
  }),
  /** 环比昨日的变化百分比；null 表示昨日无数据，无法比较 */
  deltas: z.object({
    messagesIn: z.number().nullable(),
    adsBlocked: z.number().nullable(),
    sessionsCreated: z.number().nullable(),
  }),
  generatedAt: z.number().int(),
});
export type StatsOverview = z.infer<typeof statsOverviewSchema>;

export const timeseriesPointSchema = z.object({
  /** YYYY-MM-DD */
  date: z.string(),
  messagesIn: z.number().int(),
  messagesOut: z.number().int(),
  topicsCreated: z.number().int(),
  adsBlocked: z.number().int(),
});
export type TimeseriesPoint = z.infer<typeof timeseriesPointSchema>;

export const timeseriesQuerySchema = z.object({
  days: z.coerce.number().int().min(1).max(90).default(14),
  botId: z.coerce.number().int().positive().optional(),
});
export type TimeseriesQuery = z.infer<typeof timeseriesQuerySchema>;

export const timeseriesSchema = z.object({
  points: z.array(timeseriesPointSchema),
});
export type Timeseries = z.infer<typeof timeseriesSchema>;
