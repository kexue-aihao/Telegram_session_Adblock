import { onScopeDispose, watch, type Ref } from 'vue';
import { ws, type WsFrame } from '@/lib/ws';

/**
 * 订阅 WebSocket 的某一类事件。
 *
 * 频道订阅与事件监听分开：频道决定「服务端推不推给你」，
 * 事件类型决定「推来之后你处理哪一条」。两者都需要的场景
 * （会话详情页要订阅某个话题，同时只关心 message.new）
 * 用这两个函数组合即可。
 */
export function useWsEvent<T = unknown>(
  event: string,
  handler: (payload: T, frame: WsFrame) => void,
): void {
  const off = ws.onFrame((frame) => {
    if (frame.type !== event) return;
    handler(frame.payload as T, frame);
  });
  onScopeDispose(off);
}

/** 订阅一组事件，任一到达都触发同一个回调。 */
export function useWsEvents(events: string[], handler: (frame: WsFrame) => void): void {
  const wanted = new Set(events);
  const off = ws.onFrame((frame) => {
    if (!wanted.has(frame.type)) return;
    handler(frame);
  });
  onScopeDispose(off);
}

/** 订阅一个频道，组件卸载时自动退订。 */
export function useChannel(channel: Ref<string | null> | string): void {
  let off: (() => void) | null = null;

  const attach = (value: string | null) => {
    off?.();
    off = value ? ws.subscribe(value) : null;
  };

  if (typeof channel === 'string') {
    attach(channel);
  } else {
    watch(channel, attach, { immediate: true });
  }

  onScopeDispose(() => off?.());
}
