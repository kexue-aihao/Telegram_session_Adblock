import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router';
import { useAuthStore } from '@/stores/auth';

/**
 * 路由表。
 *
 * AppShell 作为父路由：侧边栏与顶栏在页面切换时**不卸载**，
 * 只有 <RouterView> 的内容做转场。这是「工具感」与「网站感」的分界线 ——
 * 侧边栏跟着闪一下会立刻暴露出这是一个多页应用。
 */
const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/',
    component: () => import('@/layout/AppShell.vue'),
    children: [
      {
        path: '',
        name: 'dashboard',
        component: () => import('@/views/DashboardView.vue'),
        meta: { title: '仪表盘' },
      },
      {
        path: 'bots',
        name: 'bots',
        component: () => import('@/views/BotsView.vue'),
        meta: { title: '机器人' },
      },
      {
        path: 'sessions/:id?',
        name: 'sessions',
        component: () => import('@/views/SessionsView.vue'),
        meta: { title: '会话' },
      },
      {
        path: 'rules',
        name: 'rules',
        component: () => import('@/views/RulesView.vue'),
        meta: { title: '规则' },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('@/views/AuditView.vue'),
        meta: { title: '审计' },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/views/SettingsView.vue'),
        meta: { title: '设置' },
      },
    ],
  },
  // 兜底：未匹配的路径回首页（未登录时会被下面的守卫改道到登录页）
  { path: '/:pathMatch(.*)*', redirect: '/' },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
});

/**
 * 导航守卫。
 *
 * 只做「是否登录」这一件事 —— 权限模型是单管理员，没有更细的粒度。
 * 注意 `ready` 的判断：首次探测未完成时**放行**，由 App.vue 显示骨架。
 * 在这里等待会把整个应用卡在空白页上。
 */
router.beforeEach((to) => {
  const auth = useAuthStore();

  if (!auth.ready) return true;

  if (!auth.session && !to.meta.public) {
    return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : undefined };
  }

  // 已登录时访问登录页直接回首页，避免手输地址后看到一个无用的表单
  if (auth.session && to.meta.public) {
    return { name: 'dashboard' };
  }

  return true;
});
