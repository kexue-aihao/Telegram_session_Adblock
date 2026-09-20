/**
 * 后端 API 客户端。
 *
 * 统一在一个地方处理三件事，避免它们在几十个调用点各写一遍：
 *  1. 会话失效（401）→ 广播一个事件，由路由层统一跳登录页；
 *  2. 错误形状归一 —— 后端所有 4xx/5xx 都是 `{ error: string }`，
 *     这里把它转成 ApiError，调用方只需要 try/catch 一种东西；
 *  3. 查询串拼装 —— 过滤掉 undefined/null，否则会拼出 `?q=undefined`。
 */

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

type UnauthorizedHandler = () => void;

let onUnauthorized: UnauthorizedHandler | null = null;

/** 注册会话失效的回调；由 auth store 在初始化时调用一次。 */
export function setUnauthorizedHandler(handler: UnauthorizedHandler): void {
  onUnauthorized = handler;
}

export type QueryValue = string | number | boolean | undefined | null;

function toQuery(params: Record<string, QueryValue>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue;
    search.set(key, String(value));
  }
  const result = search.toString();
  return result ? `?${result}` : '';
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE';
  body?: unknown;
  query?: Record<string, QueryValue>;
  /** 401 时不广播（用于 `/api/auth/me` 这类探测接口） */
  silentUnauthorized?: boolean;
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, query, silentUnauthorized } = options;

  let response: Response;
  try {
    response = await fetch(`${path}${query ? toQuery(query) : ''}`, {
      method,
      // same-origin + HttpOnly Cookie：显式写出来是为了让
      // 「这里依赖会话 Cookie」这件事在代码里可见
      credentials: 'same-origin',
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (err) {
    // 网络层失败（断网、服务未启动）与 HTTP 错误必须区分开：
    // 前者提示「无法连接服务器」，后者提示具体业务错误
    throw new ApiError(0, '无法连接服务器，请检查服务是否正在运行');
  }

  const text = await response.text();
  let parsed: unknown = null;
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = { error: text.slice(0, 300) };
    }
  }

  if (!response.ok) {
    const payload = parsed as { error?: string } | null;
    const message = payload?.error ?? `请求失败（HTTP ${response.status}）`;

    if (response.status === 401 && !silentUnauthorized) {
      onUnauthorized?.();
    }
    throw new ApiError(response.status, message);
  }

  return parsed as T;
}

export const api = {
  get: <T>(path: string, query?: Record<string, QueryValue>) => request<T>(path, { query }),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: 'POST', body }),
  patch: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PATCH', body }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
  /** 会话探测专用：401 不触发跳转 */
  probe: <T>(path: string) => request<T>(path, { silentUnauthorized: true }),
};
