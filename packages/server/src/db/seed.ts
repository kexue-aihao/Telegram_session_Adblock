import {
  DEFAULT_ESCALATION,
  DEFAULT_ALERT_CARD_TEMPLATE,
  DEFAULT_BAN_TEMPLATE,
  DEFAULT_GREETING_TEXT,
  DEFAULT_MUTE_TEMPLATE,
  DEFAULT_SILENCE_TEMPLATE,
  DEFAULT_TOPIC_HEADER_TEMPLATE,
  DEFAULT_TOPIC_ICON_COLOR,
  DEFAULT_TOPIC_NAME_TEMPLATE,
  DEFAULT_WARN_TEMPLATE,
  SEED_RULES,
} from '@tgs/shared';
import { logger } from '../core/logger.ts';
import { hashPassword } from '../core/password.ts';
import { loadEnv } from '../env.ts';
import { getDb } from './client.ts';
import { adRules, adminUsers, appSettings, botSettings, bots } from './schema.ts';

/**
 * 首次启动的播种。
 *
 * 三个都不做「有则覆盖」：管理员改过的密码、设置、规则必须活过重启。
 * 因此每一项都是「只在缺失时写入」。
 */

export const GLOBAL_SETTINGS_KEY = 'global';

/** 全局设置默认值；与 @tgs/shared 的 globalSettingsSchema 保持一致 */
export const DEFAULT_GLOBAL_SETTINGS = {
  timezone: 'Asia/Shanghai',
  auditRetentionDays: 180,
  autoDisableOnRegexTimeout: true,
  regexTimeoutMs: 50,
} as const;

/** 新机器人的默认配置；与 botSettingsSchema 的默认值同源 */
export const DEFAULT_BOT_SETTINGS = {
  topicNameTemplate: DEFAULT_TOPIC_NAME_TEMPLATE,
  topicIconColor: DEFAULT_TOPIC_ICON_COLOR,
  autoCloseHours: 72,
  pinTopicHeader: true,
  greetingText: DEFAULT_GREETING_TEXT,
  warnTemplate: DEFAULT_WARN_TEMPLATE,
  muteTemplate: DEFAULT_MUTE_TEMPLATE,
  banTemplate: DEFAULT_BAN_TEMPLATE,
  silenceTemplate: DEFAULT_SILENCE_TEMPLATE,
  alertCardTemplate: DEFAULT_ALERT_CARD_TEMPLATE,
  topicHeaderTemplate: DEFAULT_TOPIC_HEADER_TEMPLATE,
  rulesEnabled: true,
  notifyAdmins: true,
  escalation: DEFAULT_ESCALATION,
  deleteOriginMessage: true,
  mirrorEdits: true,
  mirrorDeletes: true,
  coalesceWindowMs: 400,
  coalesceThreshold: 5,
  floodThreshold: 8,
  notifyOnUnreachable: true,
} as const;

async function seedAdmin(): Promise<void> {
  const { db } = getDb();
  const env = loadEnv();

  const existing = await db.select({ id: adminUsers.id }).from(adminUsers).limit(1);
  if (existing.length > 0) return;

  if (!env.ADMIN_PASSWORD) {
    throw new Error(
      '首次启动需要设置 ADMIN_PASSWORD 才能创建管理员账号。\n' +
        '请在 .env 中填入一个至少 8 位、同时包含字母和数字的密码后重启。',
    );
  }

  const now = Date.now();
  await db.insert(adminUsers).values({
    username: env.ADMIN_USERNAME,
    passwordHash: await hashPassword(env.ADMIN_PASSWORD),
    createdAt: now,
    updatedAt: now,
  });
  logger.info({ username: env.ADMIN_USERNAME }, '已创建管理员账号（密码取自 ADMIN_PASSWORD）');
}

async function seedGlobalSettings(): Promise<void> {
  const { db } = getDb();
  await db
    .insert(appSettings)
    .values({
      key: GLOBAL_SETTINGS_KEY,
      value: { ...DEFAULT_GLOBAL_SETTINGS },
      updatedAt: Date.now(),
    })
    .onConflictDoNothing();
}

async function seedRules(): Promise<void> {
  const { db } = getDb();

  const existing = await db.select({ id: adRules.id }).from(adRules).limit(1);
  if (existing.length > 0) return;

  const now = Date.now();
  await db.insert(adRules).values(
    SEED_RULES.map((rule) => ({
      botId: null,
      name: rule.name,
      pattern: rule.pattern,
      flags: rule.flags,
      matchMode: rule.matchMode,
      target: rule.target,
      action: rule.action,
      severity: rule.severity,
      priority: rule.priority,
      isEnabled: rule.enabled,
      isSystem: rule.isSystem,
      note: rule.note,
      hitCount: 0,
      lastHitAt: null,
      autoDisabledAt: null,
      autoDisabledReason: null,
      createdAt: now,
      updatedAt: now,
    })),
  );
  logger.info({ count: SEED_RULES.length }, '已写入预置广告规则');
}

/**
 * 为已存在的机器人补齐缺失的 settings 行。
 * 正常路径不会用到（建 bot 时就会写），但手工改库或早期版本升级后会需要。
 */
async function backfillBotSettings(): Promise<void> {
  const { db } = getDb();
  const rows = await db.select({ id: bots.id }).from(bots);
  if (rows.length === 0) return;

  for (const row of rows) {
    await db
      .insert(botSettings)
      .values({ botId: row.id, ...DEFAULT_BOT_SETTINGS, escalation: DEFAULT_ESCALATION })
      .onConflictDoNothing();
  }
}

export async function seed(): Promise<void> {
  await seedAdmin();
  await seedGlobalSettings();
  await seedRules();
  await backfillBotSettings();
}
