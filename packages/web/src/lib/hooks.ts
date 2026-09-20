import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { WsServerEvent, WsServerFrame, WsServerPayloads } from '@tgs/shared';
import { ws } from './ws.ts';
import { ApiError } from './api.ts';

/**
 * 数据获取与实时事件的通用 Hook。
 *
 * 刻意不引入 TanStack Query：它带来的缓存/失效/重试策略对这个规模的面板
 * 是过度设计，而实时数据本来就要靠 WebSocket 主动推、推不动再拉，
 * 与「查询缓存」的心智模型并不一致。这里用一个 60 行的实现覆盖全部需要。
 */

export interface AsyncState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  /** 手动重新拉取 */
  reload: () => void;
  /** 本地直接替换数据（收到 WS 推送时用，避免整页重拉） */
  setData: (updater: (current: T | null) => T | null) => void;
}

/**
 * 拉取一次数据，并在依赖变化时重拉。
 *
 * `enabled` 用于「条件拉取」——例如详情面板在没选中会话时不该发请求。
 */
export function useAsync<T>(
  loader: () => Promise<T>,
  deps: unknown[],
  options: { enabled?: boolean } = {},
): AsyncState<T> {
  const enabled = options.enabled ?? true;
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(enabled);
  const [error, setError] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);

  // loader 通常是内联箭头函数，每次渲染都是新引用。
  // 用 ref 保存它，这样下面 effect 的依赖就能只写 deps 而不含 loader，
  // 否则会陷入「每次都重拉」的无限循环。
  const loaderRef = useRef(loader);
  loaderRef.current = loader;

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }

    let cancelled = false;
    setLoading(true);

    void (async () => {
      try {
        const result = await loaderRef.current();
        if (cancelled) return;
        setData(result);
        setError(null);
      } catch (err) {
        if (cancelled) return;
        setError(err instanceof ApiError ? err.message : '请求失败');
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    // 组件卸载或依赖变化时丢弃在途结果：慢响应回来时覆盖掉新数据
    // 是这类面板最常见的一类「数据闪回」bug
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, nonce, ...deps]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  const setDataWrapped = useCallback((updater: (current: T | null) => T | null) => {
    setData((current) => updater(current));
  }, []);

  return { data, loading, error, reload, setData: setDataWrapped };
}

/**
 * 订阅 WebSocket 的某一类事件。
 *
 * handler 存在 ref 里，所以调用方不需要（也不应该）用 useCallback 包裹它 ——
 * 这避免了「忘记包 → 每帧都重新订阅」这个极其常见又很难发现的性能陷阱。
 */
export function useWsEvent<K extends WsServerEvent>(
  event: K,
  handler: (payload: WsServerPayloads[K], frame: WsServerFrame<K>) => void,
): void {
  const handlerRef = useRef(handler);
  handlerRef.current = handler;

  useEffect(() => {
    return ws.onFrame((frame) => {
      if (frame.type !== event) return;
      handlerRef.current(frame.payload as WsServerPayloads[K], frame as WsServerFrame<K>);
    });
  }, [event]);
}

/** 订阅一个频道，组件卸载时自动退订 */
export function useChannel(channel: string | null): void {
  useEffect(() => {
    if (!channel) return;
    return ws.subscribe(channel);
  }, [channel]);
}

/** 订阅连接状态，用于顶栏的在线指示 */
export function useConnectionState(): 'connecting' | 'open' | 'closed' {
  const [state, setState] = useState<'connecting' | 'open' | 'closed'>('closed');
  useEffect(() => ws.onStateChange(setState), []);
  return state;
}

/**
 * 防抖值。
 * 用于搜索框 —— 每敲一个字就发一次请求既浪费又会让列表闪烁。
 */
export function useDebounced<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}

/** 监听 media query；用于侧边栏折叠等在窄屏下的自适应行为 */
export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const media = window.matchMedia(query);
    const listener = (event: MediaQueryListEvent) => setMatches(event.matches);
    media.addEventListener('change', listener);
    setMatches(media.matches);
    return () => media.removeEventListener('change', listener);
  }, [query]);
  return matches;
}

/** 上次渲染时的值；用于「数据变化时做一次副作用」这类场景 */
export function usePrevious<T>(value: T): T | undefined {
  const ref = useRef<T | undefined>(undefined);
  useEffect(() => {
    ref.current = value;
  }, [value]);
  return ref.current;
}

/**
 * 一组持续的计时器：每秒 tick 一次，用来刷新「3 分钟前」这类相对时间。
 * 只在页面可见时跑 —— 后台标签页里每秒重渲染是纯粹的浪费。
 */
export function useTicker(intervalMs = 30_000): number {
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let timer: number | null = null;

    const start = () => {
      if (timer !== null) return;
      timer = window.setInterval(() => setTick((t) => t + 1), intervalMs);
    };
    const stop = () => {
      if (timer === null) return;
      window.clearInterval(timer);
      timer = null;
    };

    const onVisibility = () => (document.hidden ? stop() : start());

    if (!document.hidden) start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [intervalMs]);

  return tick;
}

/** 稳定的对象键，用于 useEffect 依赖里放「一组 id」 */
export function useKeyList(list: number[]): string {
  return useMemo(() => list.join(','), [list]);
}
