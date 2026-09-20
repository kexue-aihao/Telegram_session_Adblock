<script setup lang="ts">
import { computed } from 'vue';

const props = withDefaults(
  defineProps<{ tone?: 'neutral' | 'accent' | 'success' | 'warn' | 'danger'; pulse?: boolean }>(),
  { tone: 'neutral', pulse: false },
);

const color = computed(
  () =>
    ({
      neutral: 'bg-[var(--color-ink-subtle)]',
      accent: 'bg-[var(--color-accent)]',
      success: 'bg-[var(--color-success)]',
      warn: 'bg-[var(--color-warn)]',
      danger: 'bg-[var(--color-danger)]',
    })[props.tone],
);
</script>

<template>
  <span class="relative inline-flex size-2 shrink-0">
    <!-- 脉冲环：用独立的绝对定位元素而不是 animate-ping 直接加在圆点上，
         后者会把圆点本身也放大 -->
    <span v-if="pulse" class="pulse-ring absolute inset-0 rounded-full" :class="color" />
    <span class="relative inline-flex size-2 rounded-full" :class="color" />
  </span>
</template>
