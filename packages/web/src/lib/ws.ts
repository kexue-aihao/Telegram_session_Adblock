import type { WsClientFrame, WsServerEvent, WsServerFrame, WsServerPayloads } from '@tgs/shared';

/**
 * WebSocket 客户端。
 *
 * 三个必须自己实现的点，浏览器不会替你做：
 *  1. **断线重连与退避**。面板经常被挂在后台标签页里，网络切换、
 *     服务重启都会掉线；不重连的界面会在几分钟后悄悄变成「死」的，
 *     而用户完全看不出来。
 *  2. **丢包检测**。服务端的 `seq` 是单调递增的，一旦不连续就说明中间
 *     漏了帧。此时正确的动作是触发一次全量刷新，而不是傻等一个
 *     永远不会再来的增量事件。
 *  3. **频道订阅的恢复**。重连后服务端只记得你订阅了全局频道，
 *     之前订阅的话题必须重新发一遍。
 */

type AnyFrame = { [E in WsServerEvent]: WsServerFrame<E> }[WsServerEvent];

type Listener = (frame: AnyFrame) => void;

export type ConnectionState = 'connecting' | 'open' | 'closed';

export class WsClient {
  private socket: WebSocket | null = null;
  private readonly listeners = new Set<Listener>();
  private readonly channels = new Set<string>(['*']);
  private stateListeners = new Set<(state: ConnectionState) => void>();

  /** 重连退避：从 1s 开始翻倍，封顶 30s —— 面板长时间挂着时不该持续轰炸服务端 */
  private retryDelayMs = 1_000;
  private retryTimer: number | null = null;
  private lastSeq = 0;
  private manualClose = false;
  private state: ConnectionState = 'closed';

  /** 丢包时触发；由上层决定怎么重新拉数据 */
  onDesync: (() => void) | null = null;

  connect(): void {
    if (this.socket && this.socket.readyState <= WebSocket.OPEN) return;
    this.manualClose = false;
    this.setState('connecting');

    const url = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`;
    const socket = new WebSocket(url);
    this.socket = socket;

    socket.onopen = () => {
      this.setState('open');
      this.retryDelayMs = 1_000;
      // 重连后补发订阅：服务端不记得上一次连接订阅了哪些频道
      for (const channel of this.channels) {
        if (channel === '*') continue;
        this.sendFrame({ type: 'subscribe', channel });
      }
    };

    socket.onmessage = (event) => {
      let frame: AnyFrame;
      try {
        frame = JSON.parse(String(event.data)) as AnyFrame;
      } catch {
        return;
      }
      this.handleFrame(frame);
    };

    socket.onclose = () => {
      this.socket = null;
      this.setState('closed');
      if (!this.manualClose) this.scheduleReconnect();
    };

    socket.onerror = () => {
      // onerror 之后一定会跟一个 onclose，重连逻辑统一放在那边，
      // 这里只负责不让错误冒泡到 window
      socket.close();
    };
  }

  private handleFrame(frame: AnyFrame): void {
    // hello 的 seq 恒为 0（它不是事件而是握手），不参与连续性判断
    if (frame.type !== 'hello') {
      if (this.lastSeq !== 0 && frame.seq !== this.lastSeq + 1) {
        // 漏帧了。不试图拼凑，直接让上层重新拉一次全量数据 ——
        // 补一帧比重新拉更省，但漏掉的那几帧永远补不回来。
        this.onDesync?.();
      }
      this.lastSeq = frame.seq;
    }

    if (frame.type === 'hello') {
      // 服务重启后 seq 会从头开始，此时本地的 seq 基线也应当清零
      const payload = frame.payload as WsServerPayloads['hello'];
      if (payload.serverStartedAt > this.serverStartedAt) {
        this.serverStartedAt = payload.serverStartedAt;
        this.lastSeq = 0;
      }
    }

    for (const listener of this.listeners) {
      try {
        listener(frame);
      } catch {
        // 一个订阅者出错不该连累其他订阅者
      }
    }
  }

  private serverStartedAt = 0;

  private scheduleReconnect(): void {
    if (this.retryTimer !== null) return;
    this.retryTimer = window.setTimeout(() => {
      this.retryTimer = null;
      this.connect();
    }, this.retryDelayMs);
    this.retryDelayMs = Math.min(this.retryDelayMs * 2, 30_000);
  }

  private setState(state: ConnectionState): void {
    if (this.state === state) return;
    this.state = state;
    for (const listener of this.stateListeners) listener(state);
  }

  private sendFrame(frame: WsClientFrame): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return;
    this.socket.send(JSON.stringify(frame));
  }

  /** 订阅一个频道；已订阅时是幂等的 */
  subscribe(channel: string): () => void {
    this.channels.add(channel);
    this.sendFrame({ type: 'subscribe', channel });
    return () => this.unsubscribe(channel);
  }

  unsubscribe(channel: string): void {
    if (channel === '*') return;
    this.channels.delete(channel);
    this.sendFrame({ type: 'unsubscribe', channel });
  }

  /** 监听所有帧；返回取消订阅函数，直接用作 useEffect 的清理函数 */
  onFrame(listener: Listener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  onStateChange(listener: (state: ConnectionState) => void): () => void {
    this.stateListeners.add(listener);
    listener(this.state);
    return () => this.stateListeners.delete(listener);
  }

  close(): void {
    this.manualClose = true;
    if (this.retryTimer !== null) {
      window.clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    this.socket?.close();
    this.socket = null;
    this.setState('closed');
  }
}

/** 全局单例：整站只维持一条 WebSocket —— 每条连接在服务端都是一个订阅者 */
export const ws = new WsClient();
