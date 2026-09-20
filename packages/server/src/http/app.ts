import type { IncomingMessage, ServerResponse } from 'node:http';
import Fastify, {
  type FastifyBaseLogger,
  type FastifyInstance,
  type FastifyServerOptions,
  type FastifyTypeProviderDefault,
  type RawServerDefault,
} from 'fastify';
import { logger } from '../core/logger.ts';

/**
 * Fastify 实例的创建入口与**统一的类型别名**。
 *
 * 为什么需要这个看起来多余的包装：把 pino 实例传给 `loggerInstance` 时，
 * Fastify 会从参数反推出实例的 Logger 泛型（变成 pino 的 `Logger<never, boolean>`），
 * 于是每个路由注册函数的签名都会与它对不上 —— 报错出现在所有 route 调用上，
 * 且信息长得没法读。
 *
 * 解法是把 Logger 泛型**显式钉成 `FastifyBaseLogger`**（Fastify 自己的默认值），
 * 这样返回的实例就是标准的 `FastifyInstance`，其余代码全部按默认类型走。
 */

export type App = FastifyInstance;

/** 参数里排除 logger 相关选项 —— 它们正是会污染泛型推断的元凶 */
export type AppOptions = Omit<FastifyServerOptions, 'logger' | 'loggerInstance'>;

export function createApp(options: AppOptions): App {
  return Fastify<
    RawServerDefault,
    IncomingMessage,
    ServerResponse,
    FastifyBaseLogger,
    FastifyTypeProviderDefault
  >({
    ...options,
    // 断言是必要的也是安全的：pino 的 Logger 实现了 FastifyBaseLogger 要求的
    // 全部方法（info/warn/error/debug/fatal/trace/child/level），
    // 差的只是一个 pino 特有的 msgPrefix 字段。
    loggerInstance: logger as unknown as FastifyBaseLogger,
  });
}
