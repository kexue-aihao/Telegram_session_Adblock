import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import type { AdminSession } from '@tgs/shared';
import { api, onUnauthorized } from './lib/api.ts';
import { ws } from './lib/ws.ts';

/**
 * 面板会话。
 *
 * 全局只有一个管理员，所以这里的状态极其简单 —— 但**初始探测**必须做对：
 * 页面刷新时 Cookie 还在，可内存里没有会话信息。在探测完成之前不能渲染
 * 任何需要登录的界面，否则会先闪一下登录页再跳回来。
 */

interface AuthState {
  session: AdminSession | null;
  /** 首次探测是否完成；未完成时应当显示骨架而不是登录页 */
  ready: boolean;
  login: (password: string) => Promise<{ ok: boolean; error?: string; attemptsLeft?: number | null }>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function useAuth(): AuthState {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth 必须在 AuthProvider 内部使用');
  return context;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<AdminSession | null>(null);
  const [ready, setReady] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const result = await api.get<{ session: AdminSession | null }>('/api/auth/me');
      setSession(result.session);
    } catch {
      setSession(null);
    } finally {
      setReady(true);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // 任何一个请求收到 401 都说明会话没了（过期或被其它设备踢掉）
  useEffect(
    () =>
      onUnauthorized(() => {
        setSession(null);
        // 连接也要断掉：服务端已经不会给它投递任何事件了
        ws.close();
      }),
    [],
  );

  const login = useCallback<AuthState['login']>(async (password) => {
    try {
      const result = await api.post<{
        ok: boolean;
        session: AdminSession | null;
        error: string | null;
        attemptsLeft: number | null;
      }>('/api/auth/login', { password });

      if (result.ok && result.session) {
        setSession(result.session);
        ws.connect();
        return { ok: true };
      }
      return { ok: false, error: result.error ?? '登录失败', attemptsLeft: result.attemptsLeft };
    } catch (err) {
      // 这里不用 ApiError 的 status 分支：登录接口的 401 是**业务失败**
      // （密码错），不是会话失效，不该触发上面的 onUnauthorized 广播。
      return { ok: false, error: err instanceof Error ? err.message : '登录失败' };
    }
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.post('/api/auth/logout');
    } finally {
      setSession(null);
      ws.close();
    }
  }, []);

  // 登录后建立 WebSocket；未登录时不连（服务端会直接拒绝）
  useEffect(() => {
    if (session) ws.connect();
  }, [session]);

  const value = useMemo<AuthState>(
    () => ({ session, ready, login, logout, refresh }),
    [session, ready, login, logout, refresh],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
