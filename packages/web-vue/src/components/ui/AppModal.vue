<script setup lang="ts">
import { onBeforeUnmount, watch } from 'vue';

/**
 * 模态框。
 *
 * 三件必须做对的事：打开时锁滚动、Esc 关闭、关闭后把焦点还给触发元素。
 * 漏掉最后一件是常见的可访问性 bug —— 模态关了但焦点丢在 body 上，
 * 键盘用户之后就完全无法操作页面。
 */
const props = defineProps<{ open: boolean; title: string; description?: string; width?: string }>();
const emit = defineEmits<{ close: [] }>();

let previouslyFocused: HTMLElement | null = null;
let previousOverflow = '';

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.stopPropagation();
    emit('close');
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      previouslyFocused = document.activeElement as HTMLElement | null;
      document.addEventListener('keydown', onKeydown);
      previousOverflow = document.body.style.overflow;
      document.body.style.overflow = 'hidden';
    } else {
      document.removeEventListener('keydown', onKeydown);
      document.body.style.overflow = previousOverflow;
      previouslyFocused?.focus?.();
    }
  },
);

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown);
  document.body.style.overflow = previousOverflow;
});
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4">
        <div class="absolute inset-0 bg-black/55 backdrop-blur-[2px]" @click="emit('close')" />
        <div
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          class="glass-deep relative w-full rounded-3xl"
          :style="{ maxWidth: width ?? '32rem' }"
        >
          <header
            class="flex items-start justify-between gap-4 border-b border-[var(--color-line-faint)] px-5 py-4"
          >
            <div class="space-y-0.5">
              <h3 class="text-base font-semibold tracking-tight">{{ title }}</h3>
              <p v-if="description" class="text-xs text-[var(--color-ink-muted)]">{{ description }}</p>
            </div>
            <button
              type="button"
              aria-label="关闭"
              class="-mt-1 -mr-1 rounded-lg p-1.5 text-[var(--color-ink-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]"
              @click="emit('close')"
            >
              <svg viewBox="0 0 16 16" class="size-4" fill="none" aria-hidden="true">
                <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
              </svg>
            </button>
          </header>

          <div class="max-h-[65vh] overflow-y-auto px-5 py-4">
            <slot />
          </div>

          <footer
            v-if="$slots.footer"
            class="flex items-center justify-end gap-2 border-t border-[var(--color-line-faint)] px-5 py-3.5"
          >
            <slot name="footer" />
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* 模态的入场：从 scale .97 + 上浮 8px。距离刻意很小 ——
   大位移的弹窗在频繁开关时很晕。 */
.modal-enter-active,
.modal-leave-active {
  transition: opacity var(--duration-state) var(--ease-state);
}
.modal-enter-active .glass-deep,
.modal-leave-active .glass-deep {
  transition: transform var(--duration-layout) var(--ease-expo);
}
.modal-enter-from,
.modal-leave-to {
  opacity: 0;
}
.modal-enter-from .glass-deep {
  transform: scale(0.97) translateY(8px);
}
.modal-leave-to .glass-deep {
  transform: scale(0.98) translateY(4px);
}
</style>
