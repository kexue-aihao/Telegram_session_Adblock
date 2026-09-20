import { defineStore } from 'pinia';
import { ref } from 'vue';

/**
 * 轻量提示。
 *
 * 从右下角弹入、按 layout 自动重排 —— 新提示插入时其余会平滑让位，
 * 而不是生硬地跳一下。
 *
 * 有一条刻意的克制：**成功提示默认 2.6 秒就消失，且不阻止点击**。
 * 很多面板把提示做成必须手动关闭的浮层，用久了非常烦人。
 */

export type ToastTone = 'success' | 'error' | 'info' | 'warn';

export interface Toast {
  id: number;
  tone: ToastTone;
  title: string;
  description?: string;
  action?: { label: string; onClick: () => void };
}

export const useToastStore = defineStore('toast', () => {
  const items = ref<Toast[]>([]);
  let counter = 0;

  function dismiss(id: number): void {
    items.value = items.value.filter((t) => t.id !== id);
  }

  function push(toast: Omit<Toast, 'id'>, durationMs?: number): number {
    const id = ++counter;
    // 最多同时显示 4 条：再多会遮住界面而且没人看
    items.value = [...items.value, { ...toast, id }].slice(-4);

    const duration = durationMs ?? (toast.tone === 'error' ? 6000 : 2600);
    if (duration > 0) {
      window.setTimeout(() => dismiss(id), duration);
    }
    return id;
  }

  const success = (title: string, description?: string) =>
    push({ tone: 'success', title, description });
  const error = (title: string, description?: string) => push({ tone: 'error', title, description });
  const info = (title: string, description?: string) => push({ tone: 'info', title, description });
  const warn = (title: string, description?: string) => push({ tone: 'warn', title, description });

  return { items, push, dismiss, success, error, info, warn };
});
