import type { WsServerEvent, WsServerFrame, WsServerPayloads } from '@tgs/shared';

/**
 * 进程内事件总线 —— 业务代码与 WebSocket 之间的唯一解耦点。
 *
 * 为什么不让业务代码直接持有 fastify 的 ws 连接集合：
 * 那样「机器人运行时」就依赖了「HTTP 层」，而 HTTP 层的路由又依赖运行时
 * （面板要调用运行时发消息），两个模块立刻互相 import 成环。
 * 中间垫一层单向的发布/订阅，依赖方向就只剩「业务 → 总线 ← WS 转发器」。
 *
 * 单进程内存实现：本项目是单实例部署（SQLite 文件库），不引入 Redis 之类的
 * 跨进程总线。若将来要多实例，替换这一个文件即可。
 */

export type FrameListener = (frame: WsServerFrame) => void;

const listeners = new Set<FrameListener>();

/**
 * 单调递增的序号，随每个事件 +1。
 * 前端据此判断重连后是否丢过包：序号不连续就触发一次全量刷新，
 * 而不是傻等一个可能永远不会再来的增量事件。
 */
let sequence = 0;

export function publish<K extends WsServerEvent>(
  channel: string,
  type: K,
  payload: WsServerPayloads[K],
): void {
  sequence += 1;
  const frame = {
    type,
    seq: sequence,
    ts: Date.now(),
    channel,
    payload,
  } as WsServerFrame;

  for (const listener of listeners) {
    try {
      listener(frame);
    } catch {
      // 一个订阅者（比如某个已断开但未清理的 socket）出错，
      // 不能连累其余订阅者，更不能把异常抛回业务逻辑。
    }
  }
}

export function subscribe(listener: FrameListener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function currentSequence(): number {
  return sequence;
}

export function subscriberCount(): number {
  return listeners.size;
}
