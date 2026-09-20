<script setup lang="ts">
import { useAttrs } from 'vue';

/**
 * 下拉选择。
 *
 * 箭头用背景图而不是伪元素：原生 select 不允许伪元素。
 * appearance: none 是必须的 —— 否则各平台的默认箭头长得完全不一样，
 * 面板在 Windows 与 macOS 上会是两种观感。
 */
defineOptions({ inheritAttrs: false });
defineProps<{ modelValue?: string | number }>();
const emit = defineEmits<{ 'update:modelValue': [value: string] }>();
const attrs = useAttrs();

const arrow =
  "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12'%3E%3Cpath d='M3 4.5L6 7.5L9 4.5' stroke='%239AA1B2' stroke-width='1.5' fill='none' stroke-linecap='round'/%3E%3C/svg%3E\")";
</script>

<template>
  <select
    v-bind="attrs"
    :value="modelValue"
    class="h-9 w-full cursor-pointer appearance-none rounded-xl border border-[var(--color-line)] bg-[var(--color-bg-2)] pr-8 pl-3 text-sm text-[var(--color-ink)] transition-all duration-[var(--duration-micro)] focus:border-[var(--color-accent)] focus:shadow-[0_0_0_3px_var(--color-accent-soft)] focus:outline-none"
    :style="{ backgroundImage: arrow, backgroundRepeat: 'no-repeat', backgroundPosition: 'right 10px center' }"
    @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
  >
    <slot />
  </select>
</template>
