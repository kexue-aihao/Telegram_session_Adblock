import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react';
import { forwardRef } from 'react';
import { cn } from '../../lib/cn.ts';

/**
 * 基础组件。
 *
 * 每个组件都只做一件事，且**不隐藏 DOM 语义** —— 全部透传原生属性，
 * 这样 `aria-*`、`disabled`、`autoFocus` 这些不需要额外包装就能用。
 * 面板是运维天天要用的工具，键盘可达性不是可选项。
 */

// ────────────────────────────── 按钮 ──────────────────────────────

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success';
type ButtonSize = 'sm' | 'md' | 'lg' | 'icon';

const BUTTON_VARIANTS: Record<ButtonVariant, string> = {
  // 主按钮用品牌渐变 + 内高光，是整个界面里唯一「发光」的元素
  primary:
    'bg-gradient-to-b from-[var(--color-brand-strong)] to-[var(--color-brand)] text-white ' +
    'shadow-[inset_0_1px_0_rgb(255_255_255/0.25),0_1px_2px_rgb(0_0_0/0.3)] ' +
    'hover:brightness-110 active:brightness-95',
  secondary:
    'bg-[var(--color-bg-2)] text-[var(--color-fg)] border border-[var(--color-line)] ' +
    'shadow-[inset_0_1px_0_var(--color-highlight)] hover:bg-[var(--color-bg-3)]',
  ghost: 'text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg)]',
  danger: 'bg-[var(--color-danger)] text-white hover:brightness-110 active:brightness-95',
  success: 'bg-[var(--color-success)] text-[#062015] hover:brightness-110',
};

const BUTTON_SIZES: Record<ButtonSize, string> = {
  sm: 'h-7 px-2.5 text-xs rounded-lg gap-1.5',
  md: 'h-9 px-3.5 text-sm rounded-xl gap-2',
  lg: 'h-11 px-5 text-base rounded-xl gap-2',
  icon: 'h-8 w-8 rounded-lg justify-center',
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', loading = false, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      // 加载中同时禁用点击：重复提交是操作类面板最常见的脏数据来源
      disabled={disabled || loading}
      className={cn(
        'inline-flex select-none items-center font-medium transition-all',
        // 时长从令牌取，保证全局一致；用 style 而不是 arbitrary value 是因为
        // Tailwind 的 duration-* 不接受 CSS 变量
        'duration-[var(--duration-micro)] ease-[var(--ease-state)]',
        'disabled:cursor-not-allowed disabled:opacity-45',
        BUTTON_VARIANTS[variant],
        BUTTON_SIZES[size],
        className,
      )}
      {...rest}
    >
      {loading && <Spinner className="size-3.5" />}
      {children}
    </button>
  );
});

// ────────────────────────────── 输入 ──────────────────────────────

const FIELD_BASE =
  'w-full rounded-xl border bg-[var(--color-bg-2)] px-3 text-sm text-[var(--color-fg)] ' +
  'placeholder:text-[var(--color-fg-faint)] transition-all ' +
  'duration-[var(--duration-micro)] ease-[var(--ease-state)] ' +
  'border-[var(--color-line)] focus:border-[var(--color-brand)] ' +
  'focus:shadow-[0_0_0_3px_var(--color-brand-soft)] focus:outline-none ' +
  'disabled:cursor-not-allowed disabled:opacity-50';

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }>(
  function Input({ className, invalid, ...rest }, ref) {
    return (
      <input
        ref={ref}
        aria-invalid={invalid}
        className={cn(
          FIELD_BASE,
          'h-9',
          invalid && 'border-[var(--color-danger)] focus:shadow-[0_0_0_3px_var(--color-danger-soft)]',
          className,
        )}
        {...rest}
      />
    );
  },
);

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean }
>(function Textarea({ className, invalid, ...rest }, ref) {
  return (
    <textarea
      ref={ref}
      aria-invalid={invalid}
      className={cn(
        FIELD_BASE,
        'py-2 leading-relaxed',
        invalid && 'border-[var(--color-danger)]',
        className,
      )}
      {...rest}
    />
  );
});

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(
  function Select({ className, children, ...rest }, ref) {
    return (
      <select
        ref={ref}
        className={cn(FIELD_BASE, 'h-9 cursor-pointer appearance-none pr-8', className)}
        // 下拉箭头用背景图而不是伪元素：原生 select 不允许伪元素
        style={{
          backgroundImage:
            "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12'%3E%3Cpath d='M3 4.5L6 7.5L9 4.5' stroke='%239BA1B0' stroke-width='1.5' fill='none' stroke-linecap='round'/%3E%3C/svg%3E\")",
          backgroundRepeat: 'no-repeat',
          backgroundPosition: 'right 10px center',
        }}
        {...rest}
      >
        {children}
      </select>
    );
  },
);

/** 带标签与说明的字段容器；错误信息占据固定位置，避免出错时整页跳动 */
export function Field({
  label,
  hint,
  error,
  children,
  className,
}: {
  label: string;
  hint?: ReactNode;
  error?: string | null;
  children: ReactNode;
  className?: string;
}) {
  return (
    <label className={cn('block space-y-1.5', className)}>
      <span className="flex items-baseline justify-between gap-3">
        <span className="text-xs font-medium text-[var(--color-fg-muted)]">{label}</span>
        {hint && <span className="text-2xs text-[var(--color-fg-subtle)]">{hint}</span>}
      </span>
      {children}
      {error && <span className="block text-2xs text-[var(--color-danger)]">{error}</span>}
    </label>
  );
}

// ────────────────────────────── 开关 ──────────────────────────────

export function Switch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
  label?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border transition-colors',
        'duration-[var(--duration-state)] ease-[var(--ease-state)]',
        'disabled:cursor-not-allowed disabled:opacity-45',
        checked
          ? 'border-transparent bg-[var(--color-brand)]'
          : 'border-[var(--color-line)] bg-[var(--color-bg-3)]',
      )}
    >
      <span
        className={cn(
          'inline-block size-3.5 rounded-full bg-white shadow-sm transition-transform',
          // 弹簧感的缓动：滑块用 ease-out-expo 会显得迟疑，
          // 这里用略微过冲的曲线更接近物理直觉
          'duration-[var(--duration-state)] ease-[cubic-bezier(0.34,1.56,0.64,1)]',
          checked ? 'translate-x-[18px]' : 'translate-x-[3px]',
        )}
      />
    </button>
  );
}

// ────────────────────────────── 徽标与卡片 ──────────────────────────────

type BadgeTone = 'neutral' | 'brand' | 'success' | 'warn' | 'danger' | 'info';

const BADGE_TONES: Record<BadgeTone, string> = {
  neutral: 'bg-[var(--color-surface-active)] text-[var(--color-fg-muted)]',
  brand: 'bg-[var(--color-brand-soft)] text-[var(--color-brand-strong)]',
  success: 'bg-[var(--color-success-soft)] text-[var(--color-success)]',
  warn: 'bg-[var(--color-warn-soft)] text-[var(--color-warn)]',
  danger: 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]',
  info: 'bg-[var(--color-brand-soft)] text-[var(--color-info)]',
};

export function Badge({
  tone = 'neutral',
  children,
  className,
}: {
  tone?: BadgeTone;
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-2xs font-medium whitespace-nowrap',
        BADGE_TONES[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/** 状态圆点；比整块徽标更轻，适合放在列表行里 */
export function StatusDot({ tone, pulse }: { tone: BadgeTone; pulse?: boolean }) {
  const colors: Record<BadgeTone, string> = {
    neutral: 'bg-[var(--color-fg-subtle)]',
    brand: 'bg-[var(--color-brand)]',
    success: 'bg-[var(--color-success)]',
    warn: 'bg-[var(--color-warn)]',
    danger: 'bg-[var(--color-danger)]',
    info: 'bg-[var(--color-info)]',
  };
  return (
    <span className="relative inline-flex size-2 shrink-0">
      {pulse && (
        <span
          className={cn('absolute inset-0 animate-ping rounded-full opacity-60', colors[tone])}
        />
      )}
      <span className={cn('relative inline-flex size-2 rounded-full', colors[tone])} />
    </span>
  );
}

export function Card({
  children,
  className,
  interactive,
  ...rest
}: {
  children: ReactNode;
  className?: string;
  interactive?: boolean;
} & React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        'surface rounded-2xl',
        interactive &&
          // 悬停时上浮 2px 并提亮描边。位移很小，但足以让「这张卡可点」变得明确
          'transition-all duration-[var(--duration-state)] ease-[var(--ease-out-expo)] hover:-translate-y-0.5 hover:border-[var(--color-line-strong)]',
        className,
      )}
      {...rest}
    >
      {children}
    </div>
  );
}

/** 分区标题；统一「标题 + 说明 + 右侧操作」的排版 */
export function SectionHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="space-y-0.5">
        <h2 className="text-lg">{title}</h2>
        {description && <p className="text-xs text-[var(--color-fg-muted)]">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}

// ────────────────────────────── 加载与空态 ──────────────────────────────

export function Spinner({ className }: { className?: string }) {
  return (
    <svg
      className={cn('size-4 animate-spin', className)}
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.2" strokeWidth="3" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
      />
    </svg>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn('skeleton rounded-lg', className)} />;
}

/**
 * 空态。
 *
 * 刻意不用插画：一张通用插画会让所有空态长得一样，用户无法一眼分辨
 * 「还没有数据」和「筛选没结果」。用图标 + 一句人话 + 一个动作更有效。
 */
export function EmptyState({
  icon,
  title,
  description,
  action,
  compact,
}: {
  icon?: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
  compact?: boolean;
}) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center gap-3 text-center',
        compact ? 'py-8' : 'py-16',
      )}
    >
      {icon && (
        <div className="flex size-11 items-center justify-center rounded-2xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] text-[var(--color-fg-subtle)]">
          {icon}
        </div>
      )}
      <div className="space-y-1">
        <p className="text-sm font-medium text-[var(--color-fg)]">{title}</p>
        {description && (
          <p className="max-w-sm text-xs leading-relaxed text-[var(--color-fg-muted)]">
            {description}
          </p>
        )}
      </div>
      {action}
    </div>
  );
}

/** 错误态；把原始信息保留在 details 里，便于用户反馈问题时复制 */
export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 py-12 text-center">
      <div className="flex size-11 items-center justify-center rounded-2xl bg-[var(--color-danger-soft)] text-[var(--color-danger)]">
        <svg viewBox="0 0 24 24" className="size-5" fill="none" aria-hidden="true">
          <path
            d="M12 8v5m0 3h.01M10.3 3.9 2.4 17.5A2 2 0 0 0 4.1 20.5h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </div>
      <p className="max-w-md text-xs text-[var(--color-fg-muted)]">{message}</p>
      {onRetry && (
        <Button size="sm" onClick={onRetry}>
          重试
        </Button>
      )}
    </div>
  );
}

/** 一段等宽文本，用于 token 掩码、正则、ID */
export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <code className={cn('font-mono text-xs tracking-tight', className)}>{children}</code>
  );
}

/**
 * 页面容器。
 *
 * 统一的边距与最大宽度放在一处。每个页面各写一遍 `p-6 max-w-[1400px] mx-auto`
 * 的结果是它们会慢慢不一样，而页面之间的对齐差异是最容易被眼睛捕捉到的
 * 低级不一致。
 */
export function Page({
  children,
  className,
  wide,
}: {
  children: ReactNode;
  className?: string;
  wide?: boolean;
}) {
  return (
    <div
      className={cn(
        'mx-auto w-full space-y-5 p-5 lg:p-6',
        wide ? 'max-w-none' : 'max-w-[1440px]',
        className,
      )}
    >
      {children}
    </div>
  );
}

/** 列表行：统一的悬停与分隔线，用于审计、会话、规则等所有列表 */
export function Row({
  children,
  className,
  onClick,
  selected,
}: {
  children: ReactNode;
  className?: string;
  onClick?: () => void;
  selected?: boolean;
}) {
  const interactive = Boolean(onClick);
  return (
    <div
      onClick={onClick}
      role={interactive ? 'button' : undefined}
      tabIndex={interactive ? 0 : undefined}
      onKeyDown={
        interactive
          ? (event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                onClick?.();
              }
            }
          : undefined
      }
      className={cn(
        'border-b border-[var(--color-line-subtle)] px-4 py-3 transition-colors duration-[var(--duration-micro)] last:border-b-0',
        interactive && 'cursor-pointer hover:bg-[var(--color-surface-hover)]',
        selected && 'bg-[var(--color-surface-active)]',
        className,
      )}
    >
      {children}
    </div>
  );
}

/** 空状态的行内版本，用于表格与列表内部 */
export function InlineEmpty({ children }: { children: ReactNode }) {
  return (
    <p className="px-4 py-8 text-center text-xs text-[var(--color-fg-subtle)]">{children}</p>
  );
}

