import {
  RULE_ACTIONS,
  type AdRule,
  type MatchMode,
  type RuleAction,
  type RuleTarget,
} from '@tgs/shared';
import { asc, eq, sql } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { adRules } from '../db/schema.ts';
import { logger } from '../core/logger.ts';
import { runMatcher } from '../core/regex.ts';
import { getBotSettings, getGlobalSettings } from '../core/settings.ts';

/**
 * 规则引擎。
 *
 * 职责边界：**只负责判断「命中了什么」**，不负责执行处置。
 * 处罚要写库、要发消息、要改话题状态 —— 那些放在 `rules/actions.ts`。
 * 拆开的理由是这个引擎要能被沙盒（面板里的正则测试）直接复用：
 * 沙盒绝不能因为「测一下正则」就真的去删消息。
 */

export type RuleScope = 'user' | 'admin';

export interface RuleContext {
  botId: number;
  /** 被匹配的是谁的消息：终端用户（可处罚）还是管理员（仅审计） */
  scope: RuleScope;
  /** 按匹配目标预先拼好的文本；缺失的 target 视为空串 */
  haystacks: Partial<Record<RuleTarget, string>>;
  /** 归一化后的同一批文本，用于给出「为什么这也能命中」的解释 */
  normalized: Partial<Record<RuleTarget, string>>;
}

export interface RuleHit {
  rule: AdRule;
  target: RuleTarget;
  matchedText: string;
  /** 归一化文本里的命中上下文，供审计解释 */
  normalizedExcerpt: string | null;
  severity: number;
}

export interface RuleEvaluation {
  hits: RuleHit[];
  /**
   * 本次判定中超时的规则 id。
   *
   * 单独返回而不是塞进 hits：两者的处置方向完全相反 ——
   * 命中要处罚用户，超时要熔断规则。混在一起迟早会被写错。
   */
  timedOutRuleIds: number[];
}

type RuleRow = typeof adRules.$inferSelect;

/**
 * 规则缓存。
 *
 * 无缓存时每条消息都要查一次 `ad_rules` —— 中继热路径上这是纯粹的浪费，
 * 因为规则是管理员偶尔才改一次的数据。写入方调用 `invalidateRules()` 失效。
 */
let cache: RuleRow[] | null = null;

export function invalidateRules(): void {
  cache = null;
}

async function loadEnabledRules(): Promise<RuleRow[]> {
  if (cache) return cache;
  const { db } = getDb();
  cache = await db
    .select()
    .from(adRules)
    .where(eq(adRules.isEnabled, true))
    .orderBy(asc(adRules.priority), asc(adRules.id));
  return cache;
}

/**
 * 取出对某个机器人生效的规则：全局规则（botId 为 null）+ 该机器人专属规则。
 * 顺序即匹配顺序 —— 按 priority 升序，同优先级按 id 升序保证稳定。
 */
function rulesForBot(all: RuleRow[], botId: number): RuleRow[] {
  return all.filter((rule) => rule.botId === null || rule.botId === botId);
}

/** 面板上的规则列表：包含停用的，按 priority 排序 */
export async function listAllRules(): Promise<RuleRow[]> {
  const { db } = getDb();
  return db.select().from(adRules).orderBy(asc(adRules.priority), asc(adRules.id));
}

/** 单条规则读取，供沙盒与详情接口使用 */
export async function findRule(id: number): Promise<RuleRow | null> {
  const { db } = getDb();
  const rows = await db.select().from(adRules).where(eq(adRules.id, id)).limit(1);
  return rows[0] ?? null;
}

/**
 * 停用某条规则并记下原因。
 * 目前只有一个调用方：正则超时导致引擎自动熔断（见 actions.ts）。
 */
export async function autoDisableRule(id: number, reason: string): Promise<void> {
  const { db } = getDb();
  await db
    .update(adRules)
    .set({
      isEnabled: false,
      autoDisabledAt: Date.now(),
      autoDisabledReason: reason,
      updatedAt: Date.now(),
    })
    .where(eq(adRules.id, id));
  invalidateRules();
}

/** 取一段文本中命中位置附近的一小片，供审计面板展示「为什么命中」 */
function excerptAround(haystack: string, index: number, length: number): string {
  const pad = 24;
  const start = Math.max(0, index - pad);
  const end = Math.min(haystack.length, index + length + pad);
  const prefix = start > 0 ? '…' : '';
  const suffix = end < haystack.length ? '…' : '';
  return `${prefix}${haystack.slice(start, end)}${suffix}`;
}

/**
 * 把 `RuleRow` 投影成共用 DTO。
 * `hitCount` 等运行期字段也带上 —— 面板要显示「这条规则命中了多少次」。
 */
export function toAdRule(row: RuleRow): AdRule {
  return {
    id: row.id,
    botId: row.botId,
    name: row.name,
    pattern: row.pattern,
    flags: row.flags,
    matchMode: row.matchMode as MatchMode,
    target: row.target as RuleTarget,
    action: row.action as RuleAction,
    severity: row.severity,
    priority: row.priority,
    isEnabled: row.isEnabled,
    isSystem: row.isSystem,
    note: row.note,
    hitCount: row.hitCount,
    lastHitAt: row.lastHitAt,
    createdAt: row.createdAt,
    updatedAt: row.updatedAt,
  };
}

/**
 * 对一个上下文跑完整套规则。
 *
 * 返回**全部**命中而不是第一个：方案里定的是「每条都记审计，动作取并集」。
 * 只汇报第一条会让管理员永远不知道自己的规则集里有多少条在重复命中同一句话。
 */
export async function evaluateRules(ctx: RuleContext): Promise<RuleEvaluation> {
  const settings = await getBotSettings(ctx.botId);
  if (!settings.rulesEnabled) return { hits: [], timedOutRuleIds: [] };

  const { regexTimeoutMs } = await getGlobalSettings();
  const rules = rulesForBot(await loadEnabledRules(), ctx.botId);
  if (rules.length === 0) return { hits: [], timedOutRuleIds: [] };

  const hits: RuleHit[] = [];
  const timedOutRuleIds: number[] = [];

  for (const row of rules) {
    const target = row.target as RuleTarget;
    const raw = ctx.haystacks[target];
    if (!raw) continue;

    // 规则始终匹配**归一化文本**：归一化的全部意义就是让 `加<ZWSP>微信`
    // 这类变体塌缩到同一个形态；改回匹配原文等于把防护关掉。
    const haystack = ctx.normalized[target] ?? raw;

    const result = runMatcher(
      { pattern: row.pattern, flags: row.flags, matchMode: row.matchMode as MatchMode },
      haystack,
      regexTimeoutMs,
    );

    if (result.timedOut) {
      timedOutRuleIds.push(row.id);
      logger.error(
        { ruleId: row.id, ruleName: row.name, durationMs: result.durationMs },
        '规则匹配超时（疑似灾难性回溯），本次判定跳过',
      );
      continue;
    }

    if (!result.matched) continue;

    const first = result.matches[0];
    hits.push({
      rule: toAdRule(row),
      target,
      matchedText: result.matchedText,
      normalizedExcerpt: first
        ? excerptAround(haystack, first.start, first.end - first.start)
        : null,
      severity: row.severity,
    });
  }

  return { hits, timedOutRuleIds };
}

/**
 * 动作并集：命中多条规则时，四类动作各最多执行一次。
 * 取并集而不是按优先级只执行一条 —— 否则「删了但没警告」这种半吊子处置
 * 会让管理员以为规则没生效。
 */
export function unionActions(hits: RuleHit[]): Set<RuleAction> {
  const actions = new Set<RuleAction>();
  for (const hit of hits) {
    if ((RULE_ACTIONS as readonly string[]).includes(hit.rule.action)) {
      actions.add(hit.rule.action);
    }
  }
  return actions;
}

/** 命中累加的违规分：取最高而不是求和，避免 3 条轻规则叠成一次重罚 */
export function totalSeverity(hits: RuleHit[]): number {
  return hits.reduce((max, hit) => Math.max(max, hit.severity), 0);
}

/** 已启用规则数 / 自动熔断数，供仪表盘展示 */
export async function ruleStats(): Promise<{ total: number; enabled: number; autoDisabled: number }> {
  const { db } = getDb();
  const rows = await db.select().from(adRules);
  return {
    total: rows.length,
    enabled: rows.filter((r) => r.isEnabled).length,
    autoDisabled: rows.filter((r) => r.autoDisabledAt !== null).length,
  };
}

/** 命中计数批量自增；一条消息可能同时命中多条规则 */
export async function bumpHitCounts(ids: number[]): Promise<void> {
  if (ids.length === 0) return;
  const { db } = getDb();
  const now = Date.now();
  for (const id of ids) {
    await db
      .update(adRules)
      .set({ hitCount: sql`${adRules.hitCount} + 1`, lastHitAt: now })
      .where(eq(adRules.id, id));
  }
  invalidateRules();
}
