import type { z } from 'zod';

/**
 * 请求校验的两个小工具。
 *
 * 刻意不引入 fastify-type-provider-zod 这类插件：它会把 zod 的错误格式
 * 绑定到 fastify 的错误处理链上，而本项目希望所有 4xx 的响应体形状统一是
 * `{ error: string }` —— 前端只需要处理一种错误结构。
 *
 * 校验失败抛出的错误带 `statusCode`，由 server.ts 的 errorHandler 统一转成响应。
 */

export class HttpError extends Error {
  readonly statusCode: number;
  readonly detail: unknown;

  constructor(statusCode: number, message: string, detail?: unknown) {
    super(message);
    this.name = 'HttpError';
    this.statusCode = statusCode;
    this.detail = detail;
  }
}

/** 把 zod 的 issue 列表压成一行人话；面板直接展示它 */
function formatIssues(error: z.ZodError): string {
  return error.issues
    .map((issue) => {
      const path = issue.path.join('.');
      return path ? `${path}: ${issue.message}` : issue.message;
    })
    .join('；');
}

export function parseBody<T>(schema: z.ZodType<T>, body: unknown): T {
  const result = schema.safeParse(body ?? {});
  if (!result.success) {
    throw new HttpError(400, formatIssues(result.error), result.error.issues);
  }
  return result.data;
}

export function parseQuery<T>(schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value ?? {});
  if (!result.success) {
    throw new HttpError(400, formatIssues(result.error), result.error.issues);
  }
  return result.data;
}

/**
 * 直接抛 4xx 的便捷函数。
 * 用异常而不是 `reply.code().send()`，是为了让路由体里能保持一条直线、
 * 不必在每个分支都 return。
 */
export function badRequest(message: string): never {
  throw new HttpError(400, message);
}

export function notFound(message: string): never {
  throw new HttpError(404, message);
}

export function conflict(message: string): never {
  throw new HttpError(409, message);
}
