import { Navigate, Route, Routes } from 'react-router';
import { useAuth } from './auth.tsx';
import { AppShell } from './layout/AppShell.tsx';
import { Spinner } from './components/ui/primitives.tsx';
import { LoginPage } from './features/auth/LoginPage.tsx';
import { DashboardPage } from './features/dashboard/DashboardPage.tsx';
import { BotsPage } from './features/bots/BotsPage.tsx';
import { SessionsPage } from './features/sessions/SessionsPage.tsx';
import { RulesPage } from './features/rules/RulesPage.tsx';
import { AuditPage } from './features/audit/AuditPage.tsx';
import { SettingsPage } from './features/settings/SettingsPage.tsx';

/**
 * 路由表。
 *
 * AppShell 作为 layout route：侧边栏与顶栏在页面切换时**不卸载**，
 * 只有 `<Outlet />` 的内容做转场。这是「工具感」与「网站感」的分界线。
 */
export function App() {
  const { session, ready } = useAuth();

  // 首次探测会话期间显示一个极简的加载态，而不是先渲染登录页再跳走 ——
  // 那一下闪烁会让用户以为「登录掉了」。
  if (!ready) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner className="size-5 text-[var(--color-fg-subtle)]" />
      </div>
    );
  }

  if (!session) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        {/* 任何未登录的路径都回登录页；用 replace 避免在历史里堆一堆记录 */}
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }

  return (
    <Routes>
      {/* 已登录时 /login 直接回首页，避免手输地址后看到一个无用的登录表单 */}
      <Route path="/login" element={<Navigate to="/" replace />} />
      <Route element={<AppShell />}>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/bots" element={<BotsPage />} />
        <Route path="/bots/:botId" element={<BotsPage />} />
        <Route path="/sessions" element={<SessionsPage />} />
        <Route path="/sessions/:sessionId" element={<SessionsPage />} />
        <Route path="/rules" element={<RulesPage />} />
        <Route path="/audit" element={<AuditPage />} />
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
