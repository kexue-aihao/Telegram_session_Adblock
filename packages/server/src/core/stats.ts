import { and, eq, gte, lte, sql } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { statsDaily } from '../db/schema.ts';
import { loadEnv } from '../env.ts';
import { logger } from './logger.ts';

/**
 * 按日聚合的统计。
 *
 * 仪表盘上的折线图如果每次刷新都去 `messages` 表上扫全表，这个面板会随着
 * 使用时间推移越来越慢 —— 而它恰恰是运维每天都要打开的第一屏。
 * 所以在写入端就把计数累加好，读的时候只查几十行。
 */

export interface StatDelta {
  messagesIn?: number;
  messagesOut?: number;
  topicsCreated?: number;
  adsBlocked?: number;
}

/** 汇总行的 botId（0 是保留值，真实机器人 id 从 1 自增） */
export const GLOBAL_BOT_ID = 0;

/** 面板时区下的 YYYY-MM-DD；统计必须按运维看到的「今天」切日，而不是 UTC */
export function todayInTz(tz = loadEnv().TZ): string {
  // sv-SE 的日期格式恰好是 ISO 的 YYYY-MM-DD，省掉手工拼装
  return new Date().toLocaleDateString('sv-SE', { timeZone: tz });
}

async function bumpRow(date: string, botId: number, delta: StatDelta): Promise<void> {
  const { db } = getDb();

  // onConflictDoUpdate + 列自增是这里的关键：先读后写在并发下会丢计数，
  // 而中继路径上并发是常态（runner 并发处理更新）。
  await db
    .insert(statsDaily)
    .values({
      date,
      botId,
      messagesIn: delta.messagesIn ?? 0,
      messagesOut: delta.messagesOut ?? 0,
      topicsCreated: delta.topicsCreated ?? 0,
      adsBlocked: delta.adsBlocked ?? 0,
    })
    .onConflictDoUpdate({
      target: [statsDaily.date, statsDaily.botId],
      set: {
        messagesIn: sql`${statsDaily.messagesIn} + ${delta.messagesIn ?? 0}`,
        messagesOut: sql`${statsDaily.messagesOut} + ${delta.messagesOut ?? 0}`,
        topicsCreated: sql`${statsDaily.topicsCreated} + ${delta.topicsCreated ?? 0}`,
        adsBlocked: sql`${statsDaily.adsBlocked} + ${delta.adsBlocked ?? 0}`,
      },
    });
}

/**
 * 记一次业务事件。
 *
 * 同时写两行：该机器人自己的行，以及 botId=0 的全局汇总行。
 * 这样「单个机器人趋势」与「全站趋势」都是一次索引扫描，不需要在读的时候
 * 对 N 个机器人的行做 GROUP BY。
 */
export async function bumpStats(botId: number, delta: StatDelta): Promise<void> {
  const date = todayInTz();
  try {
    await bumpRow(date, botId, delta);
    await bumpRow(date, GLOBAL_BOT_ID, delta);
  } catch (err) {
    // 统计是旁路。写不进去只记日志，绝不能让中继失败。
    logger.error({ err, botId, delta }, '写入日统计失败');
  }
}

export interface TimeseriesRow {
  date: string;
  messagesIn: number;
  messagesOut: number;
  topicsCreated: number;
  adsBlocked: number;
}

/** 取最近 N 天的折线数据；缺失的日期补 0，否则前端画出来的线会断 */
export async function getTimeseries(days: number, botId = GLOBAL_BOT_ID): Promise<TimeseriesRow[]> {
  const { db } = getDb();
  const tz = loadEnv().TZ;
  const today = todayInTz(tz);

  // 用「今天往前推 N-1 天」的方式算起点，避免跨月跨年时手工计算
  const startDate = new Date(`${today}T00:00:00Z`);
  startDate.setUTCDate(startDate.getUTCDate() - (days - 1));
  const start = startDate.toISOString().slice(0, 10);

  const rows = await db
    .select()
    .from(statsDaily)
    .where(and(eq(statsDaily.botId, botId), gte(statsDaily.date, start), lte(statsDaily.date, today)));

  const byDate = new Map(rows.map((row) => [row.date, row]));

  const series: TimeseriesRow[] = [];
  for (let i = 0; i < days; i += 1) {
    const cursor = new Date(`${start}T00:00:00Z`);
    cursor.setUTCDate(cursor.getUTCDate() + i);
    const date = cursor.toISOString().slice(0, 10);
    const row = byDate.get(date);
    series.push({
      date,
      messagesIn: row?.messagesIn ?? 0,
      messagesOut: row?.messagesOut ?? 0,
      topicsCreated: row?.topicsCreated ?? 0,
      adsBlocked: row?.adsBlocked ?? 0,
    });
  }

  return series;
}

/** 今日相对昨日的百分比变化；昨日为 0 时返回 null（无法比较，而不是显示 +∞%） */
export function deltaPercent(today: number, yesterday: number): number | null {
  if (yesterday === 0) return null;
  return Math.round(((today - yesterday) / yesterday) * 100);
}
