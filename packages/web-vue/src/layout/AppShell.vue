<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import AppIcon, { type IconName } from '@/components/ui/AppIcon.vue';
import CommandPalette from './CommandPalette.vue';
import { ws, type ConnectionState } from '@/lib/ws';

/**
 * 应用外壳：常驻侧边栏 + 顶栏 + 内容区。
 *
 * ── 两处关键的实现选择 ──────────────────────────────────────
 *
 * 1. **侧边栏与顶栏用磨砂，内容卡片不用。**
 *    backdrop-filter 会让浏览器为每个表面做一次独立合成。给几十张卡片
 *    都加上模糊，滚动时会直接掉到 30fps。浮在内容之上的「框架」用磨砂，
 *    这是既有质感又不牺牲性能的平衡点。
 *
 * 2. **选中指示器用滑动的一条，而不是每项各自变色。**
 *    它让「从一个页面到另一个页面」变成一次连续的运动，
 *    而不是两次独立的颜色变化 —— 这一处的观感差异很大。
 */
const route = useRoute();
const router = useRouter();
const auth = useAuthStore();

interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  keywords: string;
}

const navItems: NavItem[] = [
  { to: '/', label: '仪表盘', icon: 'dashboard', keywords: 'dashboard 概览 首页 overview' },
  { to: '/bots', label: '机器人', icon: 'bot', keywords: 'bots telegram 令牌 token' },
  { to: '/sessions', label: '会话', icon: 'sessions', keywords: 'sessions 话题 topics 私聊' },
  { to: '/rules', label: '规则', icon: 'rules', keywords: 'rules 正则 regex 广告 ad' },
  { to: '/audit', label: '审计', icon: 'audit', keywords: 'audit 日志 命中 hits' },
  { to: '/settings', label: '设置', icon: 'settings', keywords: 'settings 配置 密码' },
];

const paletteOpen = ref(false);
const theme = ref<'dark' | 'light'>(
  document.documentElement.classList.contains('light') ? 'light' : 'dark',
);

const connection = ref<ConnectionState>('closed');
let stopStateWatch: (() => void) | null = null;

const currentTitle = computed(() => {
  const item = navItems.find(
    (n) => n.to === route.path || (n.to !== '/' && route.path.startsWith(n.to)),
  );
  return item?.label ?? '面板';
});

// ── 导航指示器的滑动位置 ────────────────────────────────────────
//
// 用一个独立元素在导航项之间滑动，而不是每个导航项各画一条。
// 位置实测自元素的 offsetTop —— 按固定行高算是更省事，但改行高
// 或插分隔线时会静默错位，而差的那一像素很难被注意到。
const navItemsEls = ref<Record<string, HTMLElement | null>>({});

const indicator = ref({ top: 0, visible: false, ready: false });

function registerNavItem(to: string, el: HTMLElement | null) {
  navItemsEls.value[to] = el;
}

function updateIndicator() {
  const active = navItems.find(
    (n) => n.to === route.path || (n.to !== '/' && route.path.startsWith(n.to)),
  );
  const el = active ? navItemsEls.value[active.to] : null;
  if (!el) {
    indicator.value = { ...indicator.value, visible: false };
    return;
  }
  // 垂直居中：元素高 36px，指示器高 16px
  indicator.value = {
    top: el.offsetTop + (el.offsetHeight - 16) / 2,
    visible: true,
    ready: indicator.value.ready,
  };
}

const connectionLabel = computed(
  () =>
    ({
      open: '实时连接正常',
      connecting: '正在连接…',
      closed: '连接已断开，正在重连',
    })[connection.value],
);

const connectionTone = computed(
  () => ({ open: 'success', connecting: 'warn', closed: 'danger' })[connection.value] as
    | 'success'
    | 'warn'
    | 'danger',
);

let cleanupKeydown: (() => void) | null = null;

onMounted(async () => {
  stopStateWatch = ws.onStateChange((state) => {
    connection.value = state;
  });

  const onKeydown = (event: KeyboardEvent) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      paletteOpen.value = !paletteOpen.value;
    }
  };
  window.addEventListener('keydown', onKeydown);
  cleanupKeydown = () => window.removeEventListener('keydown', onKeydown);

  // 窗口尺寸变化会改导航项的位置（虽然高度固定，但缩放会改 offsetTop）
  window.addEventListener('resize', updateIndicator);

  // 首帧先落位（不动画），下一帧才允许过渡 ——
  // 否则首次进入页面时指示器会从顶部「飞」到当前项，像个 bug。
  await nextTick();
  updateIndicator();
  requestAnimationFrame(() => {
    indicator.value = { ...indicator.value, ready: true };
  });
});

watch(
  () => route.path,
  () => void nextTick().then(updateIndicator),
);

onBeforeUnmount(() => {
  stopStateWatch?.();
  cleanupKeydown?.();
  window.removeEventListener('resize', updateIndicator);
});

function toggleTheme() {
  const next = theme.value === 'dark' ? 'light' : 'dark';
  theme.value = next;
  document.documentElement.classList.toggle('dark', next === 'dark');
  document.documentElement.classList.toggle('light', next === 'light');
  try {
    localStorage.setItem('tgs.theme', next);
  } catch {
    // 隐私模式下 localStorage 可能不可用，主题仅在本次会话生效即可
  }
}

function onPaletteSelect(to: string) {
  paletteOpen.value = false;
  void router.push(to);
}
</script>

<template>
  <div class="flex h-full">
    <!-- ── 侧边栏（磨砂）────────────────────────────────────── -->
    <aside
      class="glass relative z-20 flex w-[220px] shrink-0 flex-col border-r border-[var(--color-line-faint)]"
    >
      <!-- 品牌区 -->
      <div class="flex h-14 shrink-0 items-center gap-2.5 px-4">
        <div
          class="brand-gradient flex size-7 items-center justify-center rounded-lg shadow-[inset_0_1px_0_rgb(255_255_255/0.28)]"
        >
          <svg viewBox="0 0 24 24" class="size-4 text-white" fill="none" aria-hidden="true">
            <path
              d="M20.5 12.5a7.5 7.5 0 0 1-10.9 6.7L4 20.5l1.4-5.3A7.5 7.5 0 1 1 20.5 12.5Z"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linejoin="round"
            />
          </svg>
        </div>
        <div class="min-w-0">
          <p class="truncate text-sm font-semibold tracking-tight">会话中继</p>
          <p class="truncate text-2xs text-[var(--color-ink-subtle)]">Telegram Adblock</p>
        </div>
      </div>

      <!-- 导航 -->
      <nav class="relative flex-1 space-y-0.5 px-2.5 py-2">
        <!--
          选中指示器是**一个**独立元素，靠 transform 在导航项之间滑动，
          而不是每个导航项各渲染一条。

          差别不只是省了几个节点：「从一个页面到另一个页面」因此变成
          一次连续的运动，而不是两次独立的出现与消失。

          位置由实测 offsetTop 得出，不按固定行高算 —— 后者在改行高
          或插分隔线时会静默错位，而错位的那一像素很难被注意到。
        -->
        <span
          v-show="indicator.visible"
          class="pointer-events-none absolute left-0 top-0 h-4 w-0.5 rounded-full bg-[var(--color-accent)]"
          :style="{
            transform: `translateY(${indicator.top}px)`,
            transition: indicator.ready
              ? 'transform var(--duration-layout) var(--ease-expo)'
              : 'none',
          }"
          aria-hidden="true"
        />

        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          v-slot="{ isActive, navigate }"
          :to="item.to"
          custom
        >
          <a
            :ref="(el) => registerNavItem(item.to, el as HTMLElement | null)"
            href="#"
            class="relative flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-sm transition-colors duration-[var(--duration-micro)] ease-[var(--ease-state)]"
            :class="
              isActive
                ? 'bg-[var(--color-active)] font-medium text-[var(--color-ink)]'
                : 'text-[var(--color-ink-muted)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]'
            "
            @click="navigate"
          >
            <span :class="isActive ? 'text-[var(--color-accent-bright)]' : undefined">
              <AppIcon :name="item.icon" :size="16" />
            </span>
            {{ item.label }}
          </a>
        </RouterLink>
      </nav>

      <!-- 底部 -->
      <div class="border-t border-[var(--color-line-faint)] p-2.5">
        <button
          type="button"
          class="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-xs text-[var(--color-ink-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink-muted)]"
          @click="paletteOpen = true"
        >
          <AppIcon name="command" :size="14" />
          命令面板
          <kbd
            class="ml-auto rounded border border-[var(--color-line-faint)] px-1 font-mono text-2xs"
          >
            ⌘K
          </kbd>
        </button>
      </div>
    </aside>

    <!-- ── 右侧主体 ────────────────────────────────────────── -->
    <div class="flex min-w-0 flex-1 flex-col">
      <!-- 顶栏（磨砂）：内容滚动到它下面时能透出一点，边界因此显得柔和 -->
      <header
        class="glass sticky top-0 z-30 flex h-14 shrink-0 items-center justify-between gap-4 border-b border-[var(--color-line-faint)] px-5"
      >
        <h1 class="truncate text-base font-semibold tracking-tight">{{ currentTitle }}</h1>

        <div class="flex shrink-0 items-center gap-1.5">
          <button
            type="button"
            class="flex h-8 items-center gap-2 rounded-lg border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] pr-2 pl-2.5 text-xs text-[var(--color-ink-subtle)] transition-colors duration-[var(--duration-micro)] hover:border-[var(--color-line)] hover:text-[var(--color-ink-muted)]"
            @click="paletteOpen = true"
          >
            <AppIcon name="search" :size="14" />
            <span class="hidden sm:inline">搜索</span>
            <kbd
              class="hidden rounded border border-[var(--color-line-faint)] px-1 font-mono text-2xs sm:inline"
            >
              ⌘K
            </kbd>
          </button>

          <!-- 连接状态 -->
          <span
            class="flex items-center gap-1.5 rounded-full border border-[var(--color-line-faint)] px-2 py-1 text-2xs text-[var(--color-ink-muted)]"
            :title="connectionLabel"
          >
            <span class="relative inline-flex size-2 shrink-0">
              <span
                v-if="connection !== 'open'"
                class="pulse-ring absolute inset-0 rounded-full"
                :class="
                  connectionTone === 'danger'
                    ? 'bg-[var(--color-danger)]'
                    : 'bg-[var(--color-warn)]'
                "
              />
              <span
                class="relative inline-flex size-2 rounded-full"
                :class="{
                  'bg-[var(--color-success)]': connectionTone === 'success',
                  'bg-[var(--color-warn)]': connectionTone === 'warn',
                  'bg-[var(--color-danger)]': connectionTone === 'danger',
                }"
              />
            </span>
            <span class="hidden md:inline">{{ connectionLabel }}</span>
          </span>

          <button
            type="button"
            class="rounded-lg p-2 text-[var(--color-ink-muted)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]"
            aria-label="切换主题"
            @click="toggleTheme"
          >
            <AppIcon :name="theme === 'dark' ? 'sun' : 'moon'" :size="16" />
          </button>

          <div class="mx-1 h-4 w-px bg-[var(--color-line-faint)]" />

          <span class="hidden text-xs text-[var(--color-ink-muted)] sm:inline">
            {{ auth.session?.username }}
          </span>
          <button
            type="button"
            class="rounded-lg p-2 text-[var(--color-ink-muted)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]"
            aria-label="退出登录"
            @click="auth.logout()"
          >
            <AppIcon name="logout" :size="16" />
          </button>
        </div>
      </header>

      <!-- 内容区。滚动发生在这里，而不是 body ——
           这样顶栏与侧边栏能真正「吸」在上面。 -->
      <main class="min-h-0 flex-1 overflow-y-auto">
        <RouterView v-slot="{ Component, route: childRoute }">
          <Transition name="page" mode="out-in">
            <component :is="Component" :key="childRoute.path" />
          </Transition>
        </RouterView>
      </main>
    </div>

    <CommandPalette
      :open="paletteOpen"
      :items="navItems"
      @close="paletteOpen = false"
      @select="onPaletteSelect"
    />
  </div>
</template>
