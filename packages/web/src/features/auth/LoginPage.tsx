import { motion, useReducedMotion } from 'motion/react';
import { useState, type FormEvent } from 'react';
import { useAuth } from '../../auth.tsx';
import { DURATION, EASE_OUT_EXPO } from '../../components/motion/index.tsx';
import { Button, Field, Input } from '../../components/ui/primitives.tsx';

/**
 * 登录页。
 *
 * 单管理员密码登录，因此没有「记住我」「忘记密码」这些会引入其他流程的入口 ——
 * 密码忘了就改 `.env` 里的 ADMIN_PASSWORD 后重建账号，这是自托管工具应有的
 * 简单模型。
 *
 * 界面上有一个刻意的取舍：**只有密码，没有用户名**。面板只有一个管理员，
 * 让用户多填一个恒为 admin 的字段只是徒增摩擦。
 */
export function LoginPage() {
  const { login } = useAuth();
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [attemptsLeft, setAttemptsLeft] = useState<number | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const reduced = useReducedMotion();

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!password || submitting) return;

    setSubmitting(true);
    setError(null);

    const result = await login(password);
    setSubmitting(false);

    if (!result.ok) {
      setError(result.error ?? '登录失败');
      setAttemptsLeft(result.attemptsLeft ?? null);
      setPassword('');
    }
  }

  return (
    <div className="relative flex h-full items-center justify-center overflow-hidden px-4">
      {/* 背景的两团柔光：给纯色背景一点纵深，但不引入任何真实元素 */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute -top-40 -left-32 size-[520px] rounded-full opacity-[0.16] blur-[120px]"
        style={{ background: 'radial-gradient(circle, var(--color-brand), transparent 70%)' }}
      />
      <div
        aria-hidden="true"
        className="pointer-events-none absolute -right-40 -bottom-48 size-[560px] rounded-full opacity-[0.14] blur-[130px]"
        style={{ background: 'radial-gradient(circle, var(--color-accent), transparent 70%)' }}
      />

      <motion.div
        initial={reduced ? { opacity: 0 } : { opacity: 0, y: 12, scale: 0.99 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: DURATION.page, ease: EASE_OUT_EXPO }}
        className="surface relative w-full max-w-[380px] rounded-3xl p-7"
      >
        <div className="mb-6 space-y-1.5">
          <div className="mb-4 flex size-10 items-center justify-center rounded-2xl bg-gradient-to-br from-[var(--color-brand-strong)] to-[var(--color-accent)] shadow-[inset_0_1px_0_rgb(255_255_255/0.25)]">
            <svg viewBox="0 0 24 24" className="size-5 text-white" fill="none" aria-hidden="true">
              <path
                d="M20.5 12.5a7.5 7.5 0 0 1-10.9 6.7L4 20.5l1.4-5.3A7.5 7.5 0 1 1 20.5 12.5Z"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinejoin="round"
              />
            </svg>
          </div>
          <h1 className="text-xl">
            欢迎回来
          </h1>
          <p className="text-xs text-[var(--color-fg-muted)]">
            登录以管理机器人、会话与广告拦截规则
          </p>
        </div>

        <form onSubmit={onSubmit} className="space-y-4">
          <Field label="管理密码">
            <Input
              type="password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              placeholder="请输入密码"
              autoFocus
              autoComplete="current-password"
              invalid={Boolean(error)}
              disabled={submitting}
            />
          </Field>

          {/* 错误区域固定高度，避免出错时整个卡片跳动 */}
          <div className="min-h-[18px]">
            {error && (
              <motion.p
                initial={{ opacity: 0, y: -2 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: DURATION.state }}
                className="text-xs text-[var(--color-danger)]"
              >
                {error}
                {attemptsLeft !== null && attemptsLeft > 0 && (
                  <span className="text-[var(--color-fg-subtle)]">
                    {' '}
                    · 还可尝试 {attemptsLeft} 次
                  </span>
                )}
              </motion.p>
            )}
          </div>

          <Button
            type="submit"
            variant="primary"
            size="lg"
            className="w-full"
            loading={submitting}
            disabled={!password}
          >
            登录
          </Button>
        </form>

        <p className="mt-5 text-center text-2xs leading-relaxed text-[var(--color-fg-faint)]">
          忘记密码？修改 .env 中的 ADMIN_PASSWORD 后删除数据库里的 admin_users 记录，
          重启即可重新播种。
        </p>
      </motion.div>
    </div>
  );
}
