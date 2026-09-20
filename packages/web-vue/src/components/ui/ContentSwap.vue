<script setup lang="ts">
/**
 * 骨架屏 → 内容的交叉淡入。
 *
 * 数据到达时直接替换内容会「跳」一下：骨架消失与内容出现发生在同一帧，
 * 而两者的高度/密度往往不同，眼睛会捕捉到那次突变。
 *
 * 这里让两者在 200ms 内重叠淡入淡出。代价是骨架会短暂地压在新内容上，
 * 所以用 absolute 让骨架脱出文档流 —— 否则容器高度会在过渡期间
 * 等于两者之和，页面会先撑开再收缩。
 */
withDefaults(
  defineProps<{
    /** 是否仍在加载（首次加载时才显示骨架；重载时保留旧内容更稳） */
    loading: boolean;
    /** 是否已有数据。有数据时即使 loading 也显示内容，避免刷新时闪回骨架 */
    hasData: boolean;
  }>(),
  {},
);
</script>

<template>
  <div class="relative">
    <!-- 内容：有数据就显示，刷新期间保持可见 -->
    <Transition name="swap">
      <div v-if="hasData">
        <slot />
      </div>
    </Transition>

    <!-- 骨架：只在「没有数据且正在加载」时出现，且叠在内容之上 -->
    <Transition name="swap">
      <div v-if="loading && !hasData" class="absolute inset-0">
        <slot name="skeleton" />
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.swap-enter-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.swap-leave-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.swap-enter-from,
.swap-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .swap-enter-active,
  .swap-leave-active {
    transition-duration: 1ms;
  }
}
</style>
