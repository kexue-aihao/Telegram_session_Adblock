import { createPinia } from 'pinia';
import { createApp } from 'vue';
import App from './App.vue';
import { router } from './router';
import './styles/theme.css';

/**
 * 应用入口。
 *
 * Provider 的嵌套顺序有依赖：Pinia 必须在 Router 之前安装 ——
 * 导航守卫里要用 store，而守卫在首次导航时就会执行。
 */
const app = createApp(App);

app.use(createPinia());
app.use(router);
app.mount('#app');

/**
 * 前端异常上报。
 *
 * 面板跑在用户浏览器里，出了错服务端一无所知 —— 这是唯一的线索来源。
 * 只上报消息与位置，不上报任何用户数据。
 */
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
