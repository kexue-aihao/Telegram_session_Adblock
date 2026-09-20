/**
 * 动画与视图流转的底层工具。
 *
 * 这里刻意**不引入动画库**：
 *
 *  - Vue 内置的 <Transition> / <TransitionGroup> 已经覆盖了入场、离场与
 *    列表 FLIP 位移，它们直接操作 DOM class，没有虚拟 DOM 层的额外开销；
 *  - 共享元素的「变形」交给浏览器原生的 **View Transitions API** ——
 *    它由合成器执行，比 JS 逐帧改 transform 平滑得多，而且在
 *    不支持的浏览器上会优雅降级（回调照常执行，只是没有过渡）。
 *
 * 引入 motion 这类库反而会把上面两件事都做差：它们用 JS 驱动动画，
 * 在主线程忙的时候会掉帧，而面板的主线程正要处理实时消息流。
 */

/** 与 theme.css 里的 --duration-* 保持一致 */
export const DURATION = {
  micro: 120,
  state: 200,
  layout: 320,
  page: 480,
} as const;

/** View Transitions 是否可用 */
export function supportsViewTransition(): boolean {
  return typeof document !== 'undefined' && 'startViewTransition' in document;
}

/**
 * 用 View Transitions 包住一次 DOM 更新。
 *
 * `update` 里做状态修改（Vue 是异步渲染的，所以必须 await nextTick）。
 * 不支持的浏览器上直接执行 update，不做任何过渡 —— 那是可接受的降级，
 * 而不是错误。
 */
export async function withViewTransition(
  update: () => void | Promise<void>,
  options: { skip?: boolean } = {},
): Promise<void> {
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (options.skip || reduced || !supportsViewTransition()) {
    await update();
    return;
  }

  const transition = (
    document as Document & {
      startViewTransition: (cb: () => void | Promise<void>) => { finished: Promise<void> };
    }
  ).startViewTransition(async () => {
    await update();
  });

  try {
    await transition.finished;
  } catch {
    // 过渡被中断（例如用户快速连点）不是错误
  }
}

/**
 * 给一组元素分配唯一的 view-transition-name。
 *
 * `view-transition-name` 必须是全局唯一的，否则浏览器会整段放弃过渡。
 * 列表里两个元素同时挂上 `card` 就会导致这种失败，而且现象是
 * 「动画莫名其妙不生效」，极难排查。所以统一走这个函数生成。
 */
export function viewTransitionName(prefix: string, id: string | number): string {
  return `${prefix}-${id}`;
}

/**
 * 让元素在「进入/离开」时短暂地从布局中「脱出」，避免共享元素过渡
 * 被父容器的 overflow 裁剪。
 */
export function withTransitionSuppression<T extends (...args: never[]) => unknown>(fn: T): T {
  return ((...args: never[]) => {
    document.documentElement.classList.add('vt-suppress');
    try {
      return fn(...args);
    } finally {
      // 下一帧再移除：过渡的截图发生在同一帧内
      requestAnimationFrame(() => {
        document.documentElement.classList.remove('vt-suppress');
      });
    }
  }) as T;
}

/**
 * 数字弹簧补间。
 *
 * 用 requestAnimationFrame 手工积分，而不是 CSS transition：
 * 数字在快速变化时（比如实时统计）需要加速度与惯性，
 * 看起来才像「仪表」而不是「幻灯片」。
 *
 * 返回一个 stop 函数，组件卸载时必须调用。
 */
export function springNumber(
  from: number,
  to: number,
  onUpdate: (value: number) => void,
  options: { stiffness?: number; damping?: number; onDone?: () => void } = {},
): () => void {
  const stiffness = options.stiffness ?? 120;
  const damping = options.damping ?? 20;

  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    onUpdate(to);
    options.onDone?.();
    return () => undefined;
  }

  let value = from;
  let velocity = 0;
  let raf = 0;
  let last = performance.now();

  const tick = (now: number) => {
    // 按 16ms 归一化步长：不同刷新率下手感一致
    const dt = Math.min((now - last) / 1000, 0.064);
    last = now;

    const force = (to - value) * stiffness;
    velocity = (velocity + force * dt) * Math.exp(-damping * dt);
    value += velocity * dt;

    // 足够接近且速度足够小就收尾，避免永远在抖
    if (Math.abs(to - value) < 0.01 && Math.abs(velocity) < 0.01) {
      onUpdate(to);
      options.onDone?.();
      return;
    }

    onUpdate(value);
    raf = requestAnimationFrame(tick);
  };

  raf = requestAnimationFrame(tick);
  return () => cancelAnimationFrame(raf);
}

/** 当前是否应当减少动效 */
export function prefersReducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}
