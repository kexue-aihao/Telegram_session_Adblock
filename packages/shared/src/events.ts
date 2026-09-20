import type { z } from 'zod';
import type { ruleHitSchema } from './schemas/audit.ts';
import type { botSchema } from './schemas/bot.ts';
import type { relayedMessageSchema, sessionSummarySchema } from './schemas/message.ts';
import type { statsOverviewSchema } from './schemas/stats.ts';

/**
 * WebSocket 事件契约 —— 前后端各持一半，任何一侧改字段都会立刻类型报错。
 *
 * 设计取舍：所有事件共用一个信封 `{ type, seq, ts, payload }`，
 * 而不是把 type 平铺进 payload。这样前端可以用一个 reducer 统一处理，
 * 且 `seq` 让我们能检测到丢包（重连后 seq 不连续即触发一次全量刷新）。
 */

/** 面板 → 服务端的订阅意图；服务端不处理其余入站帧 */
export const WS_CLIENT_EVENTS = ['subscribe', 'unsubscribe', 'ping'] as const;
export type WsClientEvent = (typeof WS_CLIENT_EVENTS)[number];

export interface WsClientFrame {
  type: WsClientEvent;
  /** subscribe 时指定要订阅的频道，例如 `topic:42` 或 `bot:3` */
  channel?: string;
}

/**
 * 服务端 → 面板的事件名。
 *
 * `stats.tick` 是唯一按固定节奏推送的事件（低频，仅用于仪表盘刷新），
 * 其余全部由真实业务动作触发 —— 轮询会浪费带宽且让实时感变假。
 */
export const WS_SERVER_EVENTS = [
  'bot.status',
  'session.created',
  'session.updated',
  'session.deleted',
  'message.new',
  'message.updated',
  'message.deleted',
  'rule.hit',
  'audit.new',
  'alert.new',
  'stats.tick',
  'hello',
] as const;
export type WsServerEvent = (typeof WS_SERVER_EVENTS)[number];

/** 管理群话题内的告警卡片（规则命中或用户不可达） */
export interface AlertPayload {
  id: string;
  botId: number;
  sessionId: number | null;
  level: 'info' | 'warn' | 'danger';
  title: string;
  detail: string;
  createdAt: number;
}

export interface HelloPayload {
  /** 服务启动时刻，用于前端判断是否需要丢弃陈旧缓存 */
  serverStartedAt: number;
  version: string;
  /** 当前在线的机器人数量 */
  onlineBots: number;
}

/** 事件名 → payload 类型的映射表，是整条实时链路的唯一事实源 */
export interface WsServerPayloads {
  'bot.status': z.infer<typeof botSchema>;
  'session.created': z.infer<typeof sessionSummarySchema>;
  'session.updated': z.infer<typeof sessionSummarySchema>;
  'session.deleted': { sessionId: number; botId: number };
  'message.new': z.infer<typeof relayedMessageSchema>;
  'message.updated': z.infer<typeof relayedMessageSchema>;
  'message.deleted': { messageId: number; topicId: number };
  'rule.hit': z.infer<typeof ruleHitSchema>;
  'audit.new': { id: number; action: string; actorType: string; createdAt: number };
  'alert.new': AlertPayload;
  'stats.tick': z.infer<typeof statsOverviewSchema>;
  hello: HelloPayload;
}

export type WsServerFrame<K extends WsServerEvent = WsServerEvent> = {
  [E in K]: {
    type: E;
    seq: number;
    ts: number;
    /**
     * 该事件所属的频道。前端按频道订阅，只有匹配的帧才会被投递到对应页面。
     * 全局事件（stats.tick / bot.status / audit.new）用 `*`。
     */
    channel: string;
    payload: WsServerPayloads[E];
  };
}[K];

/** 频道命名 —— 单一函数生成，避免前后端手写字符串时拼错 */
export const channel = {
  global: '*',
  bot: (botId: number) => `bot:${botId}`,
  topic: (topicId: number) => `topic:${topicId}`,
} as const;

export type WsChannel = string;
