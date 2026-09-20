<script setup lang="ts">
import { computed } from 'vue';

const props = withDefaults(defineProps<{ height?: string; width?: string; radius?: string }>(), {
  height: '1rem',
  width: '100%',
  radius: 'var(--radius-lg)',
});

const style = computed(() => ({
  height: props.height,
  width: props.width,
  borderRadius: props.radius,
}));
</script>

<template>
  <div class="skeleton" :style="style" />
</template>
