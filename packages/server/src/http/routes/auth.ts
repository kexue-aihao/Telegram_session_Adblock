import { changePasswordInputSchema, loginInputSchema, type AdminSession } from '@tgs/shared';
import { eq } from 'drizzle-orm';
import type { App } from '../app.ts';
import { getDb } from '../../db/client.ts';
import { adminSessions } from '../../db/schema.ts';
import { recordAudit } from '../../core/audit.ts';
import { loadEnv } from '../../env.ts';
import {
  SESSION_COOKIE,
  attemptLogin,
  changePassword,
  requireAuth,
  revokeAllSessions,
  revokeSession,
  sessionCookieOptions,
  verifySession,
} from '../auth.ts';
import { parseBody } from '../validate.ts';

export async function registerAuthRoutes(app: App): Promise<void> {
  const env = loadEnv();

  /**
   * 登录。
   *
   * 限流在这里是**双重**的：`@fastify/rate-limit` 按 IP 限制请求速率，
   * 而 `attemptLogin` 内部按 IP 统计失败次数做锁定。前者防高频撞库，
   * 后者防慢速但持续的密码猜测 —— 只做其中一个都会留下缺口。
   */
  app.post(
    '/api/auth/login',
    {
      config: {
        rateLimit: { max: 10, timeWindow: '1 minute' },
      },
    },
    async (request, reply) => {
      const input = parseBody(loginInputSchema, request.body);
      const outcome = await attemptLogin(input.password, {
        ip: request.ip,
        userAgent: request.headers['user-agent'] ?? null,
      });

      if (!outcome.ok || outcome.token === null) {
        return reply.code(401).send({
          ok: false,
          session: null,
          error: outcome.error,
          lockedForSeconds: outcome.lockedForSeconds,
          attemptsLeft: outcome.attemptsLeft,
        });
      }

      reply.setCookie(SESSION_COOKIE, outcome.token, sessionCookieOptions(env.isProduction));
      return {
        ok: true,
        session: outcome.session,
        error: null,
        lockedForSeconds: null,
        attemptsLeft: outcome.attemptsLeft,
      };
    },
  );

  /** 当前会话；未登录返回 200 + session:null，而不是 401 —— 前端用它做初始探测 */
  app.get('/api/auth/me', async (request) => {
    const token = request.cookies[SESSION_COOKIE];
    if (!token) return { session: null };

    const auth = await verifySession(token);
    if (!auth) return { session: null };

    const { db } = getDb();
    const rows = await db
      .select()
      .from(adminSessions)
      .where(eq(adminSessions.id, auth.sessionId))
      .limit(1);
    const row = rows[0];
    if (!row) return { session: null };

    const session: AdminSession = {
      username: auth.username,
      createdAt: row.createdAt,
      expiresAt: row.expiresAt,
      ip: row.ip,
      userAgent: row.userAgent,
    };
    return { session };
  });

  app.post('/api/auth/logout', { preHandler: requireAuth }, async (request, reply) => {
    const auth = request.auth;
    if (auth) {
      await revokeSession(auth.sessionId);
      await recordAudit({
        actorType: 'admin',
        actorId: auth.username,
        action: 'logout',
        ip: request.ip,
      });
    }
    reply.clearCookie(SESSION_COOKIE, { path: '/' });
    return { ok: true };
  });

  /**
   * 改密码。
   *
   * 默认踢掉**其它**会话而保留当前这一个：刚改完密码就被登出，
   * 会让人以为改失败了。其它设备上的会话必须失效，那才是改密码的意义。
   */
  app.post('/api/auth/password', { preHandler: requireAuth }, async (request, reply) => {
    const input = parseBody(changePasswordInputSchema, request.body);
    const auth = request.auth;
    if (!auth) return reply.code(401).send({ error: '未登录' });

    const result = await changePassword(auth.adminUserId, input.currentPassword, input.newPassword);
    if (!result.ok) return reply.code(400).send({ error: result.error });

    const revoked = await revokeAllSessions(auth.sessionId);
    await recordAudit({
      actorType: 'admin',
      actorId: auth.username,
      action: 'password.changed',
      detail: { revokedSessions: revoked },
      ip: request.ip,
    });

    return { ok: true, revokedSessions: revoked };
  });
}
