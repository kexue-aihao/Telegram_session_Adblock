import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { useEffect, useRef, type ReactNode } from 'react';
import { cn } from '../../lib/cn.ts';
import { DURATION, EASE_OUT_EXPO, SPRING_SOFT } from '../motion/index.tsx';
import { Button } from './primitives.tsx';

/**
 * 浮层组件：模态、抽屉、确认框。
 *
 * 三者共用同一套「打开时锁滚动 + Esc 关闭 + 焦点回到触发元素」的行为。
 * 把这些各写一遍是最常见的可访问性 bug 来源：模态关了但焦点丢在 body 上，
 * 键盘用户之后就完全无法操作页面。
 */

/** 打开浮层时的公共行为 */
function useOverlayBehavior(open: boolean, onClose: () => void) {
  const restoreFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!open) return;

    // 记住打开前的焦点，关闭时还回去
    restoreFocusRef.current = document.activeElement as HTMLElement | null;

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
    };
    document.addEventListener('keydown', onKeyDown);

    // 锁滚动：不锁的话背景会跟着滚，在移动端尤其明显
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.body.style.overflow = previousOverflow;
      restoreFocusRef.current?.focus?.();
    };
  }, [open, onClose]);
}

function Backdrop({ onClick }: { onClick: () => void }) {
  return (
    <motion.div
      className="fixed inset-0 z-40 bg-black/55 backdrop-blur-[2px]"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: DURATION.state, ease: EASE_OUT_EXPO }}
      onClick={onClick}
      aria-hidden="true"
    />
  );
}

export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = 'md',
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
  size?: 'sm' | 'md' | 'lg';
}) {
  useOverlayBehavior(open, onClose);
  const reduced = useReducedMotion();

  const widths = { sm: 'max-w-sm', md: 'max-w-lg', lg: 'max-w-3xl' };

  return (
    <AnimatePresence>
      {open && (
        <>
          <Backdrop onClick={onClose} />
          <div className="fixed inset-0 z-50 flex items-center justify-center p-4">
            <motion.div
              role="dialog"
              aria-modal="true"
              aria-label={title}
              initial={reduced ? { opacity: 0 } : { opacity: 0, scale: 0.97, y: 8 }}
              animate={{ opacity: 1, scale: 1, y: 0 }}
              exit={reduced ? { opacity: 0 } : { opacity: 0, scale: 0.98, y: 4 }}
              transition={
                reduced ? { duration: DURATION.micro } : { type: 'spring', ...SPRING_SOFT }
              }
              className={cn(
                'surface-2 w-full rounded-3xl shadow-2xl',
                widths[size],
              )}
            >
              <div className="flex items-start justify-between gap-4 border-b border-[var(--color-line-subtle)] px-5 py-4">
                <div className="space-y-0.5">
                  <h3 className="text-base">{title}</h3>
                  {description && (
                    <p className="text-xs text-[var(--color-fg-muted)]">{description}</p>
                  )}
                </div>
                <button
                  type="button"
                  onClick={onClose}
                  aria-label="关闭"
                  className="-mr-1 -mt-1 rounded-lg p-1.5 text-[var(--color-fg-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg)]"
                >
                  <svg viewBox="0 0 16 16" className="size-4" fill="none" aria-hidden="true">
                    <path
                      d="M4 4l8 8M12 4l-8 8"
                      stroke="currentColor"
                      strokeWidth="1.6"
                      strokeLinecap="round"
                    />
                  </svg>
                </button>
              </div>

              <div className="max-h-[65vh] overflow-y-auto px-5 py-4">{children}</div>

              {footer && (
                <div className="flex items-center justify-end gap-2 border-t border-[var(--color-line-subtle)] px-5 py-3.5">
                  {footer}
                </div>
              )}
            </motion.div>
          </div>
        </>
      )}
    </AnimatePresence>
  );
}

/** 右侧抽屉；用于「以管理员身份回复」这类需要保留左侧上下文的操作 */
export function Drawer({
  open,
  onClose,
  title,
  children,
  width = 420,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  width?: number;
}) {
  useOverlayBehavior(open, onClose);
  const reduced = useReducedMotion();

  return (
    <AnimatePresence>
      {open && (
        <>
          <Backdrop onClick={onClose} />
          <motion.aside
            role="dialog"
            aria-modal="true"
            aria-label={title}
            initial={reduced ? { opacity: 0 } : { x: width }}
            animate={reduced ? { opacity: 1 } : { x: 0 }}
            exit={reduced ? { opacity: 0 } : { x: width }}
            transition={
              reduced
                ? { duration: DURATION.micro }
                : { type: 'spring', stiffness: 420, damping: 38 }
            }
            style={{ width }}
            className="glass fixed top-0 right-0 bottom-0 z-50 flex flex-col border-l border-[var(--color-line)]"
          >
            <header className="flex items-center justify-between gap-4 border-b border-[var(--color-line-subtle)] px-5 py-4">
              <h3 className="text-base">{title}</h3>
              <button
                type="button"
                onClick={onClose}
                aria-label="关闭"
                className="rounded-lg p-1.5 text-[var(--color-fg-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg)]"
              >
                <svg viewBox="0 0 16 16" className="size-4" fill="none" aria-hidden="true">
                  <path
                    d="M4 4l8 8M12 4l-8 8"
                    stroke="currentColor"
                    strokeWidth="1.6"
                    strokeLinecap="round"
                  />
                </svg>
              </button>
            </header>
            <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>
          </motion.aside>
        </>
      )}
    </AnimatePresence>
  );
}

/**
 * 危险操作确认。
 *
 * 与普通 Modal 分开的理由：危险操作需要一个**明确的措辞规范** ——
 * 说清楚「会发生什么」而不是「确定吗」，并且确认按钮是红色的。
 * 混在通用 Modal 里，这个规范迟早会被某个调用点忽略。
 */
export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  message,
  confirmLabel = '确认',
  danger = false,
  loading = false,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  loading?: boolean;
}) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={loading}>
            取消
          </Button>
          <Button
            variant={danger ? 'danger' : 'primary'}
            onClick={onConfirm}
            loading={loading}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm leading-relaxed text-[var(--color-fg-muted)]">{message}</div>
    </Modal>
  );
}
