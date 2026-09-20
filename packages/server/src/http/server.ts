import cookie from '@fastify/cookie';
import rateLimit from '@fastify/rate-limit';
import fastifyStatic from '@fastify/static';
import websocket from '@fastify/websocket';
import { existsSync } from 'node:fs';
import path from 'node:path';
import { sql } from 'drizzle-orm';
import { getDb } from '../db/client.ts';
import { loadEnv } from '../env.ts';
import { logger } from '../core/logger.ts';
import { createApp, type App } from './app.ts';
import { HttpError } from './validate.ts';
import { registerWebsocket } from './ws.ts';
import { registerAuthRoutes } from './routes/auth.ts';
import { registerBotRoutes } from './routes/bots.ts';
import { registerSessionRoutes } from './routes/sessions.ts';
import { registerRuleRoutes } from './routes/rules.ts';
import { registerAuditRoutes } from './routes/audit.ts';
import { registerStatsRoutes } from './routes/stats.ts';

/**
 * HTTP 服务。
 *
 * 生产环境下同一个进程既提供 API 也托管前端构建产物 —— 自托管面板
 * 不需要再拖一个 Nginx 进来。开发期前端跑在 Vite 上，这里只当 API。
 */

export interface ServerHandle {
  app: App;
  close: () => Promise<void>;
}

export async function buildServer(): Promise<ServerHandle> {
  const env = loadEnv();

  const app = createApp({
    // 面板部署在反代之后时，request.ip 才是真实客户端 IP。
    // 不信任 X-Forwarded-For 会让登录限流把所有请求当成同一个 IP。
    trustProxy: true,
    bodyLimit: 2 * 1024 * 1024,
  });

  await app.register(cookie, { secret: env.SESSION_SECRET });
  await app.register(rateLimit, {
    global: false, // 默认不限流，只有登录等敏感接口显式开启
    max: 200,
    timeWindow: '1 minute',
  });
  await app.register(websocket);

  registerHealthRoutes(app);
  await registerAuthRoutes(app);
  await registerBotRoutes(app);
  await registerSessionRoutes(app);
  await registerRuleRoutes(app);
  await registerAuditRoutes(app);
  await registerStatsRoutes(app);
  registerWebsocket(app);
  registerStatic(app, env.WEB_DIST ?? defaultWebDist());

  /**
   * 统一错误出口。
   *
   * 前端只需要处理一种错误结构：`{ error: string }`。
   * zod 校验失败、fastify 自身的 4xx、以及未预期的异常都在这里被翻译成它。
   */
  app.setErrorHandler((error, request, reply) => {
    if (error instanceof HttpError) {
      return reply.code(error.statusCode).send({ error: error.message, detail: error.detail });
    }

    const err = error as { statusCode?: number; message: string; stack?: string };
    const status = err.statusCode ?? 500;
    if (status >= 500) {
      logger.error({ err: error, url: request.url, method: request.method }, '请求处理失败');
      // 500 不回传内部错误细节：堆栈与 SQL 片段不该出现在浏览器里
      return reply.code(500).send({ error: '服务器内部错误，请查看服务端日志' });
    }

    return reply.code(status).send({ error: err.message });
  });

  app.setNotFoundHandler((request, reply) => {
    if (request.url.startsWith('/api/') || request.url.startsWith('/ws')) {
      return reply.code(404).send({ error: '接口不存在' });
    }
    // 其余路径交给 SPA：前端路由（/bots、/rules……）刷新时都靠这一条兜底
    if (hasWebDist) {
      return reply.sendFile('index.html');
    }
    return reply
      .code(404)
      .send({ error: '前端产物未构建 —— 开发期请访问 Vite 的地址（默认 5173）' });
  });

  return {
    app,
    close: async () => {
      await app.close();
    },
  };
}

let hasWebDist = false;

/** 健康检查：`/api/health` 免鉴权，供 Docker healthcheck 与外部探针使用 */
function registerHealthRoutes(app: App): void {
  const startedAt = Date.now();
  const env = loadEnv();

  app.get('/api/health', async () => ({
    ok: true,
    service: 'tgs-server',
    version: '0.1.0',
    uptimeSeconds: Math.floor((Date.now() - startedAt) / 1000),
    env: env.NODE_ENV,
  }));

  /**
   * 带依赖检查的就绪探针。
   * 与上面分开：存活探针返回 200 表示「进程还在」，就绪探针要能回答
   * 「现在能不能干活」—— 数据库连不上时必须返回 503。
   */
  app.get('/api/health/ready', async (_request, reply) => {
    try {
      const { db } = getDb();
      await db.run(sql`select 1`);
      return { ok: true, database: 'up', checkedAt: Date.now() };
    } catch (err) {
      logger.error({ err }, '就绪检查失败');
      return reply.code(503).send({ ok: false, database: 'down', error: (err as Error).message });
    }
  });

  /**
   * 前端上报的客户端异常。
   * 面板跑在用户浏览器里，出了错我们这边一无所知 —— 这个接口是唯一的线索来源。
   */
  app.post('/api/health/client-error', async (request) => {
    logger.warn({ clientError: request.body }, '前端上报异常');
    return { ok: true };
  });
}

/** 生产构建产物的默认位置：packages/web/dist */
function defaultWebDist(): string {
  return path.resolve(import.meta.dirname, '../../../web/dist');
}

function registerStatic(app: App, distDir: string): void {
  if (!existsSync(distDir)) {
    logger.warn({ distDir }, '未找到前端产物，仅提供 API（开发期请使用 pnpm dev）');
    return;
  }

  hasWebDist = true;
  void app.register(fastifyStatic, {
    root: distDir,
    prefix: '/',
    // index.html 不缓存，否则用户会一直拿到旧版本的页面外壳，
    // 而带 hash 的静态资源可以放心长期缓存
    // 注意这里的 res 是 FastifyReply 而不是 Node 原生 response，
    // 所以要用 .header() 而不是 .setHeader()。
    setHeaders: (reply, filePath) => {
      if (filePath.endsWith('index.html')) {
        reply.header('Cache-Control', 'no-cache');
      } else if (filePath.includes('/assets/') || filePath.includes('\\assets\\')) {
        reply.header('Cache-Control', 'public, max-age=31536000, immutable');
      }
    },
  });

  logger.info({ distDir }, '已挂载前端产物');
}
