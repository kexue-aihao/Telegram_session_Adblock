import {
  globalSettingsSchema,
  timeseriesQuerySchema,
} from '@tgs/shared';
import { and, eq, gte, lte, sql } from 'drizzle-orm';
import type { App } from '../app.ts';
import { z } from 'zod';
import { getDb } from '../../db/client.ts';
import { bots, ruleHits, statsDaily } from '../../db/schema.ts';
import { recordAudit } from '../../core/audit.ts';
import { getTimeseries, todayInTz, GLOBAL_BOT_ID } from '../../core/stats.ts';
import { getGlobalSettings, updateGlobalSettings } from '../../core/settings.ts';
import { computeOverview, shiftDate } from '../../overview.ts';
import { parseBody, parseQuery } from '../validate.ts';
import { pruneAuthData, requireAuth } from '../auth.ts';

/**
 * 仪表盘数据与全局设置。
 *
 * 统计口径全部走 `stats_daily` 这张按日聚合表，而不是实时扫 `messages`：
 * 面板是每天要打开很多次的第一屏，而 `messages` 会随使用时间线性增长 ——
 * 一个每次刷新都扫几十万行的仪表盘，用上一个月就慢得不能看了。
 */

export async function registerStatsRoutes(app: App): Promise<void> {

  app.get('/api/stats/overview', { preHandler: requireAuth }, async () => {
    return computeOverview();
  });

  app.get('/api/stats/timeseries', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(timeseriesQuerySchema, request.query);
    const points = await getTimeseries(query.days, query.botId ?? GLOBAL_BOT_ID);
    return { points };
  });

  /**
   * 每个机器人的统计对比 —— 哪个机器人在挨骂（拦得多），哪个平安无事。
   * 数据量小（机器人数 × 天数），一次查完在内存里分组即可。
   */
  app.get('/api/stats/by-bot', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(
      z.object({
        days: z.coerce.number().int().min(1).max(90).default(7),
      }),
      request.query,
    );

    const since = shiftDate(todayInTz((await getGlobalSettings()).timezone), -(query.days - 1));
    const { db } = getDb();

    const rows = await db
      .select({
        botId: statsDaily.botId,
        messagesIn: sql<number>`sum(${statsDaily.messagesIn})`,
        messagesOut: sql<number>`sum(${statsDaily.messagesOut})`,
        topicsCreated: sql<number>`sum(${statsDaily.topicsCreated})`,
        adsBlocked: sql<number>`sum(${statsDaily.adsBlocked})`,
      })
      .from(statsDaily)
      .where(and(gte(statsDaily.date, since), sql`${statsDaily.botId} <> ${GLOBAL_BOT_ID}`))
      .groupBy(statsDaily.botId);

    const botRows = await db.select({ id: bots.id, name: bots.name }).from(bots);
    const nameById = new Map(botRows.map((row) => [row.id, row.name]));

    return {
      items: rows.map((row) => ({
        botId: row.botId,
        botName: nameById.get(row.botId) ?? `#${row.botId}`,
        messagesIn: row.messagesIn,
        messagesOut: row.messagesOut,
        topicsCreated: row.topicsCreated,
        adsBlocked: row.adsBlocked,
      })),
      since,
    };
  });

  // ────────────────────────── 全局设置 ──────────────────────────

  app.get('/api/settings', { preHandler: requireAuth }, async () => {
    return getGlobalSettings();
  });

  app.patch('/api/settings', { preHandler: requireAuth }, async (request) => {
    const patch = parseBody(globalSettingsSchema.partial(), request.body);
    const next = await updateGlobalSettings(patch);

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'settings.updated',
      targetType: 'global',
      detail: { keys: Object.keys(patch) },
      ip: request.ip,
    });

    return next;
  });

  /**
   * 每小时清理一次过期数据。
   * 由这里触发而不是独立定时器：它本来就是「面板数据维护」的一部分，
   * 不需要为它再引入一套调度设施。
   */
  app.post('/api/settings/prune', { preHandler: requireAuth }, async (request) => {
    const settings = await getGlobalSettings();
    const { db } = getDb();

    let removedHits = 0;
    if (settings.auditRetentionDays !== null) {
      const cutoff = Date.now() - settings.auditRetentionDays * 86_400_000;
      const deleted = await db
        .delete(ruleHits)
        .where(lte(ruleHits.createdAt, cutoff))
        .returning({ id: ruleHits.id });
      removedHits = deleted.length;
    }

    await pruneAuthData();

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'settings.updated',
      targetType: 'global',
      detail: { prune: true, removedHits },
      ip: request.ip,
    });

    return { ok: true, removedHits };
  });
}
