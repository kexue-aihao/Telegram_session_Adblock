<script setup lang="ts">
/**
 * 开关。
 *
 * 滑块用略微过冲的缓动曲线（ease-spring），而不是 ease-out-expo ——
 * 后者在开关这种「短距离快速位移」上会显得迟疑，前者才符合物理直觉。
 */
const props = defineProps<{ modelValue: boolean; disabled?: boolean; label?: string }>();
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>();
</script>

<template>
  <button
    type="button"
    role="switch"
    :aria-checked="modelValue"
    :aria-label="label"
    :disabled="disabled"
    class="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors duration-[var(--duration-state)] ease-[var(--ease-state)] disabled:cursor-not-allowed disabled:opacity-45"
    :class="
      modelValue
        ? 'border-transparent bg-[var(--color-accent)]'
        : 'border-[var(--color-line)] bg-[var(--color-bg-3)]'
    "
    @click="emit('update:modelValue', !modelValue)"
  >
    <span
      class="inline-block size-3.5 rounded-full bg-white shadow-sm transition-transform duration-[var(--duration-state)] ease-[var(--ease-spring)]"
      :class="modelValue ? 'translate-x-[18px]' : 'translate-x-[3px]'"
    />
  </button>
</template>
