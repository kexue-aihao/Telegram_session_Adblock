import type { AdminSession, LoginResult } from '@tgs/shared';
import type { FastifyReply, FastifyRequest } from 'fastify';
import { and, desc, eq, gt, isNull, sql } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { adminSessions, adminUsers, loginAttempts } from '../db/schema.ts';
import { recordAudit } from '../core/audit.ts';
import { hashToken, randomToken } from '../core/crypto.ts';
import { logger } from '../core/logger.ts';
import { hashPassword, verifyPassword } from '../core/password.ts';

/**
 * 单管理员面板的鉴权。
 *
 * 会话用「随机 token + 库中存哈希」而不是 JWT：面板要能**主动注销**
 * 某个会话（改了密码就该踢掉所有旧会话），而无状态 token 做不到这件事。
 * 存哈希则保证库被读走也无法直接冒用。
 */

export const SESSION_COOKIE = 'tgs_session';
const SESSION_TTL_MS = 7 * 24 * 60 * 60 * 1000;

/** 登录失败锁定策略 */
const MAX_ATTEMPTS = 5;
const ATTEMPT_WINDOW_MS = 15 * 60 * 1000;

/** 挂在 request 上的已认证身份 */
export interface AuthContext {
  adminUserId: number;
  username: string;
  sessionId: number;
}

declare module 'fastify' {
  interface FastifyRequest {
    auth?: AuthContext;
  }
}

export interface LoginOutcome extends LoginResult {
  /** 成功时返回明文 token，由路由写进 Cookie；失败时为 null */
  token: string | null;
}

/** 统计某 IP 在窗口内的失败次数，判断是否已触发锁定 */
async function recentFailures(ip: string): Promise<number> {
  const { db } = getDb();
  const since = Date.now() - ATTEMPT_WINDOW_MS;
  const rows = await db
    .select({ count: sql<number>`count(*)` })
    .from(loginAttempts)
    .where(
      and(
        eq(loginAttempts.ip, ip),
        eq(loginAttempts.succeeded, false),
        gt(loginAttempts.attemptedAt, since),
      ),
    );
  return rows[0]?.count ?? 0;
}

/** 最近一次成功登录的时间；成功后应清零失败计数，否则误触发的锁定会持续生效 */
async function lastSuccessAt(ip: string): Promise<number> {
  const { db } = getDb();
  const rows = await db
    .select({ attemptedAt: loginAttempts.attemptedAt })
    .from(loginAttempts)
    .where(and(eq(loginAttempts.ip, ip), eq(loginAttempts.succeeded, true)))
    .orderBy(desc(loginAttempts.attemptedAt))
    .limit(1);
  return rows[0]?.attemptedAt ?? 0;
}

async function recordAttempt(ip: string, succeeded: boolean): Promise<void> {
  const { db } = getDb();
  await db.insert(loginAttempts).values({ ip, succeeded, attemptedAt: Date.now() });
}

/**
 * 用于「账号不存在」分支的等时哈希，首次使用时生成并缓存。
 * 生成一次 Argon2id 大约几十毫秒，只发生一次，不值得为它做预热。
 */
let dummyHash: string | null = null;
async function getDummyHash(): Promise<string> {
  dummyHash ??= await hashPassword('timing-equalizer-not-a-real-password');
  return dummyHash;
}

export async function attemptLogin(
  password: string,
  context: { ip: string; userAgent: string | null },
): Promise<LoginOutcome> {
  const { db } = getDb();
  const failures = await recentFailures(context.ip);
  const lastSuccess = await lastSuccessAt(context.ip);

  // 窗口内既没有失败、或失败发生在最近一次成功之前，都不算数
  const effectiveFailures = lastSuccess > Date.now() - ATTEMPT_WINDOW_MS ? 0 : failures;

  if (effectiveFailures >= MAX_ATTEMPTS) {
    await recordAudit({
      actorType: 'admin',
      actorId: null,
      action: 'login.locked',
      ip: context.ip,
      detail: { failures: effectiveFailures },
    });
    return {
      ok: false,
      session: null,
      error: '尝试次数过多，请稍后再试',
      lockedForSeconds: Math.ceil(ATTEMPT_WINDOW_MS / 1000),
      attemptsLeft: 0,
      token: null,
    };
  }

  const admins = await db.select().from(adminUsers).orderBy(adminUsers.id).limit(1);
  const admin = admins[0];

  // 管理员账号不存在时也走一遍**真实的**哈希校验，让响应时间与「密码错误」
  // 一致 —— 否则攻击者能靠响应快慢判断账号是否存在。
  // 必须用真实生成的哈希：写死的假哈希会因为解析失败而立刻返回，
  // 反而让时间差更明显。
  const ok = admin
    ? await verifyPassword(admin.passwordHash, password)
    : await verifyPassword(await getDummyHash(), password);

  if (!ok || !admin) {
    await recordAttempt(context.ip, false);
    await recordAudit({
      actorType: 'admin',
      actorId: null,
      action: 'login.failure',
      ip: context.ip,
      detail: { attemptsLeft: MAX_ATTEMPTS - effectiveFailures - 1 },
    });

    const left = MAX_ATTEMPTS - effectiveFailures - 1;
    return {
      ok: false,
      session: null,
      // 刻意不区分「密码错误」与「账号不存在」：面板是单管理员的，
      // 区分这两者只会给攻击者提供信息。
      error: '密码错误',
      lockedForSeconds: null,
      attemptsLeft: Math.max(0, left),
      token: null,
    };
  }

  const token = randomToken(32);
  const now = Date.now();
  const expiresAt = now + SESSION_TTL_MS;

  await db.insert(adminSessions).values({
    tokenHash: hashToken(token),
    adminUserId: admin.id,
    ip: context.ip,
    userAgent: context.userAgent,
    createdAt: now,
    expiresAt,
    revokedAt: null,
  });

  await recordAttempt(context.ip, true);
  await recordAudit({
    actorType: 'admin',
    actorId: admin.username,
    action: 'login.success',
    ip: context.ip,
  });

  return {
    ok: true,
    session: {
      username: admin.username,
      createdAt: now,
      expiresAt,
      ip: context.ip,
      userAgent: context.userAgent,
    },
    error: null,
    lockedForSeconds: null,
    attemptsLeft: MAX_ATTEMPTS,
    token,
  };
}

/** 校验会话 token，返回身份；无效 / 过期 / 已注销都返回 null */
export async function verifySession(token: string): Promise<AuthContext | null> {
  const { db } = getDb();
  const rows = await db
    .select({
      sessionId: adminSessions.id,
      expiresAt: adminSessions.expiresAt,
      adminUserId: adminUsers.id,
      username: adminUsers.username,
    })
    .from(adminSessions)
    .innerJoin(adminUsers, eq(adminUsers.id, adminSessions.adminUserId))
    .where(
      and(
        eq(adminSessions.tokenHash, hashToken(token)),
        isNull(adminSessions.revokedAt),
        gt(adminSessions.expiresAt, Date.now()),
      ),
    )
    .limit(1);

  const row = rows[0];
  if (!row) return null;
  return { adminUserId: row.adminUserId, username: row.username, sessionId: row.sessionId };
}

export async function revokeSession(sessionId: number): Promise<void> {
  const { db } = getDb();
  await db.update(adminSessions).set({ revokedAt: Date.now() }).where(eq(adminSessions.id, sessionId));
}

/** 改密码后踢掉所有旧会话 —— 这正是「库中存哈希」换来的能力 */
export async function revokeAllSessions(exceptSessionId?: number): Promise<number> {
  const { db } = getDb();
  const rows = await db
    .select({ id: adminSessions.id })
    .from(adminSessions)
    .where(isNull(adminSessions.revokedAt));

  const targets = rows.filter((row) => row.id !== exceptSessionId);
  for (const row of targets) {
    await db.update(adminSessions).set({ revokedAt: Date.now() }).where(eq(adminSessions.id, row.id));
  }
  return targets.length;
}

export async function changePassword(
  adminUserId: number,
  currentPassword: string,
  newPassword: string,
): Promise<{ ok: boolean; error: string | null }> {
  const { db } = getDb();
  const rows = await db.select().from(adminUsers).where(eq(adminUsers.id, adminUserId)).limit(1);
  const admin = rows[0];
  if (!admin) return { ok: false, error: '管理员账号不存在' };

  if (!(await verifyPassword(admin.passwordHash, currentPassword))) {
    return { ok: false, error: '当前密码不正确' };
  }

  await db
    .update(adminUsers)
    .set({ passwordHash: await hashPassword(newPassword), updatedAt: Date.now() })
    .where(eq(adminUsers.id, adminUserId));

  logger.info({ adminUserId }, '管理员密码已更新');
  return { ok: true, error: null };
}

export function sessionCookieOptions(production: boolean) {
  return {
    httpOnly: true,
    // secure 在开发期必须为 false：本地是 http，带 secure 的 Cookie 根本不会被浏览器存下，
    // 表现为「登录成功但立刻又跳回登录页」，很难排查。
    secure: production,
    sameSite: 'lax' as const,
    path: '/',
    maxAge: Math.floor(SESSION_TTL_MS / 1000),
  };
}

/**
 * 路由的鉴权前置钩子。
 *
 * 放在 preHandler 而不是在 38 个 handler 里各写一遍 —— 漏掉一处就是
 * 一个未鉴权接口，而这种遗漏在代码评审里极难被发现。
 */
export async function requireAuth(
  request: FastifyRequest,
  reply: FastifyReply,
): Promise<void> {
  const token = request.cookies[SESSION_COOKIE];
  if (!token) {
    await reply.code(401).send({ error: '未登录' });
    return;
  }

  const auth = await verifySession(token);
  if (!auth) {
    // 会话已失效（过期 / 被踢）：顺手清掉 Cookie，前端就不必自己判断了
    reply.clearCookie(SESSION_COOKIE, { path: '/' });
    await reply.code(401).send({ error: '会话已过期，请重新登录' });
    return;
  }

  request.auth = auth;
}

/** 定期清理过期会话与登录记录，避免这两张表无限增长 */
export async function pruneAuthData(): Promise<void> {
  const { db } = getDb();
  const now = Date.now();
  const cutoff = now - 30 * 24 * 60 * 60 * 1000;
  try {
    await db.delete(adminSessions).where(sql`${adminSessions.expiresAt} < ${now}`);
    await db.delete(loginAttempts).where(sql`${loginAttempts.attemptedAt} < ${cutoff}`);
  } catch (err) {
    logger.warn({ err }, '清理鉴权数据失败');
  }
}
