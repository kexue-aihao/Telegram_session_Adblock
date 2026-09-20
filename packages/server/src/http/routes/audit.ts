import {
  auditLogQuerySchema,
  ruleHitQuerySchema,
  type AuditLogEntry,
  type RuleHit,
} from '@tgs/shared';
import { and, desc, eq, like, lt, or, sql, type SQL } from 'drizzle-orm';
import type { App } from '../app.ts';
import { z } from 'zod';
import { getDb } from '../../db/client.ts';
import { auditLog, bots, contacts, ruleHits } from '../../db/schema.ts';
import { parseQuery } from '../validate.ts';
import { requireAuth } from '../auth.ts';

/**
 * 审计查询。
 *
 * 两张表在这里分开暴露：`rule_hits` 是「机器人拦了什么」（可能几十万行，
 * 按时间倒序翻页），`audit_log` 是「人做了什么」（量小，但每一条都要能追溯）。
 * 面板上也对应两个页面，混在一起查询两边都不好筛。
 */

export async function registerAuditRoutes(app: App): Promise<void> {

  /** 广告命中记录 */
  app.get('/api/audit/hits', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(ruleHitQuerySchema, request.query);
    const { db } = getDb();

    const conditions: SQL[] = [];
    if (query.botId !== undefined) conditions.push(eq(ruleHits.botId, query.botId));
    if (query.ruleId !== undefined) conditions.push(eq(ruleHits.ruleId, query.ruleId));
    if (query.contactId !== undefined) conditions.push(eq(ruleHits.contactId, query.contactId));
    if (query.from !== undefined) conditions.push(sql`${ruleHits.createdAt} >= ${query.from}`);
    if (query.to !== undefined) conditions.push(sql`${ruleHits.createdAt} <= ${query.to}`);
    if (query.q) {
      const pattern = `%${query.q}%`;
      const search = or(
        like(ruleHits.matchedText, pattern),
        like(ruleHits.ruleName, pattern),
        like(contacts.username, pattern),
        like(contacts.firstName, pattern),
      );
      if (search) conditions.push(search);
    }

    // 游标是「上一页最后一行的 id」。用 id 而不是时间戳：
    // 同一毫秒内可能有多条命中，用时间戳做游标会漏掉同刻的记录。
    if (query.cursor !== undefined) conditions.push(lt(ruleHits.id, query.cursor));

    const total = await db
      .select({ count: sql<number>`count(*)` })
      .from(ruleHits)
      .where(conditions.length > 0 ? and(...conditions) : undefined);

    const rows = await db
      .select({
        hit: ruleHits,
        botName: bots.name,
        contactName: contacts.firstName,
        contactUsername: contacts.username,
        tgUserId: contacts.tgUserId,
        threadId: sql<number | null>`(select message_thread_id from topics where topics.id = ${ruleHits.topicId})`,
      })
      .from(ruleHits)
      .leftJoin(bots, eq(bots.id, ruleHits.botId))
      .leftJoin(contacts, eq(contacts.id, ruleHits.contactId))
      .where(conditions.length > 0 ? and(...conditions) : undefined)
      .orderBy(desc(ruleHits.id))
      .limit(query.limit);

    const items: RuleHit[] = rows.map((row) => ({
      id: row.hit.id,
      ruleId: row.hit.ruleId,
      ruleName: row.hit.ruleName,
      rulePattern: row.hit.rulePattern,
      ruleFlags: row.hit.ruleFlags,
      botId: row.hit.botId,
      botName: row.botName ?? '（已删除）',
      contactId: row.hit.contactId,
      contactName: row.contactName ?? '未知用户',
      contactUsername: row.contactUsername,
      tgUserId: row.tgUserId ?? 0,
      topicId: row.hit.topicId,
      threadId: row.threadId,
      matchedText: row.hit.matchedText,
      normalizedExcerpt: row.hit.normalizedExcerpt,
      outcomes: row.hit.outcomes,
      severity: row.hit.severity,
      createdAt: row.hit.createdAt,
    }));

    const last = items[items.length - 1];
    return {
      items,
      nextCursor: items.length === query.limit && last ? last.id : null,
      total: total[0]?.count ?? null,
    };
  });

  /**
   * 命中量按规则聚合 —— 「哪条规则最忙 / 哪条规则从来没命中过」。
   * 这是管理员调整规则集时最有用的一个视角。
   */
  app.get('/api/audit/hits/by-rule', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(
      z.object({
        botId: z.coerce.number().int().positive().optional(),
        days: z.coerce.number().int().min(1).max(365).default(30),
      }),
      request.query,
    );

    const since = Date.now() - query.days * 86_400_000;
    const { db } = getDb();
    const conditions: SQL[] = [sql`${ruleHits.createdAt} >= ${since}`];
    if (query.botId !== undefined) conditions.push(eq(ruleHits.botId, query.botId));

    const rows = await db
      .select({
        ruleId: ruleHits.ruleId,
        ruleName: ruleHits.ruleName,
        count: sql<number>`count(*)`,
        lastHitAt: sql<number>`max(${ruleHits.createdAt})`,
      })
      .from(ruleHits)
      .where(and(...conditions))
      .groupBy(ruleHits.ruleId, ruleHits.ruleName)
      .orderBy(desc(sql`count(*)`))
      .limit(100);

    return { items: rows, since };
  });

  /** 面板操作审计 */
  app.get('/api/audit/log', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(auditLogQuerySchema, request.query);
    const { db } = getDb();

    const conditions: SQL[] = [];
    if (query.action) conditions.push(like(auditLog.action, `${query.action}%`));
    if (query.actorType) conditions.push(eq(auditLog.actorType, query.actorType));
    if (query.from !== undefined) conditions.push(sql`${auditLog.createdAt} >= ${query.from}`);
    if (query.to !== undefined) conditions.push(sql`${auditLog.createdAt} <= ${query.to}`);
    if (query.q) {
      const pattern = `%${query.q}%`;
      const search = or(
        like(auditLog.action, pattern),
        like(auditLog.actorId, pattern),
        like(auditLog.targetId, pattern),
      );
      if (search) conditions.push(search);
    }
    if (query.cursor !== undefined) conditions.push(lt(auditLog.id, query.cursor));

    const total = await db
      .select({ count: sql<number>`count(*)` })
      .from(auditLog)
      .where(conditions.length > 0 ? and(...conditions) : undefined);

    const rows = await db
      .select()
      .from(auditLog)
      .where(conditions.length > 0 ? and(...conditions) : undefined)
      .orderBy(desc(auditLog.id))
      .limit(query.limit);

    const items: AuditLogEntry[] = rows.map((row) => ({
      id: row.id,
      actorType: row.actorType as AuditLogEntry['actorType'],
      actorId: row.actorId,
      action: row.action,
      targetType: row.targetType,
      targetId: row.targetId,
      detail: row.detail,
      ip: row.ip,
      createdAt: row.createdAt,
    }));

    const last = items[items.length - 1];
    return {
      items,
      nextCursor: items.length === query.limit && last ? last.id : null,
      total: total[0]?.count ?? null,
    };
  });

  /** 出现过的动作名，用于筛选下拉框 */
  app.get('/api/audit/actions', { preHandler: requireAuth }, async () => {
    const { db } = getDb();
    const rows = await db
      .select({ action: auditLog.action })
      .from(auditLog)
      .groupBy(auditLog.action)
      .orderBy(auditLog.action);
    return { items: rows.map((row) => row.action) };
  });
}
