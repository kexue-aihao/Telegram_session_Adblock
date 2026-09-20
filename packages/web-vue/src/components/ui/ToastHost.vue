<script setup lang="ts">
import { storeToRefs } from 'pinia';
import { useToastStore, type ToastTone } from '@/stores/toast';

/**
 * 提示宿主。
 *
 * 从右下角弹入，并按列表顺序自动重排 —— 新提示插入时其余会平滑让位。
 * 用 <TransitionGroup> 的 move class 实现，它是 FLIP 动画，
 * 由浏览器算首末位置差，比手写位移稳得多。
 */
const toast = useToastStore();
const { items } = storeToRefs(toast);

const toneRing: Record<ToastTone, string> = {
  success: 'border-[color-mix(in_oklab,var(--color-success)_32%,transparent)]',
  error: 'border-[color-mix(in_oklab,var(--color-danger)_38%,transparent)]',
  warn: 'border-[color-mix(in_oklab,var(--color-warn)_36%,transparent)]',
  info: 'border-[var(--color-line)]',
};

const toneIcon: Record<ToastTone, string> = {
  success: 'M3.5 8.5l3 3 6-6.5',
  error: 'M8 4.5v5m0 2.5h.01',
  warn: 'M8 3 1.8 13.5h12.4L8 3Zm0 4.5v3.5m0 2h.01',
  info: 'M8 4.5a6.5 6.5 0 1 0 0 13 6.5 6.5 0 0 0 0-13ZM8 7.5v4.5M8 11h.01',
};

const toneColor: Record<ToastTone, string> = {
  success: 'text-[var(--color-success)]',
  error: 'text-[var(--color-danger)]',
  warn: 'text-[var(--color-warn)]',
  info: 'text-[var(--color-accent-bright)]',
};
</script>

<template>
  <div
    class="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(360px,calc(100vw-2rem))] flex-col gap-2"
    role="region"
    aria-label="通知"
  >
    <TransitionGroup name="toast">
      <div
        v-for="item in items"
        :key="item.id"
        class="glass pointer-events-auto flex items-start gap-3 rounded-2xl border px-3.5 py-3 shadow-2xl"
        :class="toneRing[item.tone]"
        :role="item.tone === 'error' ? 'alert' : 'status'"
      >
        <svg
          viewBox="0 0 16 16"
          class="mt-0.5 size-4 shrink-0"
          :class="toneColor[item.tone]"
          fill="none"
          aria-hidden="true"
        >
          <path
            :d="toneIcon[item.tone]"
            stroke="currentColor"
            stroke-width="1.7"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>

        <div class="min-w-0 flex-1 space-y-0.5">
          <p class="text-sm font-medium text-[var(--color-ink)]">{{ item.title }}</p>
          <p v-if="item.description" class="text-xs leading-relaxed break-words text-[var(--color-ink-muted)]">
            {{ item.description }}
          </p>
          <button
            v-if="item.action"
            type="button"
            class="mt-1 text-xs font-medium text-[var(--color-accent-bright)] hover:underline"
            @click="item.action.onClick(); toast.dismiss(item.id)"
          >
            {{ item.action.label }}
          </button>
        </div>

        <button
          type="button"
          aria-label="关闭通知"
          class="-mt-0.5 -mr-0.5 shrink-0 rounded-md p-1 text-[var(--color-ink-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-hover)] hover:text-[var(--color-ink)]"
          @click="toast.dismiss(item.id)"
        >
          <svg viewBox="0 0 12 12" class="size-3" fill="none" aria-hidden="true">
            <path d="M3 3l6 6M9 3l-6 6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
          </svg>
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
/* 从右侧滑入并轻微缩放。x 位移 24px 足够表达「从侧边来」，
   再大就会在连续弹提示时显得闹。 */
.toast-enter-active {
  transition:
    opacity var(--duration-layout) var(--ease-expo),
    transform var(--duration-layout) var(--ease-expo);
}
.toast-leave-active {
  transition:
    opacity var(--duration-state) var(--ease-state),
    transform var(--duration-state) var(--ease-state);
  /* 离开的元素脱出文档流，否则后面的元素要等它走完才动 */
  position: absolute;
  right: 0;
  width: 100%;
}
.toast-move {
  transition: transform var(--duration-layout) var(--ease-expo);
}
.toast-enter-from {
  opacity: 0;
  transform: translateX(24px) scale(0.97);
}
.toast-leave-to {
  opacity: 0;
  transform: translateX(16px) scale(0.98);
}
</style>
