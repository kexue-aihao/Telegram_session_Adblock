<script setup lang="ts">
import { computed, useAttrs } from 'vue';

/**
 * 文本输入。
 *
 * 焦点态用「描边变色 + 柔和外发光」，而不是浏览器默认的 outline：
 * 后者在暗色面板里是一圈刺眼的白框。外发光用的是品牌色的低透明度版本，
 * 强度刚好能看出「聚焦了」，又不会抢走内容。
 */
defineOptions({ inheritAttrs: false });

const props = defineProps<{ invalid?: boolean; modelValue?: string | number }>();
const emit = defineEmits<{ 'update:modelValue': [value: string] }>();
const attrs = useAttrs();

const classes = computed(() => [
  'w-full h-9 rounded-xl px-3 text-sm',
  'bg-[var(--color-bg-2)] text-[var(--color-ink)]',
  'border transition-all duration-[var(--duration-micro)] ease-[var(--ease-state)]',
  'placeholder:text-[var(--color-ink-faint)]',
  'disabled:cursor-not-allowed disabled:opacity-50',
  props.invalid
    ? 'border-[var(--color-danger)] focus:shadow-[0_0_0_3px_var(--color-danger-soft)]'
    : 'border-[var(--color-line)] focus:border-[var(--color-accent)] focus:shadow-[0_0_0_3px_var(--color-accent-soft)]',
  'focus:outline-none',
]);
</script>

<template>
  <input
    v-bind="attrs"
    :value="modelValue"
    :aria-invalid="invalid"
    :class="classes"
    @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
  />
</template>
