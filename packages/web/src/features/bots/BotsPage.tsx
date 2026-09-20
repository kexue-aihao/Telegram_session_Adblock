import type { Bot, BotValidation, GroupCheck } from '@tgs/shared';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { useState } from 'react';
import { api, ApiError } from '../../lib/api.ts';
import { relativeTime } from '../../lib/format.ts';
import { useAsync, useWsEvent } from '../../lib/hooks.ts';
import { DURATION, EASE_OUT_EXPO, Stagger, StaggerItem } from '../../components/motion/index.tsx';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  Input,
  Mono,
  Page,
  SectionHeader,
  Skeleton,
  StatusDot,
  Switch,
} from '../../components/ui/primitives.tsx';
import { ConfirmDialog, Modal } from '../../components/ui/overlay.tsx';
import { useToast } from '../../components/ui/toast.tsx';
import {
  IconBot,
  IconCheck,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconWarning,
} from '../../components/ui/icons.tsx';

/**
 * 机器人管理。
 *
 * 创建流程刻意复刻 @BotFather 的三步（申请 token → 校验 → 绑定管理群），
 * 但把每一步的**失败原因**直接摆在界面上。这个产品的搭建门槛几乎全在这里，
 * 而失败原因几乎全是 Telegram 侧的权限与配置问题 —— 一句「校验失败」
 * 会让用户去猜半小时。
 */
export function BotsPage() {
  const toast = useToast();
  const [wizardOpen, setWizardOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Bot | null>(null);
  const [deleting, setDeleting] = useState(false);

  const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'), []);
  useWsEvent('bot.status', () => bots.reload());

  async function onDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      const impact = await api.get<{ topics: number; rules: number }>(
        `/api/bots/${deleteTarget.id}/impact`,
      );
      void impact;
      await api.delete(`/api/bots/${deleteTarget.id}`);
      toast.success('机器人已删除', `@${deleteTarget.username} 及其会话记录已一并清除`);
      setDeleteTarget(null);
      bots.reload();
    } catch (err) {
      toast.error('删除失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setDeleting(false);
    }
  }

  return (
    <Page>
      <SectionHeader
        title="机器人"
        description="每个机器人独立轮询、独立话题空间；token 以 AES-256-GCM 加密存储，界面上永不回显明文。"
        actions={
          <Button variant="primary" onClick={() => setWizardOpen(true)}>
            <IconPlus className="size-4" />
            创建机器人
          </Button>
        }
      />

      {bots.loading && !bots.data ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {[0, 1, 2].map((index) => (
            <Skeleton key={index} className="h-40" />
          ))}
        </div>
      ) : (bots.data?.items.length ?? 0) === 0 ? (
        <Card>
          <EmptyState
            icon={<IconBot />}
            title="还没有添加机器人"
            description="先在 @BotFather 那里申请一个机器人拿到 token，再回到这里粘贴。整个过程大约两分钟。"
            action={
              <Button variant="primary" onClick={() => setWizardOpen(true)}>
                <IconPlus className="size-4" />
                开始创建
              </Button>
            }
          />
        </Card>
      ) : (
        <Stagger className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
          {bots.data?.items.map((bot, index) => (
            <StaggerItem key={bot.id} index={index}>
              <BotCard
                bot={bot}
                onDeleted={() => setDeleteTarget(bot)}
                onChanged={bots.reload}
              />
            </StaggerItem>
          ))}
        </Stagger>
      )}

      <BotWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        onCreated={() => {
          setWizardOpen(false);
          bots.reload();
        }}
      />

      <ConfirmDialog
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => void onDelete()}
        loading={deleting}
        danger
        title="删除机器人"
        confirmLabel="确认删除"
        message={
          <>
            将删除 <Mono className="text-[var(--color-fg)]">{deleteTarget?.name}</Mono> 及其
            <strong className="text-[var(--color-fg)]"> 全部会话、消息与命中记录</strong>
            。此操作不可撤销。
            <br />
            <br />
            Telegram 侧的群与话题不会被删除，机器人只是不再响应。
          </>
        }
      />
    </Page>
  );
}

// ────────────────────────────── 机器人卡片 ──────────────────────────────

function BotCard({
  bot,
  onDeleted,
  onChanged,
}: {
  bot: Bot;
  onDeleted: () => void;
  onChanged: () => void;
}) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [check, setCheck] = useState<GroupCheck | null>(null);

  const tone = {
    online: 'success',
    starting: 'warn',
    error: 'danger',
    stopped: 'neutral',
    unknown: 'neutral',
  }[bot.healthStatus] as 'success' | 'warn' | 'danger' | 'neutral';

  const labels = {
    online: '在线',
    starting: '启动中',
    error: '异常',
    stopped: '已停止',
    unknown: '未知',
  } as const;

  async function toggleEnabled(next: boolean) {
    setBusy(true);
    try {
      await api.patch(`/api/bots/${bot.id}`, { isEnabled: next });
      toast.success(next ? '已启用机器人' : '已停用机器人');
      onChanged();
    } catch (err) {
      toast.error('操作失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setBusy(false);
    }
  }

  async function checkGroup() {
    setBusy(true);
    try {
      setCheck(await api.post<GroupCheck>(`/api/bots/${bot.id}/check-group`));
    } catch (err) {
      toast.error('体检失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setBusy(false);
    }
  }

  async function reload() {
    setBusy(true);
    try {
      await api.post(`/api/bots/${bot.id}/reload`);
      toast.success('已重新加载');
      onChanged();
    } catch (err) {
      toast.error('重新加载失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="flex flex-col p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <StatusDot tone={tone} pulse={bot.healthStatus === 'starting'} />
            <h3 className="truncate text-sm font-medium">{bot.name}</h3>
          </div>
          <Mono className="mt-0.5 block truncate text-[var(--color-fg-subtle)]">
            @{bot.username}
          </Mono>
        </div>
        <Switch
          checked={bot.isEnabled}
          onChange={(next) => void toggleEnabled(next)}
          disabled={busy}
          label="启用机器人"
        />
      </div>

      <dl className="mt-3.5 space-y-1.5 text-xs">
        <div className="flex items-center justify-between gap-3">
          <dt className="text-[var(--color-fg-subtle)]">状态</dt>
          <dd>
            <Badge tone={tone}>{labels[bot.healthStatus]}</Badge>
          </dd>
        </div>
        <div className="flex items-center justify-between gap-3">
          <dt className="text-[var(--color-fg-subtle)]">管理群</dt>
          <dd className="min-w-0 truncate text-[var(--color-fg-muted)]">
            {bot.adminGroupTitle ?? (bot.adminGroupId ? <Mono>{bot.adminGroupId}</Mono> : '未绑定')}
          </dd>
        </div>
        <div className="flex items-center justify-between gap-3">
          <dt className="text-[var(--color-fg-subtle)]">Token</dt>
          <dd>
            <Mono className="text-[var(--color-fg-muted)]">{bot.tokenMask}</Mono>
          </dd>
        </div>
        <div className="flex items-center justify-between gap-3">
          <dt className="text-[var(--color-fg-subtle)]">最后轮询</dt>
          <dd className="text-[var(--color-fg-muted)]">{relativeTime(bot.lastPolledAt)}</dd>
        </div>
      </dl>

      {bot.lastError && (
        <p className="mt-2.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]">
          {bot.lastError}
        </p>
      )}

      {!bot.adminGroupId && (
        <p className="mt-2.5 flex items-start gap-1.5 rounded-lg bg-[var(--color-warn-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-warn)]">
          <IconWarning className="mt-px size-3.5 shrink-0" />
          尚未绑定管理群，用户私聊的消息无法中继进去。
        </p>
      )}

      <div className="mt-3.5 flex items-center gap-1.5 border-t border-[var(--color-line-subtle)] pt-3">
        <Button size="sm" variant="ghost" onClick={() => void checkGroup()} disabled={busy}>
          <IconCheck className="size-3.5" />
          群体检
        </Button>
        <Button size="sm" variant="ghost" onClick={() => void reload()} disabled={busy}>
          <IconRefresh className="size-3.5" />
          重载
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className="ml-auto text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
          onClick={onDeleted}
          disabled={busy}
        >
          <IconTrash className="size-3.5" />
        </Button>
      </div>

      {check && (
        <div className="mt-3 space-y-1.5 rounded-lg border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] px-2.5 py-2">
          <p className="text-2xs font-medium">
            {check.ok ? '✅ 管理群配置正常' : '⚠️ 管理群配置有问题'}
          </p>
          {check.problems.map((problem) => (
            <p key={problem} className="text-2xs leading-relaxed text-[var(--color-fg-muted)]">
              · {problem}
            </p>
          ))}
          <button
            type="button"
            onClick={() => setCheck(null)}
            className="text-2xs text-[var(--color-fg-subtle)] hover:underline"
          >
            收起
          </button>
        </div>
      )}
    </Card>
  );
}

// ────────────────────────────── 创建向导 ──────────────────────────────

type WizardStep = 'token' | 'group' | 'done';

function BotWizard({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const reduced = useReducedMotion();

  const [step, setStep] = useState<WizardStep>('token');
  const [token, setToken] = useState('');
  const [validation, setValidation] = useState<BotValidation | null>(null);
  const [validating, setValidating] = useState(false);

  const [groupId, setGroupId] = useState('');
  const [groupCheck, setGroupCheck] = useState<GroupCheck | null>(null);
  const [checkingGroup, setCheckingGroup] = useState(false);
  const [creating, setCreating] = useState(false);

  function reset() {
    setStep('token');
    setToken('');
    setValidation(null);
    setGroupId('');
    setGroupCheck(null);
  }

  async function onValidate() {
    setValidating(true);
    setValidation(null);
    try {
      const result = await api.post<BotValidation>('/api/bots/validate', {
        token: token.trim(),
      });
      setValidation(result);
      if (result.ok) {
        toast.success('Token 有效', `机器人 @${result.username}`);
      }
    } catch (err) {
      toast.error('校验失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setValidating(false);
    }
  }

  async function onCheckGroup() {
    const parsed = Number.parseInt(groupId.trim(), 10);
    if (!Number.isFinite(parsed) || parsed >= 0) {
      toast.error('群 ID 格式不对', '超级群 ID 是负数，形如 -1001234567890');
      return;
    }

    setCheckingGroup(true);
    try {
      setGroupCheck(
        await api.post<GroupCheck>('/api/bots/check-group', { token: token.trim(), chatId: parsed }),
      );
    } catch (err) {
      toast.error('体检失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setCheckingGroup(false);
    }
  }

  async function onCreate() {
    setCreating(true);
    try {
      const parsed = groupId.trim() ? Number.parseInt(groupId.trim(), 10) : null;
      await api.post('/api/bots', {
        token: token.trim(),
        adminGroupId: Number.isFinite(parsed) ? parsed : null,
        name: validation?.name ?? undefined,
      });
      toast.success('机器人已创建', '正在启动长轮询，状态会实时更新');
      reset();
      onCreated();
    } catch (err) {
      toast.error('创建失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setCreating(false);
    }
  }

  return (
    <Modal
      open={open}
      onClose={() => {
        reset();
        onClose();
      }}
      title="创建机器人"
      description={
        step === 'token'
          ? '第 1 步 / 共 3 步 · 粘贴 BotFather 签发的 token'
          : step === 'group'
            ? '第 2 步 / 共 3 步 · 绑定管理群'
            : '完成'
      }
      footer={
        step === 'token' ? (
          <>
            <Button variant="ghost" onClick={onClose}>
              取消
            </Button>
            <Button
              variant="primary"
              onClick={() => void onValidate()}
              loading={validating}
              disabled={token.trim().length < 10}
            >
              验证 Token
            </Button>
          </>
        ) : step === 'group' ? (
          <>
            <Button variant="ghost" onClick={() => setStep('token')}>
              上一步
            </Button>
            <Button variant="primary" onClick={() => void onCreate()} loading={creating}>
              {groupId.trim() ? '完成创建' : '稍后再绑定群'}
            </Button>
          </>
        ) : null
      }
    >
      <AnimatePresence mode="wait">
        <motion.div
          key={step}
          initial={reduced ? { opacity: 0 } : { opacity: 0, x: 12 }}
          animate={{ opacity: 1, x: 0 }}
          exit={reduced ? { opacity: 0 } : { opacity: 0, x: -12 }}
          transition={{ duration: DURATION.layout, ease: EASE_OUT_EXPO }}
          className="space-y-4"
        >
          {step === 'token' && (
            <>
              <div className="space-y-2 rounded-xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] p-3.5">
                <p className="text-xs font-medium">在 @BotFather 里这样操作</p>
                <ol className="space-y-1 text-2xs leading-relaxed text-[var(--color-fg-muted)]">
                  <li>1. 打开 @BotFather，发送 /newbot</li>
                  <li>2. 依次输入机器人名称与用户名（用户名必须以 bot 结尾）</li>
                  <li>3. 复制它返回的那串 token，粘贴到下面</li>
                  <li>4. 建议再发送 /setprivacy 并选择 Disable —— 否则机器人读不到群里的普通消息</li>
                </ol>
              </div>

              <Field
                label="Bot Token"
                hint="形如 123456789:AAF…"
                error={validation && !validation.ok ? (validation.error ?? '验证失败') : null}
              >
                <Input
                  value={token}
                  onChange={(event) => setToken(event.target.value)}
                  placeholder="123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-xs"
                  invalid={Boolean(validation && !validation.ok)}
                />
              </Field>

              {validation?.ok && (
                <motion.div
                  initial={{ opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ duration: DURATION.state, ease: EASE_OUT_EXPO }}
                  className="space-y-1.5 rounded-xl bg-[var(--color-success-soft)] px-3.5 py-3"
                >
                  <p className="text-xs font-medium text-[var(--color-success)]">
                    ✅ 已验证：{validation.name}（@{validation.username}）
                  </p>
                  {validation.canReadAllGroupMessages === false && (
                    <p className="text-2xs leading-relaxed text-[var(--color-warn)]">
                      ⚠️ 该机器人处于 Privacy Mode，读不到群里的普通消息。请到 @BotFather 发送
                      /setprivacy → 选择 Disable，否则话题中继无法工作。
                    </p>
                  )}
                  <Button
                    size="sm"
                    variant="primary"
                    onClick={() => setStep('group')}
                    className="mt-1"
                  >
                    下一步：绑定管理群
                  </Button>
                </motion.div>
              )}
            </>
          )}

          {step === 'group' && (
            <>
              <div className="space-y-2 rounded-xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] p-3.5">
                <p className="text-xs font-medium">绑定前请在 Telegram 里准备好</p>
                <ol className="space-y-1 text-2xs leading-relaxed text-[var(--color-fg-muted)]">
                  <li>1. 建一个私有超级群，在群设置里开启「话题 / Topics」</li>
                  <li>2. 把机器人拉进群，并设为管理员</li>
                  <li>
                    3. 至少勾选「管理话题」与「删除消息」两项权限 ——
                    前者用于建话题，后者用于撤回命中的广告
                  </li>
                  <li>4. 获取群 ID：把群消息转发给 @userinfobot，或看群链接</li>
                </ol>
              </div>

              <Field label="管理群 ID" hint="超级群 ID 是负数，形如 -1001234567890">
                <Input
                  value={groupId}
                  onChange={(event) => setGroupId(event.target.value)}
                  placeholder="-1001234567890"
                  inputMode="numeric"
                  className="font-mono text-xs"
                />
              </Field>

              <Button
                size="sm"
                variant="secondary"
                onClick={() => void onCheckGroup()}
                loading={checkingGroup}
                disabled={!groupId.trim()}
              >
                体检这个群
              </Button>

              {groupCheck && (
                <motion.div
                  initial={{ opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  className={`space-y-1.5 rounded-xl px-3.5 py-3 ${
                    groupCheck.ok
                      ? 'bg-[var(--color-success-soft)]'
                      : 'bg-[var(--color-warn-soft)]'
                  }`}
                >
                  <p
                    className={`text-xs font-medium ${
                      groupCheck.ok ? 'text-[var(--color-success)]' : 'text-[var(--color-warn)]'
                    }`}
                  >
                    {groupCheck.ok
                      ? `✅ ${groupCheck.title ?? '管理群'} 配置正常`
                      : `⚠️ ${groupCheck.title ?? '管理群'} 还有问题`}
                  </p>
                  {groupCheck.problems.map((problem) => (
                    <p
                      key={problem}
                      className="text-2xs leading-relaxed text-[var(--color-fg-muted)]"
                    >
                      · {problem}
                    </p>
                  ))}
                </motion.div>
              )}
            </>
          )}

          {step === 'done' && null}
        </motion.div>
      </AnimatePresence>
    </Modal>
  );
}
