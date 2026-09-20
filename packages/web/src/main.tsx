import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { AuthProvider } from './auth.tsx';
import { ToastProvider } from './components/ui/toast.tsx';
import { App } from './App.tsx';
import './styles/theme.css';

/**
 * 应用入口。
 *
 * Provider 的嵌套顺序有依赖：Toast 在外层，这样 AuthProvider 内部的
 * 任何逻辑（比如会话失效）都能弹提示；AuthProvider 又必须在 Router 之内
 * 吗？—— 不需要，它只依赖 fetch 与 WebSocket。但 Router 必须在 App 之外，
 * 因为 AppShell 里要用 useNavigate。
 */

const container = document.getElementById('root');
if (!container) {
  throw new Error('找不到 #root 挂载点，index.html 可能被改坏了');
}

// 前端异常上报：面板跑在用户浏览器里，出了错服务端一无所知。
// 这里只上报消息与组件栈，不上报用户数据。
window.addEventListener('error', (event) => {
  void fetch('/api/health/client-error', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      message: event.message,
      source: event.filename,
      lineno: event.lineno,
      colno: event.colno,
    }),
  }).catch(() => undefined);
});

window.addEventListener('unhandledrejection', (event) => {
  void fetch('/api/health/client-error', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message: String(event.reason) }),
  }).catch(() => undefined);
});

createRoot(container).render(
  <StrictMode>
    <ToastProvider>
      <BrowserRouter>
        <AuthProvider>
          <App />
        </AuthProvider>
      </BrowserRouter>
    </ToastProvider>
  </StrictMode>,
);
