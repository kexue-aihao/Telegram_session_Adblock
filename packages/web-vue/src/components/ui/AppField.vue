<script setup lang="ts">
/**
 * 带标签与说明的字段容器。
 * 错误信息占据固定位置，避免出错时整页跳动。
 */
defineProps<{ label: string; hint?: string; error?: string | null }>();
</script>

<template>
  <label class="block space-y-1.5">
    <span class="flex items-baseline justify-between gap-3">
      <span class="text-xs font-medium text-[var(--color-ink-muted)]">{{ label }}</span>
      <span v-if="hint" class="text-2xs text-[var(--color-ink-subtle)]">{{ hint }}</span>
    </span>
    <slot />
    <span v-if="error" class="block text-2xs text-[var(--color-danger)]">{{ error }}</span>
  </label>
</template>
