/**
 * 媒体组缓冲。
 *
 * Telegram 把相册拆成 N 条独立更新推送，间隔几十毫秒。要还原成一张相册，
 * 必须等这一组到齐再一次性 `copyMessages` —— 逐条转发会被渲染成 N 条
 * 互不相干的消息，相册形态完全丢失。
 *
 * 用「每次 push 重置计时器」的 debounce 而不是固定窗口：网络抖动会让
 * 同一组的消息间隔忽长忽短，固定窗口总有一批会被切成两半。
 *
 * 注意这里刻意不用构造函数参数属性（`constructor(private readonly x: T)`）：
 * 那会生成运行时代码，而本项目开了 `erasableSyntaxOnly` —— Node 直接执行
 * TypeScript 时只做类型擦除，这类语法糖不在支持范围内。
 */
export class MediaGroupBatcher<T> {
  private readonly buckets = new Map<string, { items: T[]; timer: NodeJS.Timeout }>();
  private readonly windowMs: number;
  private readonly onFlush: (key: string, items: T[]) => Promise<void>;
  private readonly onError: ((key: string, err: unknown) => void) | undefined;

  constructor(
    windowMs: number,
    onFlush: (key: string, items: T[]) => Promise<void>,
    onError?: (key: string, err: unknown) => void,
  ) {
    this.windowMs = windowMs;
    this.onFlush = onFlush;
    this.onError = onError;
  }

  push(key: string, item: T): void {
    const existing = this.buckets.get(key);
    if (existing) {
      existing.items.push(item);
      clearTimeout(existing.timer);
      existing.timer = setTimeout(() => void this.flush(key), this.windowMs);
      return;
    }

    const timer = setTimeout(() => void this.flush(key), this.windowMs);
    // 定时器不该拖住进程退出，尤其在这个进程还要优雅关闭机器人轮询的时候
    timer.unref?.();
    this.buckets.set(key, { items: [item], timer });
  }

  private async flush(key: string): Promise<void> {
    const bucket = this.buckets.get(key);
    if (!bucket) return;
    this.buckets.delete(key);
    clearTimeout(bucket.timer);

    try {
      await this.onFlush(key, bucket.items);
    } catch (err) {
      // 一组相册转发失败不能影响后续消息 —— 调用方已经拿到错误，
      // 这里只负责别让异常冒到定时器里变成 unhandledRejection。
      this.onError?.(key, err);
    }
  }

  /** 关闭时把还剩的桶全部冲掉，否则最后几张图会凭空消失 */
  async drain(): Promise<void> {
    const keys = [...this.buckets.keys()];
    await Promise.all(keys.map((key) => this.flush(key)));
  }

  get pending(): number {
    return this.buckets.size;
  }
}

/**
 * 滑动窗口计数器：判断「短时间内连发」。
 *
 * 用时间戳数组而不是每次重新过滤：这个函数在每条消息上都会跑，
 * 而窗口内最多也就几十个元素。
 */
export class SlidingWindowCounter {
  private readonly stamps: number[] = [];
  private readonly windowMs: number;
  private threshold: number;

  constructor(windowMs: number, threshold: number) {
    this.windowMs = windowMs;
    this.threshold = threshold;
  }

  /** 管理员改了阈值时立刻生效，而不是等下一个人被判定为刷屏 */
  setThreshold(threshold: number): void {
    this.threshold = threshold;
  }

  /** 记一次事件，返回窗口内是否**超过**阈值 */
  hit(now = Date.now()): boolean {
    this.stamps.push(now);
    // 数组按时间有序，从头删掉过期的即可
    while (this.stamps.length > 0 && now - (this.stamps[0] ?? 0) > this.windowMs) {
      this.stamps.shift();
    }
    return this.stamps.length > this.threshold;
  }

  get count(): number {
    return this.stamps.length;
  }

  reset(): void {
    this.stamps.length = 0;
  }
}
