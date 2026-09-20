import { logger } from './logger.ts';

/**
 * 按 key 串行化的任务队列。
 *
 * 解决一个具体问题：`@grammyjs/runner` 是**并发**处理更新的，同一个用户
 * 连发的两条消息会同时在两个协程里跑完「建话题 → 转发」，先发出的那条
 * 完全可能后落地，话题里的顺序就是乱的。人工客服场景里顺序即语义。
 *
 * 所以：同一个 contact 的中继任务串行，不同 contact 之间仍然并发 ——
 * 全局串行会让一个慢速 API 调用拖垮所有人。
 *
 * 刻意不用 bottleneck：它面向的是「速率限制」，而这里要的是「顺序保证」。
 * API 侧的真实速率限制由 @grammyjs/transformer-throttler 负责，两者不重复。
 */
export class KeyedQueue {
  /** 每个 key 上「最后一个任务」的 promise，后续任务挂在它后面 */
  private readonly tails = new Map<string, Promise<unknown>>();

  /**
   * 把任务排到 key 的队尾。
   *
   * 返回的 promise 一定会在任务真正执行完后 settle —— 调用方 `await` 它
   * 就等于「等我的消息被处理完」，而不是「等它被排进队列」。
   */
  run<T>(key: string, task: () => Promise<T>): Promise<T> {
    const previous = this.tails.get(key) ?? Promise.resolve();

    // 前一个任务失败不能阻断后面的任务，所以 onFulfilled 与 onRejected 用同一个
    // 函数 —— 我们要的只是「等它结束」，不是「等它成功」。
    const current = previous.then(task, task);

    // 队里记录的是「已消化掉异常」的版本：尾部若长期是 rejected，
    // 又没人挂 catch，进程会收到 unhandledRejection 警告。
    const settled: Promise<void> = current.then(
      () => undefined,
      () => undefined,
    );
    this.tails.set(key, settled);

    void settled.then(() => {
      // 只有自己仍是队尾时才清理。否则说明期间又有人排了队，
      // 删掉会把后来者的 promise 一起丢掉，串行保证就断了。
      if (this.tails.get(key) === settled) this.tails.delete(key);
    });

    return current;
  }

  /** 当前有多少个 key 还有未完成任务；用于健康检查与调试 */
  get pendingKeys(): number {
    return this.tails.size;
  }

  /** 等待所有 key 上的任务排空；用于优雅关闭 */
  async drain(): Promise<void> {
    // 队列可能在做空的过程中又追加任务，循环到真的空为止
    let guard = 0;
    while (this.tails.size > 0 && guard < 1000) {
      guard += 1;
      await Promise.allSettled([...this.tails.values()]);
    }
    if (guard >= 1000) {
      logger.warn('KeyedQueue.drain 达到循环上限，仍有任务未排空');
    }
  }
}

/**
 * 创建一个「同 key 串行」的包装函数，省去到处传队列实例。
 * 返回的 `drain` 用于关闭时等待收尾。
 */
export function createKeyedRunner(): {
  run: <T>(key: string, task: () => Promise<T>) => Promise<T>;
  drain: () => Promise<void>;
} {
  const queue = new KeyedQueue();
  return {
    run: (key, task) => queue.run(key, task),
    drain: () => queue.drain(),
  };
}
