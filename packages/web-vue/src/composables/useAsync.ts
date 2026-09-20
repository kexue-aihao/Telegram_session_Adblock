import { onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue';
import { ApiError } from '@/lib/api';

/**
 * 数据获取。
 *
 * 刻意不引入 TanStack Query 这类库：它带来的缓存/失效/重试策略对这个
 * 规模的面板是过度设计，而实时数据本来就要靠 WebSocket 主动推、
 * 推不动再拉，与「查询缓存」的心智模型并不一致。
 *
 * 一个必须做对的细节：**组件卸载或依赖变化时丢弃在途结果**。
 * 慢响应回来时覆盖掉新数据，是这类面板最常见的一类「数据闪回」bug。
 */
export interface AsyncState<T> {
  data: Ref<T | null>;
  loading: Ref<boolean>;
  error: Ref<string | null>;
  reload: () => Promise<void>;
}

export function useAsync<T>(
  loader: () => Promise<T>,
  deps: () => unknown[] = () => [],
  options: { immediate?: boolean } = { immediate: true },
): AsyncState<T> {
  const data = shallowRef<T | null>(null);
  const loading = ref(false);
  const error = ref<string | null>(null);

  let generation = 0;
  let disposed = false;

  async function run(): Promise<void> {
    const current = ++generation;
    loading.value = true;

    try {
      const result = await loader();
      // 过期响应直接丢弃，不管它成功与否
      if (disposed || current !== generation) return;
      data.value = result;
      error.value = null;
    } catch (err) {
      if (disposed || current !== generation) return;
      error.value = err instanceof ApiError ? err.message : '请求失败';
    } finally {
      if (!disposed && current === generation) loading.value = false;
    }
  }

  if (options.immediate !== false) {
    void run();
  }

  watch(deps, () => void run(), { deep: false });

  onScopeDispose(() => {
    disposed = true;
  });

  return { data, loading, error, reload: run };
}
