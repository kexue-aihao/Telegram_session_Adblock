<script setup lang="ts">
import AppIcon, { type IconName } from './AppIcon.vue';

/**
 * 空态。
 *
 * 刻意不用插画：一张通用插画会让所有空态长得一样，用户无法一眼分辨
 * 「还没有数据」和「筛选没结果」。用图标 + 一句人话 + 一个动作更有效。
 */
withDefaults(defineProps<{ icon?: IconName; title: string; description?: string; compact?: boolean }>(), {
  icon: 'info',
  compact: false,
});
</script>

<template>
  <div
    class="flex flex-col items-center justify-center gap-3 text-center"
    :class="compact ? 'py-8' : 'py-16'"
  >
    <div
      class="flex size-11 items-center justify-center rounded-2xl border border-[var(--color-line-faint)] bg-[var(--color-bg-2)] text-[var(--color-ink-subtle)]"
    >
      <AppIcon :name="icon" :size="20" />
    </div>
    <div class="space-y-1">
      <p class="text-sm font-medium text-[var(--color-ink)]">{{ title }}</p>
      <p v-if="description" class="max-w-sm text-xs leading-relaxed text-[var(--color-ink-muted)]">
        {{ description }}
      </p>
    </div>
    <slot />
  </div>
</template>
