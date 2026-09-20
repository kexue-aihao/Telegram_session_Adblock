<script setup lang="ts">
import { computed } from 'vue';

const props = withDefaults(
  defineProps<{ tone?: 'neutral' | 'accent' | 'success' | 'warn' | 'danger' | 'violet' }>(),
  { tone: 'neutral' },
);

const toneClass = computed(
  () =>
    ({
      neutral: 'bg-[var(--color-active)] text-[var(--color-ink-muted)]',
      accent: 'bg-[var(--color-accent-soft)] text-[var(--color-accent-bright)]',
      success: 'bg-[var(--color-success-soft)] text-[var(--color-success)]',
      warn: 'bg-[var(--color-warn-soft)] text-[var(--color-warn)]',
      danger: 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]',
      violet: 'bg-[color-mix(in_oklab,var(--color-violet)_14%,transparent)] text-[var(--color-violet)]',
    })[props.tone],
);
</script>

<template>
  <span
    class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-2xs font-medium whitespace-nowrap"
    :class="toneClass"
  >
    <slot />
  </span>
</template>
