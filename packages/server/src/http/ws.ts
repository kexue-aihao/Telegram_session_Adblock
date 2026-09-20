import { channel as channels, type WsClientFrame, type WsServerFrame } from '@tgs/shared';
import type { WebSocket } from '@fastify/websocket';
import { subscribe } from '../core/bus.ts';
import { logger } from '../core/logger.ts';
import { SESSION_COOKIE, verifySession } from './auth.ts';
import { getBotManager } from '../bots/manager.ts';
import type { App } from './app.ts';

/**
 * WebSocket 网关。
 *
 * 面板的实时性全部由这里承担。设计上有两个刻意的选择：
 *
 *  1. **服务端只是总线的转发器**。业务代码往 `core/bus.ts` 发事件，
 *     这里订阅并投递，两边互不认识。中继逻辑因此完全不依赖 HTTP 层。
 *  2. **按频道订阅**。会话列表页不需要知道另一个话题里每一条消息 ——
 *     全量广播在几十个活跃会话时就会把带宽吃满。
 */

interface Client {
  socket: WebSocket;
  /** 已订阅的频道；`*` 表示全局事件 */
  channels: Set<string>;
}

/** 积压超过这个字节数的连接直接丢弃事件：慢客户端不该拖垮进程内存 */
const MAX_BUFFERED_BYTES = 1 << 20; // 1 MiB

const STARTED_AT = Date.now();
const VERSION = '0.1.0';

/** 连接建立时与心跳应答都发它，前端据此判断服务是否重启过 */
function helloFrame(): string {
  return JSON.stringify({
    type: 'hello',
    seq: 0,
    ts: Date.now(),
    channel: channels.global,
    payload: {
      serverStartedAt: STARTED_AT,
      version: VERSION,
      onlineBots: getBotManager().onlineCount(),
    },
  });
}

export function registerWebsocket(app: App): void {
  const clients = new Set<Client>();

  // 总线 → 所有匹配频道的前端
  const unsubscribe = subscribe((frame: WsServerFrame) => {
    for (const client of clients) {
      // `*` 是全局频道，所有连接都收
      if (!client.channels.has(frame.channel) && !client.channels.has(channels.global)) continue;

      if (client.socket.bufferedAmount > MAX_BUFFERED_BYTES) {
        // 客户端读得太慢。丢掉这一帧比让它无限堆积更安全 ——
        // 前端发现 seq 不连续时会自己触发一次全量刷新。
        logger.debug({ buffered: client.socket.bufferedAmount }, 'WebSocket 积压过多，丢弃一帧');
        continue;
      }

      try {
        client.socket.send(JSON.stringify(frame));
      } catch (err) {
        logger.debug({ err }, 'WebSocket 发送失败');
      }
    }
  });

  app.get('/ws', { websocket: true }, (socket: WebSocket, request) => {
    void (async () => {
      // 复用与 REST 完全相同的会话校验：WS 不能成为鉴权的后门
      const token = request.cookies[SESSION_COOKIE];
      const auth = token ? await verifySession(token) : null;
      if (!auth) {
        socket.close(4401, '未登录');
        return;
      }

      const client: Client = {
        socket,
        channels: new Set([channels.global]),
      };
      clients.add(client);

      socket.send(helloFrame());

      socket.on('message', (raw: unknown) => {
        let frame: WsClientFrame;
        try {
          frame = JSON.parse(String(raw)) as WsClientFrame;
        } catch {
          return; // 坏帧静默丢弃：前端不该因为一次拼错就断开重连
        }

        if (frame.type === 'subscribe' && frame.channel) {
          client.channels.add(frame.channel);
          return;
        }
        if (frame.type === 'unsubscribe' && frame.channel) {
          // 全局频道不允许退订：它是心跳与统计的通道
          if (frame.channel !== channels.global) client.channels.delete(frame.channel);
          return;
        }
        if (frame.type === 'ping') {
          socket.send(helloFrame());
        }
      });

      socket.on('close', () => {
        clients.delete(client);
      });
      socket.on('error', () => {
        clients.delete(client);
      });
    })().catch((err: unknown) => {
      logger.warn({ err }, 'WebSocket 连接处理失败');
      try {
        socket.close(1011, '服务端错误');
      } catch {
        // 连接可能已经断了，忽略
      }
    });
  });

  app.addHook('onClose', async () => {
    unsubscribe();
    for (const client of clients) {
      try {
        client.socket.close(1001, '服务正在关闭');
      } catch {
        // 忽略
      }
    }
    clients.clear();
  });
}
