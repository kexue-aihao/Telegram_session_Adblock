import {
  DEFAULT_ESCALATION,
  botSettingsSchema,
  globalSettingsSchema,
  type BotSettings,
  type GlobalSettings,
} from '@tgs/shared';
import { eq } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { appSettings, botSettings } from '../db/schema.ts';
import { DEFAULT_BOT_SETTINGS, DEFAULT_GLOBAL_SETTINGS, GLOBAL_SETTINGS_KEY } from '../db/seed.ts';
import { logger } from './logger.ts';

/**
 * 设置的读取层。
 *
 * 这两类设置都在**每条消息的热路径**上被读到（要不要删原消息、警告文案是什么、
 * 阈值多少），每次都查库等于给中继加一次磁盘往返，而设置几乎不变。
 * 因此：进程内缓存 + 写入时失效。
 *
 * 缓存失效刻意做成「写入方负责」，而不是 TTL：TTL 会让「刚在面板改完设置、
 * 立刻测试」的流程出现「改了没生效」的困惑，这正是最难排查的一类问题。
 */

let globalCache: GlobalSettings | null = null;
const botCache = new Map<number, BotSettings>();

/**
 * 解析失败时回落到默认值而不是抛错。
 *
 * 这些值直接决定消息要不要被删、用户要不要被禁言 —— 一次手动改库改坏了
 * 就让整个机器人停止中继，代价远大于「静默用回默认配置」。
 */
function parseGlobal(raw: unknown): GlobalSettings {
  const parsed = globalSettingsSchema.safeParse(raw ?? {});
  if (parsed.success) return parsed.data;

  logger.warn({ issues: parsed.error.issues }, '全局设置解析失败，已回落到默认值');
  return globalSettingsSchema.parse({ ...DEFAULT_GLOBAL_SETTINGS });
}

function parseBotSettings(botId: number, raw: unknown): BotSettings {
  // 不能直接 `{ ...raw }` —— `unknown` 不可展开。这里显式判断一次，
  // 顺带把「库里存了 null / 字符串」这种脏数据挡在 schema 之前。
  const candidate = typeof raw === 'object' && raw !== null ? { ...raw, botId } : { botId };
  const parsed = botSettingsSchema.safeParse(candidate);
  if (parsed.success) return parsed.data;

  logger.warn({ botId, issues: parsed.error.issues }, '机器人设置解析失败，已回落到默认值');
  return botSettingsSchema.parse({ ...DEFAULT_BOT_SETTINGS, botId });
}

export async function getGlobalSettings(): Promise<GlobalSettings> {
  if (globalCache) return globalCache;

  const { db } = getDb();
  const rows = await db
    .select({ value: appSettings.value })
    .from(appSettings)
    .where(eq(appSettings.key, GLOBAL_SETTINGS_KEY))
    .limit(1);

  globalCache = parseGlobal(rows[0]?.value);
  return globalCache;
}

export async function updateGlobalSettings(patch: Partial<GlobalSettings>): Promise<GlobalSettings> {
  const current = await getGlobalSettings();
  const next = globalSettingsSchema.parse({ ...current, ...patch });

  const { db } = getDb();
  await db
    .insert(appSettings)
    .values({ key: GLOBAL_SETTINGS_KEY, value: next, updatedAt: Date.now() })
    .onConflictDoUpdate({
      target: appSettings.key,
      set: { value: next, updatedAt: Date.now() },
    });

  globalCache = next;
  return next;
}

export async function getBotSettings(botId: number): Promise<BotSettings> {
  const cached = botCache.get(botId);
  if (cached) return cached;

  const { db } = getDb();
  const rows = await db.select().from(botSettings).where(eq(botSettings.botId, botId)).limit(1);

  // 补齐行而不是抛错：settings 行缺失只可能来自手工改库或早期版本升级，
  // 让机器人因为「少了条配置」而拒绝服务是没有道理的。
  if (!rows[0]) {
    await db
      .insert(botSettings)
      .values({ botId, ...DEFAULT_BOT_SETTINGS, escalation: DEFAULT_ESCALATION })
      .onConflictDoNothing();

    const fresh = parseBotSettings(botId, DEFAULT_BOT_SETTINGS);
    botCache.set(botId, fresh);
    return fresh;
  }

  const parsed = parseBotSettings(botId, rows[0]);
  botCache.set(botId, parsed);
  return parsed;
}

export async function updateBotSettings(
  botId: number,
  patch: Partial<BotSettings>,
): Promise<BotSettings> {
  const current = await getBotSettings(botId);
  const next = botSettingsSchema.parse({ ...current, ...patch, botId });

  const { db } = getDb();
  // 全列覆盖而不是只更新 patch 里的列：schema 里没有可选列，
  // 逐列拼 SET 只会增加「漏掉某个字段」的机会。
  await db
    .insert(botSettings)
    .values({ ...next, escalation: next.escalation })
    .onConflictDoUpdate({ target: botSettings.botId, set: next });

  botCache.set(botId, next);
  return next;
}

/** 机器人被删除时清掉它的缓存，避免 id 复用后读到上一个机器人的配置 */
export function invalidateBotSettings(botId: number): void {
  botCache.delete(botId);
}

/** 仅供测试与「恢复默认」使用 */
export function invalidateAllSettings(): void {
  globalCache = null;
  botCache.clear();
}
