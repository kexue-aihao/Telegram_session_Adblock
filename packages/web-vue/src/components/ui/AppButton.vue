<script setup lang="ts">
/**
 * 按钮。
 *
 * 全部透传原生属性 —— `aria-*`、`disabled`、`type` 这些不需要额外包装
 * 就能用。面板是运维天天要用的工具，键盘可达性不是可选项。
 */
import { computed } from 'vue';
import AppIcon from './AppIcon.vue';

const props = withDefaults(
  defineProps<{
    variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'success';
    size?: 'sm' | 'md' | 'lg' | 'icon';
    loading?: boolean;
    disabled?: boolean;
    type?: 'button' | 'submit';
    block?: boolean;
  }>(),
  { variant: 'secondary', size: 'md', loading: false, disabled: false, type: 'button', block: false },
);

const variantClass = computed(() => {
  switch (props.variant) {
    case 'primary':
      // 主按钮是整个界面里唯一「发光」的元素，靠渐变 + 顶部内高光实现。
      // 不用外发光：暗色下的外发光会糊成一团脏色。
      return [
        'brand-gradient text-white',
        'shadow-[inset_0_1px_0_rgb(255_255_255/0.28),0_1px_2px_rgb(0_0_0/0.4)]',
        'hover:brightness-110 active:brightness-95',
      ].join(' ');
    case 'secondary':
      return [
        'bg-[var(--color-bg-2)] text-[var(--color-ink)]',
        'border border-[var(--color-line)]',
        'shadow-[inset_0_1px_0_var(--color-highlight)]',
        'hover:bg-[var(--color-bg-3)] hover:border-[var(--color-line-strong)]',
      ].join(' ');
    case 'ghost':
      return 'text-[var(--color-ink-muted)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]';
    case 'danger':
      return 'bg-[var(--color-danger)] text-white hover:brightness-110 active:brightness-95';
    case 'success':
      return 'bg-[var(--color-success)] text-[#062015] hover:brightness-110';
  }
});

const sizeClass = computed(() => {
  switch (props.size) {
    case 'sm':
      return 'h-7 px-2.5 text-xs rounded-lg gap-1.5';
    case 'lg':
      return 'h-11 px-5 text-base rounded-xl gap-2';
    case 'icon':
      return 'h-8 w-8 rounded-lg justify-center';
    default:
      return 'h-9 px-3.5 text-sm rounded-xl gap-2';
  }
});
</script>

<template>
  <button
    :type="type"
    :disabled="disabled || loading"
    class="inline-flex select-none items-center font-medium transition-all duration-[var(--duration-micro)] ease-[var(--ease-state)] disabled:cursor-not-allowed disabled:opacity-45 active:scale-[0.98]"
    :class="[variantClass, sizeClass, block && 'w-full justify-center']"
  >
    <!-- 加载态用旋转图标而不是替换文字：文字一变宽度就跳，很廉价 -->
    <svg
      v-if="loading"
      class="size-3.5 animate-spin"
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" stroke-opacity="0.2" stroke-width="3" />
      <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
    </svg>
    <slot />
  </button>
</template>
