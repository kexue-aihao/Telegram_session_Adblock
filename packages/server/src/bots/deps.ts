import type { BotSettings } from '@tgs/shared';
import type { Api } from 'grammy';
import type { MediaGroupBatcher, SlidingWindowCounter } from '../core/batcher.ts';
import type { IncomingMessage } from '../core/content.ts';
import type { createKeyedRunner } from '../core/queue.ts';
import type { ContactRow } from './topics.ts';

/**
 * 处理器拿到的运行时依赖。
 *
 * 单独成文件是为了打断 import 环：`instance.ts` 要注册 handler，
 * handler 又要用运行时信息。让两边都依赖这个纯类型模块，依赖方向就成了一条直线。
 */

export interface RuntimeDeps {
  botId: number;
  botName: string;
  /** 机器人的 Telegram user id；getMe 失败时为 null */
  botTgId: number | null;
  adminGroupId: number;
  api: Api;
  /**
   * 每次向 handler 传入当时的设置，而不是把设置对象固定在 deps 里：
   * 管理员在面板上改完配置应当立刻生效，而不是等下一次重启机器人。
   */
  getSettings: () => Promise<BotSettings>;
  /** 相册缓冲；按 media_group_id 聚合 */
  batcher: MediaGroupBatcher<IncomingMessage>;
  /** 每个联系人一个滑动窗口，用于刷屏判定 */
  flood: Map<number, SlidingWindowCounter>;
  /** 联系人档案缓存，避免刷屏判定时反复查库 */
  contacts: Map<number, ContactRow>;
  /**
   * 已经就「你被禁言了」提示过的联系人 → 当时那次禁言的解禁时刻。
   *
   * 禁言期内用户继续发消息是常态，每条都回一次提示等于我们自己刷屏。
   * 记的是解禁时刻而不是布尔值：同一个人第二次被禁言时应当再提示一次，
   * 布尔标记做不到这件事。
   */
  muteNotified: Map<number, number | null>;
  /**
   * 同 key 串行执行。
   *
   * `@grammyjs/runner` 是**并发**处理更新的，同一个用户连发的两条消息
   * 会同时跑完「建话题 → 复制消息」，先发的完全可能后落地，话题里的
   * 顺序就是乱的。人工客服场景里顺序即语义，所以同会话的操作必须串行。
   * 不同会话之间仍然并发，避免一个人拖慢所有人。
   */
  queue: ReturnType<typeof createKeyedRunner>;
}
