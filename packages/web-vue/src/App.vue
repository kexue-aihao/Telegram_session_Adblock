<script setup lang="ts">
import { onMounted } from 'vue';
import { useAuthStore } from '@/stores/auth';
import AmbientBackdrop from '@/components/ui/AmbientBackdrop.vue';
import ToastHost from '@/components/ui/ToastHost.vue';

const auth = useAuthStore();

onMounted(async () => {
  await auth.refresh();
  auth.connectRealtime();
});
</script>

<template>
  <!-- 环境光晕在最底层：它给磨砂表面提供「可模糊的内容」，
       没有它整个主题的玻璃质感就不成立 -->
  <AmbientBackdrop />

  <div class="relative z-10 h-full">
    <!-- 首次探测会话期间显示极简加载态，而不是先渲染登录页再跳走 ——
         那一下闪烁会让用户以为「登录掉了」 -->
    <div v-if="!auth.ready" class="flex h-full items-center justify-center">
      <svg class="size-6 animate-spin text-[var(--color-ink-subtle)]" viewBox="0 0 24 24" fill="none">
        <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity="0.2" stroke-width="3" />
        <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
      </svg>
    </div>

    <RouterView v-else v-slot="{ Component, route }">
      <!-- 页面级转场。mode="out-in" 让旧页面先走完再进新的，
           避免两个页面重叠时布局互相挤压。 -->
      <Transition name="page" mode="out-in">
        <component :is="Component" :key="route.path" />
      </Transition>
    </RouterView>
  </div>

  <ToastHost />
</template>

<style>
/* 页面转场：淡入 + 上浮 8px。
   上浮距离刻意很小 —— 大位移的页面切换在频繁跳转的控制台里会让人眩晕，
   而 8px 足以让「页面换了」这件事被感知到。 */
.page-enter-active {
  transition:
    opacity var(--duration-layout) var(--ease-expo),
    transform var(--duration-layout) var(--ease-expo);
}
.page-leave-active {
  transition:
    opacity var(--duration-state) var(--ease-state),
    transform var(--duration-state) var(--ease-state);
}
.page-enter-from {
  opacity: 0;
  transform: translateY(8px);
}
.page-leave-to {
  opacity: 0;
  transform: translateY(-4px);
}

@media (prefers-reduced-motion: reduce) {
  .page-enter-from,
  .page-leave-to {
    transform: none;
  }
}
</style>
