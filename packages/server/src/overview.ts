import type { StatsOverview } from '@tgs/shared';
import { and, eq, gte, inArray, sql } from 'drizzle-orm';
import { getDb } from './db/client.ts';
import { bots, contacts, messages, ruleHits, statsDaily, topics } from './db/schema.ts';
import { deltaPercent, GLOBAL_BOT_ID, todayInTz } from './core/stats.ts';
import { getGlobalSettings } from './core/settings.ts';
import { ruleStats } from './rules/engine.ts';

/**
 * 仪表盘总览的**唯一**计算入口。
 *
 * HTTP 路由（面板首次加载）与 WebSocket 的定时推送都用它 —— 两个地方各算一遍
 * 必然会漂移，表现出来就是「页面刷新前后的数字对不上」，而这类 bug
 * 因为不是每次都复现，排查成本极高。
 */

export async function computeOverview(): Promise<StatsOverview> {
  const { db } = getDb();
  const tz = (await getGlobalSettings()).timezone;
  const today = todayInTz(tz);
  const yesterday = shiftDate(today, -1);
  const startOfToday = new Date(`${today}T00:00:00`).getTime();

  const [botRows, openTopics, closedTopics, contactAgg, messageTotal, rules] = await Promise.all([
    db.select({ healthStatus: bots.healthStatus, isEnabled: bots.isEnabled }).from(bots),
    db.select({ count: sql<number>`count(*)` }).from(topics).where(eq(topics.status, 'open')),
    db.select({ count: sql<number>`count(*)` }).from(topics).where(eq(topics.status, 'closed')),
    db
      .select({
        total: sql<number>`count(*)`,
        blocked: sql<number>`coalesce(sum(case when ${contacts.isBlocked} = 1 then 1 else 0 end), 0)`,
        flagged: sql<number>`coalesce(sum(case when ${contacts.violationScore} > 0 then 1 else 0 end), 0)`,
      })
      .from(contacts),
    db.select({ count: sql<number>`count(*)` }).from(messages),
    ruleStats(),
  ]);

  // 今日 / 昨日的日聚合行一次查回来，避免两次往返
  const daily = await db
    .select()
    .from(statsDaily)
    .where(and(eq(statsDaily.botId, GLOBAL_BOT_ID), inArray(statsDaily.date, [today, yesterday])));

  const todayRow = daily.find((row) => row.date === today);
  const yesterdayRow = daily.find((row) => row.date === yesterday);

  /**
   * 「今日新增会话」与「24 小时拦截数」需要小时级粒度，按日聚合表给不了，
   * 因此直查原表。两者都带时间下界并命中索引，代价可控 ——
   * 而它们恰恰是运维最关心的两个数字，值得这一次查询。
   */
  const [createdToday, blocked24h, hitsTotal] = await Promise.all([
    db.select({ count: sql<number>`count(*)` }).from(topics).where(gte(topics.createdAt, startOfToday)),
    db
      .select({ count: sql<number>`count(*)` })
      .from(ruleHits)
      .where(gte(ruleHits.createdAt, Date.now() - 86_400_000)),
    db.select({ count: sql<number>`count(*)` }).from(ruleHits),
  ]);

  return {
    bots: {
      total: botRows.length,
      online: botRows.filter((row) => row.healthStatus === 'online').length,
      error: botRows.filter((row) => row.healthStatus === 'error').length,
      disabled: botRows.filter((row) => !row.isEnabled).length,
    },
    sessions: {
      open: openTopics[0]?.count ?? 0,
      closed: closedTopics[0]?.count ?? 0,
      createdToday: createdToday[0]?.count ?? 0,
    },
    contacts: {
      total: contactAgg[0]?.total ?? 0,
      blocked: contactAgg[0]?.blocked ?? 0,
      flagged: contactAgg[0]?.flagged ?? 0,
    },
    messages: {
      inToday: todayRow?.messagesIn ?? 0,
      outToday: todayRow?.messagesOut ?? 0,
      total: messageTotal[0]?.count ?? 0,
    },
    ads: {
      blockedToday: todayRow?.adsBlocked ?? 0,
      blocked24h: blocked24h[0]?.count ?? 0,
      blockedTotal: hitsTotal[0]?.count ?? 0,
    },
    rules,
    deltas: {
      messagesIn: deltaPercent(todayRow?.messagesIn ?? 0, yesterdayRow?.messagesIn ?? 0),
      adsBlocked: deltaPercent(todayRow?.adsBlocked ?? 0, yesterdayRow?.adsBlocked ?? 0),
      sessionsCreated: deltaPercent(todayRow?.topicsCreated ?? 0, yesterdayRow?.topicsCreated ?? 0),
    },
    generatedAt: Date.now(),
  };
}

/** 日期偏移；输入输出都是 YYYY-MM-DD */
export function shiftDate(date: string, days: number): string {
  const cursor = new Date(`${date}T00:00:00Z`);
  cursor.setUTCDate(cursor.getUTCDate() + days);
  return cursor.toISOString().slice(0, 10);
}
