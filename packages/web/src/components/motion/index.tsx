import {
  AnimatePresence,
  motion,
  useMotionValue,
  useReducedMotion,
  useSpring,
  useTransform,
} from 'motion/react';
import { useEffect, useRef, type ReactNode } from 'react';
import { cn } from '../../lib/cn.ts';

/**
 * 动画原语。
 *
 * 全部时长与缓动都取自 `theme.css` 里的 CSS 变量（在 TS 里以常量镜像一份），
 * 并且**统一在这里定义**。散落各处的 magic number 是「廉价感」最大的来源：
 * 一个 150ms 的 hover 配一个 500ms 的展开，整体就会显得笨重。
 *
 * 所有原语都尊重 `prefers-reduced-motion` —— 降级为纯透明度过渡而不是
 * 完全不动，因为完全不动会让「操作是否生效」失去反馈。
 */

/** 与 theme.css 里的 --duration-* 保持一致 */
export const DURATION = {
  micro: 0.12,
  state: 0.2,
  layout: 0.32,
  page: 0.48,
} as const;

export const EASE_OUT_EXPO = [0.16, 1, 0.3, 1] as const;
export const EASE_STATE = [0.4, 0, 0.2, 1] as const;
/** 略微过冲，用于「落下」的动作，比如拖拽结束、新条目入场 */
export const SPRING_SOFT = { stiffness: 400, damping: 30 } as const;

// ────────────────────────────── 页面切换 ──────────────────────────────

/**
 * 路由切换动画：淡入 + 上浮 8px。
 *
 * 上浮距离刻意很小 —— 大位移的页面切换在频繁跳转的控制台里会让人眩晕，
 * 而 8px 足以让「页面换了」这件事被感知到。
 */
export function PageTransition({ children, routeKey }: { children: ReactNode; routeKey: string }) {
  const reduced = useReducedMotion();

  return (
    <AnimatePresence mode="wait" initial={false}>
      <motion.div
        key={routeKey}
        initial={reduced ? { opacity: 0 } : { opacity: 0, y: 8 }}
        animate={{ opacity: 1, y: 0 }}
        exit={reduced ? { opacity: 0 } : { opacity: 0, y: -4 }}
        transition={{
          duration: reduced ? DURATION.micro : DURATION.layout,
          ease: EASE_OUT_EXPO,
        }}
        className="h-full"
      >
        {children}
      </motion.div>
    </AnimatePresence>
  );
}

// ────────────────────────────── 列表错峰入场 ──────────────────────────────

/**
 * 列表容器。
 *
 * 错峰上限由 `StaggerItem` 的 `max` 控制（见下）：一个 500 行的审计列表
 * 如果逐行错峰入场，最后一行要等 15 秒才出现。所以超过上限之后
 * 直接全部显示，这一点必须逐项判断，容器层做不到。
 */
export function Stagger({
  children,
  className,
  delay = 0.03,
}: {
  children: ReactNode;
  className?: string;
  delay?: number;
}) {
  const reduced = useReducedMotion();

  return (
    <motion.div
      className={className}
      initial="hidden"
      animate="visible"
      variants={{
        hidden: {},
        visible: {
          transition: { staggerChildren: reduced ? 0 : delay },
        },
      }}
    >
      {children}
    </motion.div>
  );
}

export function StaggerItem({
  children,
  className,
  index = 0,
  max = 12,
}: {
  children: ReactNode;
  className?: string;
  index?: number;
  max?: number;
}) {
  const reduced = useReducedMotion();
  const skip = index >= max;

  return (
    <motion.div
      className={className}
      variants={{
        hidden: reduced || skip ? { opacity: 1, y: 0 } : { opacity: 0, y: 8 },
        visible: { opacity: 1, y: 0 },
      }}
      transition={{ duration: reduced ? DURATION.micro : DURATION.layout, ease: EASE_OUT_EXPO }}
    >
      {children}
    </motion.div>
  );
}

// ────────────────────────────── 数字滚动 ──────────────────────────────

/**
 * KPI 数字的弹簧补间。
 *
 * 用 useSpring 而不是 CSS transition：数字在快速变化时（比如实时统计）
 * 会有加速度与惯性，看起来像「仪表」而不是「幻灯片」。
 */
export function AnimatedNumber({
  value,
  format = (v) => String(Math.round(v)),
  className,
}: {
  value: number;
  format?: (value: number) => string;
  className?: string;
}) {
  const reduced = useReducedMotion();
  const motionValue = useMotionValue(value);
  const spring = useSpring(motionValue, { stiffness: 90, damping: 20, mass: 0.6 });
  const text = useTransform(spring, (v) => format(v));
  const ref = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    motionValue.set(value);
  }, [motionValue, value]);

  // 直接把 MotionValue 的结果写进 DOM，而不是每帧 setState ——
  // 后者会在数字变化时重渲染整个子树
  useEffect(() => {
    if (reduced) {
      if (ref.current) ref.current.textContent = format(value);
      return;
    }
    return text.on('change', (latest) => {
      if (ref.current) ref.current.textContent = latest;
    });
  }, [text, format, value, reduced]);

  return (
    <span ref={ref} className={cn('tabular', className)}>
      {format(value)}
    </span>
  );
}

// ────────────────────────────── 迷你折线 ──────────────────────────────

export interface SparklinePoint {
  x: number;
  y: number;
}

/**
 * 迷你折线。
 *
 * 用 `pathLength` 动画而不是逐点补间：前者只需要 V8 插值一条属性，
 * 后者要重算整条 path 的 d，在 90 个点上会明显掉帧。
 */
export function Sparkline({
  points,
  width = 120,
  height = 32,
  tone = 'brand',
  className,
}: {
  points: SparklinePoint[];
  width?: number;
  height?: number;
  tone?: 'brand' | 'success' | 'danger';
  className?: string;
}) {
  const reduced = useReducedMotion();

  const colors: Record<string, string> = {
    brand: 'var(--color-brand)',
    success: 'var(--color-success)',
    danger: 'var(--color-danger)',
  };

  if (points.length < 2) {
    return <div className={cn('h-8 w-full', className)} />;
  }

  const maxY = Math.max(...points.map((p) => p.y), 1);
  const step = width / (points.length - 1);
  const path = points
    .map((point, index) => {
      const x = index * step;
      // 上下各留 2px，避免峰值贴边被裁掉
      const y = height - 2 - (point.y / maxY) * (height - 4);
      return `${index === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');

  const area = `${path} L${width},${height} L0,${height} Z`;

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      className={cn('overflow-visible', className)}
      style={{ width, height }}
      aria-hidden="true"
    >
      <defs>
        <linearGradient id={`spark-${tone}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={colors[tone]} stopOpacity="0.22" />
          <stop offset="100%" stopColor={colors[tone]} stopOpacity="0" />
        </linearGradient>
      </defs>
      <motion.path
        d={area}
        fill={`url(#spark-${tone})`}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: DURATION.layout, ease: EASE_OUT_EXPO }}
      />
      <motion.path
        d={path}
        fill="none"
        stroke={colors[tone]}
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        initial={reduced ? { pathLength: 1, opacity: 0 } : { pathLength: 0, opacity: 1 }}
        animate={{ pathLength: 1, opacity: 1 }}
        transition={{ duration: reduced ? DURATION.micro : 0.9, ease: EASE_OUT_EXPO }}
      />
    </svg>
  );
}

// ────────────────────────────── 大小测量 ──────────────────────────────

/** 折叠展开：高度自适应内容。用于审计行的行内展开、设置项的高级选项 */
export function Collapse({ open, children }: { open: boolean; children: ReactNode }) {
  const reduced = useReducedMotion();

  return (
    <AnimatePresence initial={false}>
      {open && (
        <motion.div
          initial={{ height: 0, opacity: 0 }}
          animate={{ height: 'auto', opacity: 1 }}
          exit={{ height: 0, opacity: 0 }}
          transition={{
            duration: reduced ? DURATION.micro : DURATION.layout,
            ease: EASE_OUT_EXPO,
          }}
          className="overflow-hidden"
        >
          {children}
        </motion.div>
      )}
    </AnimatePresence>
  );
}

/** 新条目从顶部弹入并带一次高亮扫过 —— 实时审计流用它 */
export function SlideIn({ children, className }: { children: ReactNode; className?: string }) {
  const reduced = useReducedMotion();

  return (
    <motion.div
      initial={reduced ? { opacity: 0 } : { opacity: 0, y: -12, scale: 0.99 }}
      animate={{ opacity: 1, y: 0, scale: 1 }}
      transition={
        reduced
          ? { duration: DURATION.micro }
          : { type: 'spring', ...SPRING_SOFT }
      }
      className={cn('sweep-in', className)}
    >
      {children}
    </motion.div>
  );
}

export { AnimatePresence, motion };
