import {
  createRuleInputSchema,
  reorderRulesInputSchema,
  ruleTestInputSchema,
  ruleTestResultSchema,
  updateRuleInputSchema,
  type RuleTestResult,
} from '@tgs/shared';
import { eq } from 'drizzle-orm';
import type { App } from '../app.ts';
import { z } from 'zod';
import { getDb } from '../../db/client.ts';
import { adRules } from '../../db/schema.ts';
import { recordAudit } from '../../core/audit.ts';
import { checkPatternSafety, runMatcher } from '../../core/regex.ts';
import { normalizeText } from '../../core/text.ts';
import { getGlobalSettings } from '../../core/settings.ts';
import { findRule, invalidateRules, listAllRules, toAdRule } from '../../rules/engine.ts';
import { parseBody, parseQuery, badRequest, HttpError } from '../validate.ts';
import { requireAuth } from '../auth.ts';

/**
 * 广告规则的增删改查与**测试沙盒**。
 *
 * 沙盒是本项目里最值得投入的一个功能：正则的写法与匹配结果之间的关系
 * 对大多数管理员来说并不直观，让他们「保存后再看效果」等于让他们拿
 * 线上的真实用户当实验对象。这里直接复用引擎的匹配函数，保证沙盒所见
 * 即运行时所得 —— 另写一套简化版匹配必然与真实行为漂移。
 */

const idParam = z.object({ id: z.coerce.number().int().positive() });

export async function registerRuleRoutes(app: App): Promise<void> {

  app.get('/api/rules', { preHandler: requireAuth }, async (request) => {
    const query = parseQuery(
      z.object({ botId: z.coerce.number().int().positive().optional() }),
      request.query,
    );
    const rows = await listAllRules();
    // botId 过滤包含全局规则（botId 为 null），因为它们对所有机器人生效
    const items = rows
      .filter((row) => query.botId === undefined || row.botId === null || row.botId === query.botId)
      .map(toAdRule);
    return { items };
  });

  app.post('/api/rules', { preHandler: requireAuth }, async (request, reply) => {
    const input = parseBody(createRuleInputSchema, request.body);

    // 保存前静态体检：嵌套量词这类模式一旦入库，就会在中继热路径上
    // 反复吃掉几十毫秒，而管理员完全不知道是自己写错了正则。
    const safety = checkPatternSafety(input.pattern, input.flags);
    if (!safety.safe) return reply.code(400).send({ error: safety.reason });

    const { db } = getDb();
    const now = Date.now();
    const inserted = await db
      .insert(adRules)
      .values({
        botId: input.botId,
        name: input.name,
        pattern: input.pattern,
        flags: input.flags,
        matchMode: input.matchMode,
        target: input.target,
        action: input.action,
        severity: input.severity,
        priority: input.priority,
        isEnabled: input.isEnabled,
        isSystem: false,
        note: input.note,
        hitCount: 0,
        lastHitAt: null,
        autoDisabledAt: null,
        autoDisabledReason: null,
        createdAt: now,
        updatedAt: now,
      })
      .returning();

    const row = inserted[0];
    if (!row) return reply.code(500).send({ error: '创建规则失败' });

    invalidateRules();
    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'rule.created',
      targetType: 'rule',
      targetId: row.id,
      detail: { name: row.name, pattern: row.pattern },
      ip: request.ip,
    });

    return reply.code(201).send({
      rule: toAdRule(row),
      /** 静态检查的提醒（例如「未能分析，将依赖运行期超时」） */
      warning: safety.reason,
    });
  });

  app.patch('/api/rules/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const input = parseBody(updateRuleInputSchema, request.body);

    const existing = await findRule(id);
    if (!existing) return reply.code(404).send({ error: '规则不存在' });

    const pattern = input.pattern ?? existing.pattern;
    const flags = input.flags ?? existing.flags;
    if (input.pattern !== undefined || input.flags !== undefined) {
      const safety = checkPatternSafety(pattern, flags);
      if (!safety.safe) return reply.code(400).send({ error: safety.reason });
    }

    const { db } = getDb();
    await db
      .update(adRules)
      .set({
        name: input.name ?? existing.name,
        pattern,
        flags,
        matchMode: input.matchMode ?? existing.matchMode,
        target: input.target ?? existing.target,
        action: input.action ?? existing.action,
        severity: input.severity ?? existing.severity,
        priority: input.priority ?? existing.priority,
        isEnabled: input.isEnabled ?? existing.isEnabled,
        note: input.note === undefined ? existing.note : input.note,
        // 手工改过就清掉熔断标记：管理员显然已经知道这条规则有问题了，
        // 再把「已被自动停用」的红标挂在上面会让人以为改动没生效。
        autoDisabledAt: null,
        autoDisabledReason: null,
        updatedAt: Date.now(),
      })
      .where(eq(adRules.id, id));

    invalidateRules();

    const updated = await findRule(id);
    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'rule.updated',
      targetType: 'rule',
      targetId: id,
      detail: { keys: Object.keys(input) },
      ip: request.ip,
    });

    return { rule: updated ? toAdRule(updated) : null };
  });

  app.delete('/api/rules/:id', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const existing = await findRule(id);
    if (!existing) return reply.code(404).send({ error: '规则不存在' });

    const { db } = getDb();
    // 命中审计里存的是规则快照，所以删掉规则不会让历史记录失去意义
    await db.delete(adRules).where(eq(adRules.id, id));
    invalidateRules();

    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'rule.deleted',
      targetType: 'rule',
      targetId: id,
      detail: { name: existing.name, pattern: existing.pattern },
      ip: request.ip,
    });

    return { ok: true };
  });

  /** 快速启停（列表页的开关） */
  app.post('/api/rules/:id/toggle', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const input = parseBody(z.object({ isEnabled: z.boolean() }), request.body);

    const existing = await findRule(id);
    if (!existing) return reply.code(404).send({ error: '规则不存在' });

    const { db } = getDb();
    await db
      .update(adRules)
      .set({
        isEnabled: input.isEnabled,
        // 重新启用时清掉熔断标记，否则刚打开的规则立刻又被标成「已停用」
        ...(input.isEnabled ? { autoDisabledAt: null, autoDisabledReason: null } : {}),
        updatedAt: Date.now(),
      })
      .where(eq(adRules.id, id));

    invalidateRules();
    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'rule.toggled',
      targetType: 'rule',
      targetId: id,
      detail: { isEnabled: input.isEnabled },
      ip: request.ip,
    });

    return { ok: true };
  });

  /** 拖拽排序：按下标重写 priority（步长 10，留出手工微调的余地） */
  app.post('/api/rules/reorder', { preHandler: requireAuth }, async (request, reply) => {
    const input = parseBody(reorderRulesInputSchema, request.body);
    const { db } = getDb();

    const existing = await listAllRules();
    const known = new Set(existing.map((row) => row.id));
    const missing = input.orderedIds.filter((id) => !known.has(id));
    if (missing.length > 0) {
      return reply.code(400).send({ error: `这些规则不存在：${missing.join(', ')}` });
    }

    for (const [index, id] of input.orderedIds.entries()) {
      await db
        .update(adRules)
        .set({ priority: (index + 1) * 10, updatedAt: Date.now() })
        .where(eq(adRules.id, id));
    }

    invalidateRules();
    await recordAudit({
      actorType: 'admin',
      actorId: request.auth?.username ?? null,
      action: 'rule.reordered',
      targetType: 'rule',
      detail: { order: input.orderedIds },
      ip: request.ip,
    });

    return { ok: true };
  });

  /**
   * 沙盒：用一段样本文本试跑规则。
   *
   * 返回归一化后的文本，因为「为什么这也能命中」是管理员最常问的问题 ——
   * 明明写的是「加微信」，为什么「加​微​信」也被拦了？
   * 把归一化结果摆出来，答案就不言自明。
   */
  app.post('/api/rules/test', { preHandler: requireAuth }, async (request) => {
    const input = parseBody(ruleTestInputSchema, request.body);
    return runSandbox(input.pattern, input.flags, input.matchMode, input.sample);
  });

  /** 与上面的区别：用已入库的规则试跑，避免前端把 pattern 再传一遍 */
  app.post('/api/rules/:id/test', { preHandler: requireAuth }, async (request, reply) => {
    const { id } = parseQuery(idParam, request.params);
    const input = parseBody(z.object({ sample: z.string().max(20_000) }), request.body);

    const rule = await findRule(id);
    if (!rule) return reply.code(404).send({ error: '规则不存在' });

    return runSandbox(rule.pattern, rule.flags, rule.matchMode as 'regex', input.sample);
  });

  /** 只做静态校验，不执行 —— 前端在输入时实时调用 */
  app.post('/api/rules/validate-regex', { preHandler: requireAuth }, async (request) => {
    const input = parseBody(
      z.object({
        pattern: z.string().max(2000),
        flags: z.string().max(10).default('iu'),
      }),
      request.body,
    );
    return checkPatternSafety(input.pattern, input.flags);
  });
}

/** 供 /test 与 /:id/test 共用：跑一次匹配并组装结果 */
async function runSandbox(
  pattern: string,
  flags: string,
  matchMode: 'regex' | 'contains' | 'whole_word',
  sample: string,
): Promise<RuleTestResult> {
  const safety = checkPatternSafety(pattern, flags);
  const { regexTimeoutMs } = await getGlobalSettings();

  if (!safety.safe) {
    throw new HttpError(400, safety.reason ?? '正则不安全');
  }

  // 沙盒也走归一化，与运行时完全一致
  const normalized = normalizeText(sample);
  const started = performance.now();
  const result = runMatcher({ pattern, flags, matchMode }, normalized, regexTimeoutMs);

  if (result.error !== null) {
    badRequest(result.error);
  }

  return ruleTestResultSchema.parse({
    ok: !result.timedOut && result.error === null,
    error: result.error,
    safeRegex: { safe: safety.safe, reason: safety.reason },
    matches: result.matches.map((match) => ({
      start: match.start,
      end: match.end,
      text: match.text,
      groups: match.groups,
    })),
    truncated: result.truncated,
    timedOut: result.timedOut,
    durationMs: Math.round((performance.now() - started) * 100) / 100,
    normalizedSample: normalized,
  });
}
