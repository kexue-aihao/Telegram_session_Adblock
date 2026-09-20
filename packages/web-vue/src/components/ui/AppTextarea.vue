<script setup lang="ts">
import { computed, useAttrs } from 'vue';

defineOptions({ inheritAttrs: false });

const props = defineProps<{ invalid?: boolean; modelValue?: string }>();
const emit = defineEmits<{ 'update:modelValue': [value: string] }>();
const attrs = useAttrs();

const classes = computed(() => [
  'w-full rounded-xl px-3 py-2 text-sm leading-relaxed',
  'bg-[var(--color-bg-2)] text-[var(--color-ink)]',
  'border transition-all duration-[var(--duration-micro)] ease-[var(--ease-state)]',
  'placeholder:text-[var(--color-ink-faint)] resize-y',
  props.invalid
    ? 'border-[var(--color-danger)]'
    : 'border-[var(--color-line)] focus:border-[var(--color-accent)] focus:shadow-[0_0_0_3px_var(--color-accent-soft)]',
  'focus:outline-none',
]);
</script>

<template>
  <textarea
    v-bind="attrs"
    :value="modelValue"
    :aria-invalid="invalid"
    :class="classes"
    @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
  />
</template>
