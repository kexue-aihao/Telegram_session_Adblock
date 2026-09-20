import {
  BLOCKING_SANCTION_TYPES,
  type EscalationStep,
  type SanctionReason,
  type SanctionType,
} from '@tgs/shared';
import { and, desc, eq, isNotNull, lte } from 'drizzle-orm';
import { getDb } from './db/client.ts';
import { contacts, sanctions } from './db/schema.ts';
import { logger } from './core/logger.ts';
import { getBotSettings } from './core/settings.ts';

/**
 * 阶梯处罚状态机。
 *
 * 这是整个项目里最容易写错的一块，因为它要同时满足三个互相拉扯的要求：
 *
 *   1. **累计**：违规分是累加的，第 5 次命中和第 1 次命中的处置不同；
 *   2. **幂等**：用户在第 5 分上连发 10 条广告，不能被禁言 10 次、
 *      也不能每命中一次就往私聊里灌一条「你已被禁言」；
 *   3. **可降级**：静默/禁言到期、或管理员手动解封后，状态要真的回去。
 *
 * 采取的模型是「状态收敛」而不是「事件累加」：
 * 每次违规只做一件事 —— 根据**当前分数**算出它应该处于哪一档，
 * 与**当前实际所处**的档位比较，不同就迁移过去。这样重复命中天然幂等，
 * 因为「算出应该在哪一档」是纯函数。
 *
 * 真正的处罚动作只发生在档位**变化**时，这一点由 `escalated` 字段告知调用方。
 */

/** 档位高低；数字大 = 更严。用于比较是否需要升级 */
const TIER: Record<SanctionType, number> = {
  warn: 1,
  silence: 2,
  mute: 3,
  ban: 4,
};

export interface SanctionRow {
  id: number;
  botId: number;
  contactId: number;
  type: SanctionType;
  reason: SanctionReason;
  ruleId: number | null;
  expiresAt: number | null;
  isActive: boolean;
  createdBy: string | null;
  scoreAt: number;
  createdAt: number;
  liftedAt: number | null;
}

function toSanction(row: typeof sanctions.$inferSelect): SanctionRow {
  return {
    id: row.id,
    botId: row.botId,
    contactId: row.contactId,
    type: row.type as SanctionType,
    reason: row.reason as SanctionReason,
    ruleId: row.ruleId,
    expiresAt: row.expiresAt,
    isActive: row.isActive,
    createdBy: row.createdBy,
    scoreAt: row.scoreAt,
    createdAt: row.createdAt,
    liftedAt: row.liftedAt,
  };
}

/**
 * 违规分时间衰减。
 *
 * 没有衰减的话，一个半年前误触发过 4 次的用户会永远停在「静默」档，
 * 管理员根本没机会知道。衰减是纯读时计算 —— 不改写库里的分数，
 * 否则每次读都要写一次，还会让「原始违规分」这个事实消失。
 */
function applyDecay(
  score: number,
  lastViolationAt: number | null,
  decayDays: number | null,
  now: number,
): { score: number; decayed: boolean } {
  if (decayDays === null || lastViolationAt === null || score <= 0) {
    return { score, decayed: false };
  }
  const elapsedDays = (now - lastViolationAt) / 86_400_000;
  if (elapsedDays < decayDays) return { score, decayed: false };

  // 每过一个周期减半，而不是一次性清零：连续多个周期不违规的用户
  // 最终会自然回到 0，而刚过线一点点的用户不会被突然赦免。
  const halvings = Math.floor(elapsedDays / decayDays);
  const decayed = Math.floor(score / 2 ** halvings);
  return { score: decayed, decayed: true };
}

export interface ViolationInput {
  botId: number;
  contactId: number;
  ruleId: number | null;
  /** 本次命中的违规分 */
  severity: number;
  reason: SanctionReason;
  createdBy?: string | null;
}

export interface ViolationOutcome {
  /** 累加并衰减后的违规分 */
  score: number;
  /** 升级前所处的档位 */
  previousType: SanctionType | null;
  /** 升级后应处的档位 */
  currentType: SanctionType | null;
  /** 本次是否发生了档位变化 —— 只有 true 时才该发通知，避免重复骚扰 */
  escalated: boolean;
  /** 本次新建的处罚行；未升级时为 null */
  sanction: SanctionRow | null;
  /** 命中的阶梯配置；未升级时为 null */
  step: EscalationStep | null;
  /** 违规分是否发生了时间衰减 */
  decayed: boolean;
}

/** 该分数落在哪一档；分数未达到任何门槛时返回 null */
export function stepForScore(steps: EscalationStep[], score: number): EscalationStep | null {
  let matched: EscalationStep | null = null;
  for (const step of steps) {
    if (!step.enabled) continue;
    if (score >= step.atScore) {
      // steps 不保证有序，取满足条件的最高档
      if (!matched || step.atScore >= matched.atScore) matched = step;
    }
  }
  return matched;
}

/** 距离下一档还差多少分；已在最高档时返回 null。供面板显示「再违规 N 分将被禁言」 */
export function nextStepForScore(
  steps: EscalationStep[],
  score: number,
): { atScore: number; type: SanctionType; remaining: number } | null {
  const candidates = steps
    .filter((step) => step.enabled && step.atScore > score)
    .sort((a, b) => a.atScore - b.atScore);
  const next = candidates[0];
  if (!next) return null;
  return { atScore: next.atScore, type: next.type, remaining: next.atScore - score };
}

/** 当前生效的处罚（已排除过期项） */
export async function getActiveSanctions(contactId: number): Promise<SanctionRow[]> {
  const { db } = getDb();
  const rows = await db
    .select()
    .from(sanctions)
    .where(and(eq(sanctions.contactId, contactId), eq(sanctions.isActive, true)))
    .orderBy(desc(sanctions.createdAt));

  const now = Date.now();
  // 过期的禁言「仍然在库里是 active」，但语义上已经失效。
  // 这里过滤而不是顺手写回：读路径不该产生写放大，真正的清理在
  // expireDueSanctions() 里由定时任务做。
  return rows
    .map(toSanction)
    .filter((row) => row.expiresAt === null || row.expiresAt > now);
}

/** 当前所处档位（取最严的一条） */
async function currentTier(contactId: number): Promise<SanctionRow | null> {
  const active = await getActiveSanctions(contactId);
  let top: SanctionRow | null = null;
  for (const row of active) {
    if (!top || TIER[row.type] > TIER[top.type]) top = row;
  }
  return top;
}

/**
 * 记一次违规并推进阶梯。返回本次是否需要执行处罚动作。
 *
 * 注意它**不发送任何消息** —— 发什么文案、发给谁，由 `rules/actions.ts`
 * 决定。状态机只管状态，这样它才能被面板的「模拟处罚」功能安全复用。
 */
export async function recordViolation(input: ViolationInput): Promise<ViolationOutcome> {
  const { db } = getDb();
  const settings = await getBotSettings(input.botId);
  const escalation = settings.escalation;
  const now = Date.now();

  const rows = await db.select().from(contacts).where(eq(contacts.id, input.contactId)).limit(1);
  const contact = rows[0];
  if (!contact) {
    throw new Error(`联系人不存在：${input.contactId}`);
  }

  const decayedResult = applyDecay(contact.violationScore, contact.lastViolationAt, escalation.decayDays, now);
  const rawScore = decayedResult.score + input.severity;
  const score = Math.min(rawScore, escalation.maxScore);

  await db
    .update(contacts)
    .set({ violationScore: score, lastViolationAt: now })
    .where(eq(contacts.id, input.contactId));

  const before = await currentTier(input.contactId);
  const target = stepForScore(escalation.steps, score);

  const previousType = before?.type ?? null;
  const currentType = target?.type ?? null;

  // 档位没变（或降了）就不动处罚记录。降档交给过期与手动解封处理 ——
  // 在「记违规」这条路径上顺手降档，会让手动解封反复被覆盖。
  const upgraded = target !== null && (before === null || TIER[target.type] > TIER[before.type]);
  if (!upgraded) {
    return {
      score,
      previousType,
      currentType: previousType,
      escalated: false,
      sanction: null,
      step: null,
      decayed: decayedResult.decayed,
    };
  }

  // 旧档位作废。保留历史行（不删）只是为了审计可追溯，因此只翻 isActive。
  if (before) {
    await db
      .update(sanctions)
      .set({ isActive: false, liftedAt: now })
      .where(and(eq(sanctions.contactId, input.contactId), eq(sanctions.isActive, true)));
  }

  const expiresAt =
    target.type === 'mute' && target.durationMinutes !== null
      ? now + target.durationMinutes * 60_000
      : null;

  const inserted = await db
    .insert(sanctions)
    .values({
      botId: input.botId,
      contactId: input.contactId,
      type: target.type,
      reason: input.reason,
      ruleId: input.ruleId,
      expiresAt,
      isActive: true,
      createdBy: input.createdBy ?? null,
      scoreAt: score,
      createdAt: now,
      liftedAt: null,
    })
    .returning();

  const row = inserted[0];
  if (!row) throw new Error('写入处罚记录失败');

  logger.info(
    { contactId: input.contactId, from: previousType, to: target.type, score },
    '阶梯处罚升级',
  );

  return {
    score,
    previousType,
    currentType: target.type,
    escalated: true,
    sanction: toSanction(row),
    step: target,
    decayed: decayedResult.decayed,
  };
}

/**
 * 当前该不该拦这个消息。
 *
 * 拉黑与静默/禁言对**用户**的可见性完全不同，但中继管线只关心
 * 「转不转」，所以统一成一个判断。`ban` 额外让机器人完全不再响应。
 */
export async function blockingState(
  contactId: number,
): Promise<{ blocked: boolean; type: SanctionType | null; expiresAt: number | null }> {
  const active = await getActiveSanctions(contactId);
  const blocking = active.filter((row) =>
    (BLOCKING_SANCTION_TYPES as readonly string[]).includes(row.type),
  );
  if (blocking.length === 0) return { blocked: false, type: null, expiresAt: null };

  let top = blocking[0] as SanctionRow;
  for (const row of blocking) {
    if (TIER[row.type] > TIER[top.type]) top = row;
  }
  return { blocked: true, type: top.type, expiresAt: top.expiresAt };
}

/** 手动解封 / 解除静默；`type` 为空表示解除该联系人的全部生效处罚 */
export async function liftSanctions(
  contactId: number,
  options: { actor: string; type?: SanctionType },
): Promise<number> {
  const { db } = getDb();
  const now = Date.now();
  const where = options.type
    ? and(eq(sanctions.contactId, contactId), eq(sanctions.isActive, true), eq(sanctions.type, options.type))
    : and(eq(sanctions.contactId, contactId), eq(sanctions.isActive, true));

  const updated = await db
    .update(sanctions)
    .set({ isActive: false, liftedAt: now })
    .where(where)
    .returning({ id: sanctions.id });

  return updated.length;
}

/**
 * 把违规分清零并解除所有处罚。
 * 「重置违规分」是管理员在话题里的一键操作，必须把两件事一起做 ——
 * 只清分数会留下还在生效的禁言，下次命中时状态机的档位判断也会错乱。
 */
export async function resetViolations(contactId: number): Promise<void> {
  const { db } = getDb();
  const now = Date.now();
  await db
    .update(contacts)
    .set({ violationScore: 0, lastViolationAt: null })
    .where(eq(contacts.id, contactId));
  await db
    .update(sanctions)
    .set({ isActive: false, liftedAt: now })
    .where(and(eq(sanctions.contactId, contactId), eq(sanctions.isActive, true)));
}

/** 到期的禁言写回 inactive；由定时任务调用，避免读路径产生写放大 */
export async function expireDueSanctions(): Promise<number> {
  const { db } = getDb();
  const now = Date.now();

  // expires_at IS NOT NULL AND expires_at <= now
  // 永久处罚（拉黑、静默）的 expiresAt 是 NULL，必须显式排除，
  // 否则 SQL 里 `NULL <= ?` 虽然为假不会误伤，但 `IS NULL OR ...` 这种写法
  // 一旦有人顺手改成 OR 就会把永久处罚全部清掉。
  const due = and(isNotNull(sanctions.expiresAt), lte(sanctions.expiresAt, now));

  const expired = await db
    .select({ id: sanctions.id })
    .from(sanctions)
    .where(and(eq(sanctions.isActive, true), due));

  if (expired.length === 0) return 0;

  await db
    .update(sanctions)
    .set({ isActive: false, liftedAt: now })
    .where(and(eq(sanctions.isActive, true), due));

  logger.debug({ count: expired.length }, '已解除到期处罚');
  return expired.length;
}
