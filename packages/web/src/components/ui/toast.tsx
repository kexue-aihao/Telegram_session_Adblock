import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import { cn } from '../../lib/cn.ts';
import { DURATION, SPRING_SOFT } from '../motion/index.tsx';

/**
 * 轻量提示。
 *
 * 从右下角弹入、按 `layout` 自动重排 —— 新提示插入时其余会平滑让位，
 * 而不是生硬地跳一下。这个细节是「高级感」里最容易被感知到的一处。
 *
 * 有一条刻意的克制：**成功提示默认 2.6 秒就消失，且不阻止点击**。
 * 很多面板把提示做成必须手动关闭的浮层，用久了非常烦人。
 */

type ToastTone = 'success' | 'error' | 'info' | 'warn';

interface Toast {
  id: number;
  tone: ToastTone;
  title: string;
  description?: string;
  /** 操作按钮，例如「撤销」「查看」 */
  action?: { label: string; onClick: () => void };
  /** 毫秒；0 表示不自动关闭 */
  duration: number;
}

interface ToastContextValue {
  push: (toast: Omit<Toast, 'id' | 'duration'> & { duration?: number }) => number;
  success: (title: string, description?: string) => number;
  error: (title: string, description?: string) => number;
  info: (title: string, description?: string) => number;
  dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastContextValue | null>(null);

export function useToast(): ToastContextValue {
  const context = useContext(ToastContext);
  if (!context) throw new Error('useToast 必须在 ToastProvider 内部使用');
  return context;
}

const TONE_STYLES: Record<ToastTone, { ring: string; icon: ReactNode }> = {
  success: {
    ring: 'border-[var(--color-success)]/30',
    icon: (
      <svg viewBox="0 0 16 16" className="size-4 text-[var(--color-success)]" fill="none">
        <path
          d="M3.5 8.5l3 3 6-6.5"
          stroke="currentColor"
          strokeWidth="1.8"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    ),
  },
  error: {
    ring: 'border-[var(--color-danger)]/35',
    icon: (
      <svg viewBox="0 0 16 16" className="size-4 text-[var(--color-danger)]" fill="none">
        <path d="M8 4v5m0 2.5h.01" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
        <circle cx="8" cy="8" r="6.2" stroke="currentColor" strokeWidth="1.4" />
      </svg>
    ),
  },
  warn: {
    ring: 'border-[var(--color-warn)]/35',
    icon: (
      <svg viewBox="0 0 16 16" className="size-4 text-[var(--color-warn)]" fill="none">
        <path
          d="M8 2.5 1.8 13h12.4L8 2.5Zm0 4v3m0 2h.01"
          stroke="currentColor"
          strokeWidth="1.4"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    ),
  },
  info: {
    ring: 'border-[var(--color-line)]',
    icon: (
      <svg viewBox="0 0 16 16" className="size-4 text-[var(--color-brand)]" fill="none">
        <circle cx="8" cy="8" r="6.2" stroke="currentColor" strokeWidth="1.4" />
        <path d="M8 7.2v4M8 5h.01" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
      </svg>
    ),
  },
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const dismiss = useCallback((id: number) => {
    setToasts((current) => current.filter((toast) => toast.id !== id));
  }, []);

  const push = useCallback<ToastContextValue['push']>(
    ({ tone, title, description, action, duration }) => {
      // 用时间戳 + 随机数而不是自增计数器：热更新时模块会重新求值，
      // 计数器会归零并与现有 id 撞车
      const id = Date.now() + Math.floor(Math.random() * 1000);
      const finalDuration = duration ?? (tone === 'error' ? 6000 : 2600);

      setToasts((current) => {
        // 最多同时显示 4 条：再多会遮住界面而且没人看
        const next = [...current, { id, tone, title, description, action, duration: finalDuration }];
        return next.slice(-4);
      });

      if (finalDuration > 0) {
        window.setTimeout(() => dismiss(id), finalDuration);
      }
      return id;
    },
    [dismiss],
  );

  const value = useMemo<ToastContextValue>(
    () => ({
      push,
      dismiss,
      success: (title, description) => push({ tone: 'success', title, description }),
      error: (title, description) => push({ tone: 'error', title, description }),
      info: (title, description) => push({ tone: 'info', title, description }),
    }),
    [push, dismiss],
  );

  return (
    <ToastContext.Provider value={value}>
      {children}
      <Viewport toasts={toasts} onDismiss={dismiss} />
    </ToastContext.Provider>
  );
}

function Viewport({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  const reduced = useReducedMotion();

  return (
    <div
      // 移动端贴边、桌面端留出呼吸空间；z-index 高于 Modal，
      // 因为「模态里触发的操作结果」必须能看见
      className="pointer-events-none fixed right-4 bottom-4 z-[60] flex w-[min(360px,calc(100vw-2rem))] flex-col gap-2"
      role="region"
      aria-label="通知"
    >
      <AnimatePresence initial={false}>
        {toasts.map((toast) => (
          <motion.div
            key={toast.id}
            layout
            initial={reduced ? { opacity: 0 } : { opacity: 0, x: 24, scale: 0.97 }}
            animate={{ opacity: 1, x: 0, scale: 1 }}
            exit={reduced ? { opacity: 0 } : { opacity: 0, x: 16, scale: 0.98 }}
            transition={
              reduced ? { duration: DURATION.micro } : { type: 'spring', ...SPRING_SOFT }
            }
            className={cn(
              'glass pointer-events-auto flex items-start gap-3 rounded-2xl border px-3.5 py-3 shadow-xl',
              TONE_STYLES[toast.tone].ring,
            )}
            role={toast.tone === 'error' ? 'alert' : 'status'}
          >
            <span className="mt-0.5 shrink-0">{TONE_STYLES[toast.tone].icon}</span>

            <div className="min-w-0 flex-1 space-y-0.5">
              <p className="text-sm font-medium text-[var(--color-fg)]">{toast.title}</p>
              {toast.description && (
                <p className="text-xs leading-relaxed break-words text-[var(--color-fg-muted)]">
                  {toast.description}
                </p>
              )}
              {toast.action && (
                <button
                  type="button"
                  onClick={() => {
                    toast.action?.onClick();
                    onDismiss(toast.id);
                  }}
                  className="mt-1 text-xs font-medium text-[var(--color-brand-strong)] hover:underline"
                >
                  {toast.action.label}
                </button>
              )}
            </div>

            <button
              type="button"
              onClick={() => onDismiss(toast.id)}
              aria-label="关闭通知"
              className="-mt-0.5 -mr-0.5 shrink-0 rounded-md p-1 text-[var(--color-fg-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg)]"
            >
              <svg viewBox="0 0 12 12" className="size-3" fill="none" aria-hidden="true">
                <path
                  d="M3 3l6 6M9 3l-6 6"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  strokeLinecap="round"
                />
              </svg>
            </button>
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  );
}
