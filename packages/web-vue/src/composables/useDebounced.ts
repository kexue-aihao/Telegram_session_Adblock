import { ref, watch, type Ref } from 'vue';

/**
 * 防抖。
 *
 * 用于搜索框 —— 每敲一个字就发一次请求既浪费又会让列表闪烁。
 *
 * 实现刻意用最直白的「watch 源 → 延迟写目标」，而不是 customRef：
 * customRef 需要手工管理 track/trigger，一旦写错就会出现「值变了但
 * 界面不更新」这种极难排查的问题，而这里要的只是一次延迟赋值。
 */
export function useDebouncedRef<T>(source: Ref<T>, delayMs = 300): Ref<T> {
  const debounced = ref(source.value) as Ref<T>;
  let timer: number | null = null;

  watch(source, (value) => {
    if (timer !== null) window.clearTimeout(timer);
    timer = window.setTimeout(() => {
      debounced.value = value;
      timer = null;
    }, delayMs);
  });

  return debounced;
}

/**
 * 自带原始值与防抖值的组合。
 * 输入框绑 `raw`（保证输入即时可见），查询用 `debounced`。
 */
export function useDebounced<T>(initial: T, delayMs = 300) {
  const raw = ref<T>(initial) as Ref<T>;
  const debounced = useDebouncedRef<T>(raw, delayMs);
  return { raw, debounced };
}
