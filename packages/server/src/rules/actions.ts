import {
  channel,
  type BotSettings,
  type HitOutcome,
  type SanctionType,
} from '@tgs/shared';
import type { Api } from 'grammy';
import { eq } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { messages, ruleHits } from '../db/schema.ts';
import { publish } from '../core/bus.ts';
import { logger } from '../core/logger.ts';
import { getGlobalSettings } from '../core/settings.ts';
import { formatUntil, renderTemplate } from '../core/template.ts';
import { recordViolation, type ViolationOutcome } from '../sanctions.ts';
import {
  closeTopic,
  sendToTopic,
  sendToUser,
  touchTopic,
  type ContactRow,
} from '../bots/topics.ts';
import { deleteMirror } from '../bots/relay.ts';
import {
  autoDisableRule,
  bumpHitCounts,
  unionActions,
  type RuleEvaluation,
  type RuleHit,
  type RuleScope,
} from './engine.ts';

/**
 * 规则命中的处置执行。
 *
 * 与 `engine.ts` 的分工：引擎判断「命中了什么」，这里决定「因此做什么」。
 * 分开有一条硬性理由：面板里的正则沙盒要复用引擎，但绝不能因为管理员
 * 点了「测试」就真的去删消息或封禁用户。
 */

export interface HitContext {
  api: Api;
  botId: number;
  botName: string;
  adminGroupId: number;
  settings: BotSettings;
  contact: ContactRow;
  /** 用户消息（可处罚）还是管理员消息（仅审计） */
  scope: RuleScope;
  /** 触发规则的原始消息所在的聊天与 id */
  sourceChatId: number;
  sourceMessageId: number;
  /** 落库的中继记录 id；命中即拦截时为 null */
  messageRowId: number | null;
  topicId: number | null;
  threadId: number | null;
  /** 被匹配的原文，用于 matchedText 兜底 */
  rawText: string;
}

export interface ActionReport {
  outcomes: HitOutcome[];
  /** 是否拦截这条消息（不转发） */
  blocked: boolean;
  hitIds: number[];
  violation: ViolationOutcome | null;
  /** 本次把用户拉黑了 —— 调用方据此关闭话题 */
  banned: boolean;
}

/**
 * 写一条命中审计。
 *
 * `rulePattern` / `ruleFlags` 是**快照**：规则随时可能被改甚至被删，
 * 而审计记录必须永远能回答「当时是按什么判定的」。
 */
async function insertHit(
  ctx: HitContext,
  hit: RuleHit,
  outcomes: HitOutcome[],
): Promise<number | null> {
  const { db } = getDb();
  const now = Date.now();
  try {
    const inserted = await db
      .insert(ruleHits)
      .values({
        ruleId: hit.rule.id,
        ruleName: hit.rule.name,
        rulePattern: hit.rule.pattern,
        ruleFlags: hit.rule.flags,
        botId: ctx.botId,
        contactId: ctx.contact.id,
        topicId: ctx.topicId,
        messageId: ctx.messageRowId,
        matchedText: hit.matchedText || ctx.rawText.slice(0, 200),
        normalizedExcerpt: hit.normalizedExcerpt,
        outcomes,
        severity: hit.severity,
        createdAt: now,
      })
      .returning({ id: ruleHits.id });

    const id = inserted[0]?.id ?? null;
    if (id !== null) {
      publish(channel.global, 'rule.hit', {
        id,
        ruleId: hit.rule.id,
        ruleName: hit.rule.name,
        rulePattern: hit.rule.pattern,
        ruleFlags: hit.rule.flags,
        botId: ctx.botId,
        botName: ctx.botName,
        contactId: ctx.contact.id,
        contactName:
          [ctx.contact.firstName, ctx.contact.lastName].filter(Boolean).join(' ') || '未知用户',
        contactUsername: ctx.contact.username,
        tgUserId: ctx.contact.tgUserId,
        topicId: ctx.topicId,
        threadId: ctx.threadId,
        matchedText: hit.matchedText,
        normalizedExcerpt: hit.normalizedExcerpt,
        outcomes,
        severity: hit.severity,
        createdAt: now,
      });
    }
    return id;
  } catch (err) {
    logger.error({ err, ruleId: hit.rule.id }, '写命中审计失败');
    return null;
  }
}

/** 撤回用户私聊里的原消息。Bot API 明确允许删除私聊中的 incoming 消息 */
async function deleteOrigin(ctx: HitContext): Promise<boolean> {
  if (!ctx.settings.deleteOriginMessage) return false;
  try {
    await ctx.api.deleteMessage(ctx.sourceChatId, ctx.sourceMessageId);
    return true;
  } catch (err) {
    logger.debug({ err }, '删除用户原消息失败（通常是超过 48 小时）');
    return false;
  }
}

/** 处置动作 → 面向管理员的简短描述，用于告警卡片里的 `{outcome}` */
function describeOutcomes(outcomes: HitOutcome[], sanctionType: SanctionType | null): string {
  if (sanctionType === 'ban') return '拉黑';
  if (sanctionType === 'mute') return '禁言';
  if (sanctionType === 'silence') return '静默';
  if (sanctionType === 'warn') return '警告';

  const labels: Partial<Record<HitOutcome, string>> = {
    deleted: '已删除',
    delete_failed: '删除失败',
    notified: '已通知管理员',
    notify_failed: '通知失败',
    warned: '已警告',
    logged_only: '仅记录',
    regex_timeout: '规则超时',
  };
  const named = outcomes.map((o) => labels[o]).filter((v): v is string => Boolean(v));
  return named.length > 0 ? [...new Set(named)].join(' + ') : '仅记录';
}

function outcomeForSanction(type: SanctionType): HitOutcome {
  switch (type) {
    case 'warn':
      return 'warned';
    case 'silence':
      return 'silenced';
    case 'mute':
      return 'muted';
    case 'ban':
      return 'banned';
  }
}

/**
 * 执行一次命中处置。
 *
 * 顺序有讲究：**先删原消息，再发警告**。反过来会让用户在「收到警告」到
 * 「消息被删」之间有一个窗口去截图或转发 —— 这是真实存在的对抗场景。
 */
export async function applyRuleHits(
  ctx: HitContext,
  evaluation: RuleEvaluation,
): Promise<ActionReport> {
  const { hits, timedOutRuleIds } = evaluation;
  const adminScope = ctx.scope === 'admin';

  // 超时的规则先熔断：它对任何消息都可能超时，留着就是给中继持续放血。
  if (timedOutRuleIds.length > 0) {
    const settings = await getGlobalSettings();
    for (const ruleId of timedOutRuleIds) {
      const rule = hits.find((h) => h.rule.id === ruleId)?.rule;
      if (settings.autoDisableOnRegexTimeout) {
        const reason = `匹配超时（超过 ${settings.regexTimeoutMs}ms），疑似灾难性回溯`;
        await autoDisableRule(ruleId, reason);
        logger.warn({ ruleId, ruleName: rule?.name }, '规则因匹配超时被自动停用');
      }
      if (ctx.settings.notifyAdmins && ctx.threadId !== null) {
        await sendToTopic(
          ctx.api,
          ctx.adminGroupId,
          ctx.threadId,
          `⚙️ **规则匹配超时**\n\n规则 \`${rule?.name ?? ruleId}\` 在匹配时超时，疑似存在灾难性回溯。` +
            (settings.autoDisableOnRegexTimeout ? '已自动停用以保护服务。' : '请尽快在面板中改写它。'),
        );
      }
    }
  }

  if (hits.length === 0) {
    return { outcomes: [], blocked: false, hitIds: [], violation: null, banned: false };
  }

  const actions = unionActions(hits);
  const outcomes: HitOutcome[] = [];
  let violation: ViolationOutcome | null = null;

  // 管理员的消息只审计不处罚：规则是给终端用户定的，
  // 让管理员被自己的规则静默掉是荒谬的。
  if (adminScope) {
    outcomes.push('logged_only');
    const hitIds: number[] = [];
    for (const hit of hits) {
      const id = await insertHit(ctx, hit, outcomes);
      if (id !== null) hitIds.push(id);
    }
    return { outcomes, blocked: false, hitIds, violation: null, banned: false };
  }

  // 1. 删除：用户私聊里的原消息
  if (actions.has('delete')) {
    outcomes.push((await deleteOrigin(ctx)) ? 'deleted' : 'delete_failed');
  }

  // 2. 阶梯处罚（仅 escalate 动作触发）
  if (actions.has('escalate')) {
    // 取最高分而不是求和：三条轻规则叠成一次重罚会让阈值失去意义
    const severity = hits.reduce((max, hit) => Math.max(max, hit.severity), 0);
    violation = await recordViolation({
      botId: ctx.botId,
      contactId: ctx.contact.id,
      ruleId: hits[0]?.rule.id ?? null,
      severity,
      reason: 'rule_hit',
    });
    outcomes.push(
      violation.escalated && violation.step
        ? outcomeForSanction(violation.step.type)
        : 'logged_only',
    );
  }

  // 3. 用户侧通知：显式 warn 动作，或阶梯升级（升级优先，避免一次命中发两条）
  if (violation?.escalated && violation.step) {
    if (await notifyUserOfSanction(ctx, violation, hits[0]?.rule.name ?? '')) outcomes.push('warned');
  } else if (actions.has('warn')) {
    const text = renderTemplate(ctx.settings.warnTemplate, {
      name: ctx.contact.firstName ?? '',
      username: ctx.contact.username ?? '',
      id: ctx.contact.tgUserId,
      ruleName: hits[0]?.rule.name ?? '',
      matched: hits[0]?.matchedText ?? '',
      score: violation?.score ?? ctx.contact.violationScore,
      botName: ctx.botName,
    });
    if ((await sendToUser(ctx.api, ctx.botId, ctx.contact.tgUserId, text)) !== null) {
      outcomes.push('warned');
    }
  }

  // 4. 管理员侧告警卡片
  const shouldNotify =
    ctx.settings.notifyAdmins &&
    ctx.threadId !== null &&
    (actions.has('notify') ||
      (violation?.escalated === true && violation.step?.notifyAdmins === true));

  if (shouldNotify && ctx.threadId !== null) {
    outcomes.push((await sendAlertCard(ctx, hits, outcomes, violation)) ? 'notified' : 'notify_failed');
  }

  // 5. 落审计：每条命中一行（方案里的「每条都记，动作取并集」）
  const hitIds: number[] = [];
  for (const hit of hits) {
    const id = await insertHit(ctx, hit, outcomes);
    if (id !== null) hitIds.push(id);
  }
  await bumpHitCounts(hits.map((hit) => hit.rule.id));

  // 6. 拉黑后关闭话题。刻意「关闭」而不是「删除」：删除会丢掉全部历史，
  //    而拉黑往往正是最需要保留证据的时候。管理员仍可在 Telegram 里手动删。
  let banned = false;
  if (violation?.currentType === 'ban' && ctx.topicId !== null && ctx.threadId !== null) {
    banned = true;
    await closeTopic(ctx.api, ctx.adminGroupId, {
      id: ctx.topicId,
      messageThreadId: ctx.threadId,
    });
  }

  // 7. 撤回已转发进话题的副本（先转发、后新增规则命中的场景）
  if (ctx.messageRowId !== null && actions.has('delete')) {
    const { db } = getDb();
    const rows = await db
      .select({ destChatId: messages.destChatId, relayedMessageId: messages.relayedMessageId })
      .from(messages)
      .where(eq(messages.id, ctx.messageRowId))
      .limit(1);
    const row = rows[0];
    if (row?.destChatId !== null && row?.destChatId !== undefined && row.relayedMessageId !== null) {
      await deleteMirror(ctx.api, row.destChatId, row.relayedMessageId);
    }
  }

  if (ctx.topicId !== null) await touchTopic(ctx.topicId);

  const blocked =
    violation !== null &&
    (violation.currentType === 'silence' ||
      violation.currentType === 'mute' ||
      violation.currentType === 'ban');

  return { outcomes, blocked, hitIds, violation, banned };
}

/** 按阶梯档位给用户发提示文案 */
async function notifyUserOfSanction(
  ctx: HitContext,
  violation: ViolationOutcome,
  ruleName: string,
): Promise<boolean> {
  const step = violation.step;
  if (!step) return false;

  // 静默刻意不通知：它的全部意义就是让对方无感知。
  // 一发提示等于告诉对方「你的消息被吞了，换个号再来」。
  if (step.type === 'silence') return false;

  const template =
    step.type === 'mute'
      ? ctx.settings.muteTemplate
      : step.type === 'ban'
        ? ctx.settings.banTemplate
        : ctx.settings.warnTemplate;

  const text = renderTemplate(template, {
    name: ctx.contact.firstName ?? '',
    username: ctx.contact.username ?? '',
    id: ctx.contact.tgUserId,
    ruleName,
    matched: '',
    score: violation.score,
    until: formatUntil(violation.sanction?.expiresAt ?? null),
    botName: ctx.botName,
  });

  if (!text) return false;
  return (await sendToUser(ctx.api, ctx.botId, ctx.contact.tgUserId, text)) !== null;
}

/** 话题内的告警卡片 */
async function sendAlertCard(
  ctx: HitContext,
  hits: RuleHit[],
  outcomes: HitOutcome[],
  violation: ViolationOutcome | null,
): Promise<boolean> {
  if (ctx.threadId === null) return false;

  const primary = hits[0];
  const text = renderTemplate(ctx.settings.alertCardTemplate, {
    name: [ctx.contact.firstName, ctx.contact.lastName].filter(Boolean).join(' ') || '未知用户',
    username: ctx.contact.username ? `@${ctx.contact.username}` : '（无用户名）',
    id: ctx.contact.tgUserId,
    ruleName: hits.map((h) => h.rule.name).join('、'),
    matched: primary?.matchedText ?? '',
    outcome: describeOutcomes(outcomes, violation?.currentType ?? null),
    score: violation?.score ?? ctx.contact.violationScore,
    botName: ctx.botName,
    reason: primary?.rule.note ?? '',
  });

  const id = await sendToTopic(ctx.api, ctx.adminGroupId, ctx.threadId, text);
  return id !== null;
}
