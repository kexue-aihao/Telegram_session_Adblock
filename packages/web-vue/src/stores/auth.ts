import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import { api, setUnauthorizedHandler } from '@/lib/api';
import { ws } from '@/lib/ws';
import type { AdminSession } from '@/lib/types';

/**
 * 面板会话。
 *
 * 全局只有一个管理员，所以状态极其简单 —— 但**初始探测**必须做对：
 * 页面刷新时 Cookie 还在，可内存里没有会话信息。在探测完成之前不能渲染
 * 任何需要登录的界面，否则会先闪一下登录页再跳回来。
 */
export const useAuthStore = defineStore('auth', () => {
  const session = ref<AdminSession | null>(null);
  /** 首次探测是否完成；未完成时应当显示骨架而不是登录页 */
  const ready = ref(false);

  const isAuthenticated = computed(() => session.value !== null);

  async function refresh(): Promise<void> {
    try {
      const result = await api.probe<{ session: AdminSession | null }>('/api/auth/me');
      session.value = result.session;
    } catch {
      session.value = null;
    } finally {
      ready.value = true;
    }
  }

  async function login(
    password: string,
  ): Promise<{ ok: boolean; error?: string; attemptsLeft?: number | null }> {
    try {
      const result = await api.post<{
        ok: boolean;
        session: AdminSession | null;
        error: string | null;
        attemptsLeft: number | null;
      }>('/api/auth/login', { password });

      if (result.ok && result.session) {
        session.value = result.session;
        ws.connect();
        return { ok: true };
      }
      return {
        ok: false,
        error: result.error ?? '登录失败',
        attemptsLeft: result.attemptsLeft,
      };
    } catch (err) {
      // 这里不用 ApiError 的 status 分支：登录接口的 401 是**业务失败**
      // （密码错），不是会话失效，不该触发上面的 onUnauthorized 广播。
      return { ok: false, error: err instanceof Error ? err.message : '登录失败' };
    }
  }

  async function logout(): Promise<void> {
    try {
      await api.post('/api/auth/logout');
    } finally {
      session.value = null;
      ws.close();
    }
  }

  /** 登录之后建立 WebSocket；由 App 在挂载时调用一次 */
  function connectRealtime(): void {
    if (session.value) ws.connect();
  }

  /** 会话失效时的清理；由 setUnauthorizedHandler 触发 */
  function handleExpired(): void {
    session.value = null;
    // 连接也要断掉：服务端已经不会给它投递任何事件了
    ws.close();
  }

  // 任何一个请求收到 401 都说明会话没了（过期或被踢掉）
  setUnauthorizedHandler(handleExpired);

  return { session, ready, isAuthenticated, refresh, login, logout, connectRealtime };
});
