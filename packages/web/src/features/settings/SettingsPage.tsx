import type { Bot, BotSettings, EscalationStep, GlobalSettings, SanctionType } from '@tgs/shared';
import { AnimatePresence, motion } from 'motion/react';
import { useEffect, useState } from 'react';
import { api, ApiError } from '../../lib/api.ts';
import { useAsync } from '../../lib/hooks.ts';
import {
  Badge,
  Button,
  Card,
  Field,
  Input,
  Mono,
  Page,
  SectionHeader,
  Select,
  Skeleton,
  Switch,
  Textarea,
} from '../../components/ui/primitives.tsx';
import { useToast } from '../../components/ui/toast.tsx';
import { useAuth } from '../../auth.tsx';
import { IconPlus, IconTrash, IconWarning } from '../../components/ui/icons.tsx';

/**
 * 设置页。
 *
 * 分三段：全局、单机器人、账号。单机器人那一段是这个页面里真正复杂的地方
 * —— 阶梯处罚与文案模板都在那里，而且它们直接决定用户会不会被误伤，
 * 所以每一档都给出「这一档会发生什么」的人话说明，而不只是放几个数字输入框。
 */

const SANCTION_LABELS: Record<SanctionType, string> = {
  warn: '私聊警告',
  silence: '静默（消息不再进话题，用户无感知）',
  mute: '硬禁言（回复禁言提示与解禁时间）',
  ban: '拉黑（机器人不再响应，话题自动关闭）',
};

export function SettingsPage() {
  return (
    <Page>
      <SectionHeader
        title="设置"
        description="全局配置、单机器人行为与账号安全。改动即时生效，不需要重启服务。"
      />

      <GlobalSettingsCard />
      <BotSettingsCard />
      <AccountCard />
    </Page>
  );
}

// ────────────────────────────── 全局设置 ──────────────────────────────

function GlobalSettingsCard() {
  const toast = useToast();
  const settings = useAsync<GlobalSettings>(() => api.get('/api/settings'), []);
  const [draft, setDraft] = useState<GlobalSettings | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (settings.data) setDraft(settings.data);
  }, [settings.data]);

  async function onSave() {
    if (!draft) return;
    setSaving(true);
    try {
      await api.patch('/api/settings', draft);
      toast.success('设置已保存');
    } catch (err) {
      toast.error('保存失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card className="p-4">
      <SectionHeader title="全局" description="对所有机器人生效" />

      {!draft ? (
        <div className="mt-4 space-y-2">
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ) : (
        <div className="mt-4 space-y-4">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="面板时区" hint="影响统计按哪一天切分">
              <Select
                value={draft.timezone}
                onChange={(event) => setDraft({ ...draft, timezone: event.target.value })}
              >
                <option value="Asia/Shanghai">Asia/Shanghai（北京时间）</option>
                <option value="Asia/Tokyo">Asia/Tokyo</option>
                <option value="Asia/Singapore">Asia/Singapore</option>
                <option value="Europe/London">Europe/London</option>
                <option value="America/New_York">America/New_York</option>
                <option value="UTC">UTC</option>
              </Select>
            </Field>

            <Field label="正则匹配超时" hint="毫秒，超时即判定为疑似 ReDoS">
              <Input
                type="number"
                min={5}
                max={2000}
                value={draft.regexTimeoutMs}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    regexTimeoutMs: Number.parseInt(event.target.value, 10) || 50,
                  })
                }
              />
            </Field>
          </div>

          <Field
            label="命中审计保留天数"
            hint="留空表示永久保留"
          >
            <Input
              type="number"
              min={1}
              max={3650}
              value={draft.auditRetentionDays ?? ''}
              placeholder="永久保留"
              onChange={(event) =>
                setDraft({
                  ...draft,
                  auditRetentionDays: event.target.value
                    ? Number.parseInt(event.target.value, 10)
                    : null,
                })
              }
            />
          </Field>

          <ToggleRow
            label="正则超时后自动停用规则"
            hint="强烈建议开启。一条灾难性回溯的规则会让所有机器人的长轮询一起卡住。"
            checked={draft.autoDisableOnRegexTimeout}
            onChange={(next) => setDraft({ ...draft, autoDisableOnRegexTimeout: next })}
          />

          <div className="flex justify-end">
            <Button variant="primary" onClick={() => void onSave()} loading={saving}>
              保存全局设置
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}

// ────────────────────────────── 单机器人设置 ──────────────────────────────

function BotSettingsCard() {
  const toast = useToast();
  const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'), []);
  const [botId, setBotId] = useState<number | null>(null);

  useEffect(() => {
    const first = bots.data?.items[0];
    if (botId === null && first) setBotId(first.id);
  }, [bots.data, botId]);

  const settings = useAsync<BotSettings>(
    () => api.get(`/api/bots/${botId}/settings`),
    [botId],
    { enabled: botId !== null },
  );

  const [draft, setDraft] = useState<BotSettings | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (settings.data) setDraft(settings.data);
  }, [settings.data]);

  async function onSave() {
    if (!draft || botId === null) return;
    setSaving(true);
    try {
      // 只提交可编辑的字段：botId 由服务端按路径参数确定，传回去没有意义
      const { botId: _ignored, ...payload } = draft;
      void _ignored;
      await api.patch(`/api/bots/${botId}/settings`, payload);
      toast.success('机器人设置已保存', '立即生效，无需重载');
    } catch (err) {
      toast.error('保存失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setSaving(false);
    }
  }

  if ((bots.data?.items.length ?? 0) === 0) {
    return (
      <Card className="p-4">
        <SectionHeader title="机器人设置" description="还没有添加机器人" />
      </Card>
    );
  }

  return (
    <Card className="p-4">
      <SectionHeader
        title="机器人设置"
        description="阶梯处罚、话题行为与全部面向用户的文案"
        actions={
          <Select
            value={botId ?? ''}
            onChange={(event) => setBotId(Number.parseInt(event.target.value, 10))}
            className="h-8 w-48 text-xs"
          >
            {bots.data?.items.map((bot) => (
              <option key={bot.id} value={bot.id}>
                {bot.name}
              </option>
            ))}
          </Select>
        }
      />

      {!draft ? (
        <div className="mt-4 space-y-2">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <div className="mt-4 space-y-6">
          {/* ── 阶梯处罚 ─────────────────────────────── */}
          <section className="space-y-3">
            <div>
              <h4 className="text-sm font-medium">阶梯处罚</h4>
              <p className="mt-0.5 text-2xs leading-relaxed text-[var(--color-fg-subtle)]">
                违规分由规则命中累加（取单次命中的最高分，不叠加）。达到某一档的分数时
                自动升级处置 —— 这是「用户不在管理群里、Telegram 原生禁言用不了」这个
                约束下的替代方案。
              </p>
            </div>

            <EscalationEditor
              steps={draft.escalation.steps}
              onChange={(steps) =>
                setDraft({ ...draft, escalation: { ...draft.escalation, steps } })
              }
            />

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field label="违规分衰减周期" hint="多少天无违规后减半；留空表示永不衰减">
                <Input
                  type="number"
                  min={1}
                  max={3650}
                  value={draft.escalation.decayDays ?? ''}
                  placeholder="永不衰减"
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      escalation: {
                        ...draft.escalation,
                        decayDays: event.target.value
                          ? Number.parseInt(event.target.value, 10)
                          : null,
                      },
                    })
                  }
                />
              </Field>

              <Field label="违规分上限" hint="防止无限累加">
                <Input
                  type="number"
                  min={1}
                  max={100000}
                  value={draft.escalation.maxScore}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      escalation: {
                        ...draft.escalation,
                        maxScore: Number.parseInt(event.target.value, 10) || 999,
                      },
                    })
                  }
                />
              </Field>
            </div>
          </section>

          {/* ── 话题行为 ─────────────────────────────── */}
          <section className="space-y-3 border-t border-[var(--color-line-subtle)] pt-5">
            <h4 className="text-sm font-medium">话题与中继</h4>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Field
                label="话题命名模板"
                hint="{name} {username} {id} {botName}"
              >
                <Input
                  value={draft.topicNameTemplate}
                  onChange={(event) => setDraft({ ...draft, topicNameTemplate: event.target.value })}
                  className="font-mono text-xs"
                />
              </Field>

              <Field label="自动归档" hint="话题静默多少小时后自动关闭；留空表示不自动关闭">
                <Input
                  type="number"
                  min={1}
                  max={8760}
                  value={draft.autoCloseHours ?? ''}
                  placeholder="不自动关闭"
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      autoCloseHours: event.target.value
                        ? Number.parseInt(event.target.value, 10)
                        : null,
                    })
                  }
                />
              </Field>
            </div>

            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <ToggleRow
                label="置顶用户信息卡片"
                hint="话题顶部固定一条含用户信息与快捷按钮的消息"
                checked={draft.pinTopicHeader}
                onChange={(next) => setDraft({ ...draft, pinTopicHeader: next })}
              />
              <ToggleRow
                label="撤回用户私聊里的原消息"
                hint="命中规则时连同用户那边的原消息一起删除"
                checked={draft.deleteOriginMessage}
                onChange={(next) => setDraft({ ...draft, deleteOriginMessage: next })}
              />
              <ToggleRow
                label="镜像编辑"
                hint="用户或管理员改动消息时，同步更新对面那条"
                checked={draft.mirrorEdits}
                onChange={(next) => setDraft({ ...draft, mirrorEdits: next })}
              />
              <ToggleRow
                label="用户屏蔽机器人时提醒管理员"
                hint="发送失败（403）时在话题里说明原因"
                checked={draft.notifyOnUnreachable}
                onChange={(next) => setDraft({ ...draft, notifyOnUnreachable: next })}
              />
              <ToggleRow
                label="规则引擎"
                hint="关闭后所有消息直接中继，不做任何拦截"
                checked={draft.rulesEnabled}
                onChange={(next) => setDraft({ ...draft, rulesEnabled: next })}
              />
              <ToggleRow
                label="通知管理员"
                hint="命中与处罚升级时往话题里发告警卡片"
                checked={draft.notifyAdmins}
                onChange={(next) => setDraft({ ...draft, notifyAdmins: next })}
              />
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Field label="相册合并窗口" hint="毫秒">
                <Input
                  type="number"
                  min={0}
                  max={5000}
                  value={draft.coalesceWindowMs}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      coalesceWindowMs: Number.parseInt(event.target.value, 10) || 0,
                    })
                  }
                />
              </Field>
              <Field label="刷屏阈值" hint="3 秒内超过该条数判定为刷屏">
                <Input
                  type="number"
                  min={3}
                  max={100}
                  value={draft.floodThreshold}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      floodThreshold: Number.parseInt(event.target.value, 10) || 8,
                    })
                  }
                />
              </Field>
              <Field label="触发合并的条数" hint="窗口内超过该条数才合并转发">
                <Input
                  type="number"
                  min={2}
                  max={50}
                  value={draft.coalesceThreshold}
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      coalesceThreshold: Number.parseInt(event.target.value, 10) || 5,
                    })
                  }
                />
              </Field>
            </div>
          </section>

          {/* ── 文案模板 ─────────────────────────────── */}
          <section className="space-y-3 border-t border-[var(--color-line-subtle)] pt-5">
            <div>
              <h4 className="text-sm font-medium">文案模板</h4>
              <p className="mt-0.5 text-2xs leading-relaxed text-[var(--color-fg-subtle)]">
                支持 Markdown。<Mono className="text-[var(--color-fg-muted)]">{'{name} {username} {id} {ruleName} {matched} {score} {until} {outcome} {botName}'}</Mono>{' '}
                会被替换成实际值 —— 替换值里的特殊字符会自动转义，因此昵称里带 <Mono className="text-[var(--color-fg-muted)]">**</Mono> 也不会把消息发送搞坏。
              </p>
            </div>

            <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
              <TemplateField
                label="欢迎语（/start）"
                value={draft.greetingText}
                onChange={(value) => setDraft({ ...draft, greetingText: value })}
              />
              <TemplateField
                label="话题头部卡片"
                value={draft.topicHeaderTemplate}
                onChange={(value) => setDraft({ ...draft, topicHeaderTemplate: value })}
              />
              <TemplateField
                label="警告文案"
                value={draft.warnTemplate}
                onChange={(value) => setDraft({ ...draft, warnTemplate: value })}
              />
              <TemplateField
                label="禁言文案"
                value={draft.muteTemplate}
                onChange={(value) => setDraft({ ...draft, muteTemplate: value })}
              />
              <TemplateField
                label="拉黑文案"
                value={draft.banTemplate}
                onChange={(value) => setDraft({ ...draft, banTemplate: value })}
              />
              <TemplateField
                label="管理员告警卡片"
                value={draft.alertCardTemplate}
                onChange={(value) => setDraft({ ...draft, alertCardTemplate: value })}
              />
            </div>

            <Field
              label="静默文案"
              hint="默认为空。静默的意义就是让用户无感知 —— 一旦回复，等于告诉对方「换个号再来」。"
            >
              <Textarea
                value={draft.silenceTemplate}
                onChange={(event) => setDraft({ ...draft, silenceTemplate: event.target.value })}
                rows={2}
                className="text-xs"
                placeholder="留空则静默时不发送任何提示"
              />
            </Field>
          </section>

          <div className="flex justify-end border-t border-[var(--color-line-subtle)] pt-4">
            <Button variant="primary" onClick={() => void onSave()} loading={saving}>
              保存机器人设置
            </Button>
          </div>
        </div>
      )}
    </Card>
  );
}

function TemplateField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <Field label={label}>
      <Textarea
        value={value}
        onChange={(event) => onChange(event.target.value)}
        rows={4}
        className="text-xs"
      />
    </Field>
  );
}

// ────────────────────────────── 阶梯编辑器 ──────────────────────────────

function EscalationEditor({
  steps,
  onChange,
}: {
  steps: EscalationStep[];
  onChange: (steps: EscalationStep[]) => void;
}) {
  const sorted = [...steps].sort((a, b) => a.atScore - b.atScore);

  function update(id: string, patch: Partial<EscalationStep>) {
    onChange(steps.map((step) => (step.id === id ? { ...step, ...patch } : step)));
  }

  function remove(id: string) {
    onChange(steps.filter((step) => step.id !== id));
  }

  function add() {
    const last = sorted[sorted.length - 1];
    onChange([
      ...steps,
      {
        id: `step-${Date.now()}`,
        atScore: (last?.atScore ?? 0) + 3,
        type: 'warn',
        durationMinutes: null,
        deleteMessage: true,
        notifyAdmins: true,
        enabled: true,
      },
    ]);
  }

  return (
    <div className="space-y-2">
      <AnimatePresence initial={false}>
        {sorted.map((step, index) => (
          <motion.div
            key={step.id}
            layout
            initial={{ opacity: 0, y: 6 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, height: 0 }}
            transition={{ duration: 0.2 }}
            className="rounded-xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] p-3"
          >
            <div className="flex items-start gap-3">
              <div className="flex size-6 shrink-0 items-center justify-center rounded-full bg-[var(--color-surface-active)] text-2xs font-medium text-[var(--color-fg-muted)]">
                {index + 1}
              </div>

              <div className="min-w-0 flex-1 space-y-2.5">
                <div className="flex flex-wrap items-end gap-2.5">
                  <label className="space-y-1">
                    <span className="block text-2xs text-[var(--color-fg-subtle)]">达到分数</span>
                    <Input
                      type="number"
                      min={1}
                      max={10000}
                      value={step.atScore}
                      onChange={(event) =>
                        update(step.id, {
                          atScore: Number.parseInt(event.target.value, 10) || 1,
                        })
                      }
                      className="h-8 w-24 text-xs"
                    />
                  </label>

                  <label className="min-w-[220px] flex-1 space-y-1">
                    <span className="block text-2xs text-[var(--color-fg-subtle)]">处置方式</span>
                    <Select
                      value={step.type}
                      onChange={(event) =>
                        update(step.id, {
                          type: event.target.value as SanctionType,
                          // 只有禁言需要时长，切走时清掉避免留下无意义的数字
                          durationMinutes:
                            event.target.value === 'mute' ? (step.durationMinutes ?? 1440) : null,
                        })
                      }
                      className="h-8 text-xs"
                    >
                      {Object.entries(SANCTION_LABELS).map(([value, label]) => (
                        <option key={value} value={value}>
                          {label}
                        </option>
                      ))}
                    </Select>
                  </label>

                  {step.type === 'mute' && (
                    <label className="space-y-1">
                      <span className="block text-2xs text-[var(--color-fg-subtle)]">
                        时长（分钟）
                      </span>
                      <Input
                        type="number"
                        min={1}
                        max={525600}
                        value={step.durationMinutes ?? 1440}
                        onChange={(event) =>
                          update(step.id, {
                            durationMinutes: Number.parseInt(event.target.value, 10) || null,
                          })
                        }
                        className="h-8 w-28 text-xs"
                      />
                    </label>
                  )}

                  <div className="ml-auto flex items-center gap-2">
                    <Switch
                      checked={step.enabled}
                      onChange={(next) => update(step.id, { enabled: next })}
                      label="启用这一档"
                    />
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
                      onClick={() => remove(step.id)}
                      aria-label="删除这一档"
                      disabled={steps.length <= 1}
                    >
                      <IconTrash className="size-3.5" />
                    </Button>
                  </div>
                </div>

                {step.type === 'silence' && (
                  <p className="text-2xs leading-relaxed text-[var(--color-warn)]">
                    静默用户不会收到任何提示，消息直接石沉大海。它最不打扰人，但也最容易
                    让被误伤的用户困惑 —— 建议只在已明确警告过之后使用。
                  </p>
                )}
                {step.type === 'mute' && step.durationMinutes === null && (
                  <p className="flex items-center gap-1.5 text-2xs text-[var(--color-warn)]">
                    <IconWarning className="size-3.5" />
                    时长为空表示永久禁言，请确认这是你想要的。
                  </p>
                )}
              </div>
            </div>
          </motion.div>
        ))}
      </AnimatePresence>

      <Button size="sm" variant="secondary" onClick={add} disabled={steps.length >= 20}>
        <IconPlus className="size-3.5" />
        添加一档
      </Button>
    </div>
  );
}

// ────────────────────────────── 账号 ──────────────────────────────

function AccountCard() {
  const toast = useToast();
  const { session } = useAuth();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [saving, setSaving] = useState(false);

  const mismatch = confirm.length > 0 && next !== confirm;
  const tooWeak = next.length > 0 && !(/[a-zA-Z]/.test(next) && /[0-9]/.test(next));
  const canSubmit = current && next.length >= 8 && !mismatch && !tooWeak;

  async function onSubmit() {
    setSaving(true);
    try {
      const result = await api.post<{ ok: boolean; revokedSessions: number }>(
        '/api/auth/password',
        { currentPassword: current, newPassword: next },
      );
      toast.success(
        '密码已更新',
        result.revokedSessions > 0
          ? `同时登录的其它 ${result.revokedSessions} 个会话已被登出`
          : '当前会话保持登录状态',
      );
      setCurrent('');
      setNext('');
      setConfirm('');
    } catch (err) {
      toast.error('修改失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card className="p-4">
      <SectionHeader
        title="账号"
        description="修改后其它设备上的登录会立即失效，当前浏览器保持登录"
        actions={session && <Badge tone="brand">{session.username}</Badge>}
      />

      <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <Field label="当前密码">
          <Input
            type="password"
            value={current}
            onChange={(event) => setCurrent(event.target.value)}
            autoComplete="current-password"
          />
        </Field>
        <Field
          label="新密码"
          hint="至少 8 位"
          error={tooWeak ? '需要同时包含字母和数字' : null}
        >
          <Input
            type="password"
            value={next}
            onChange={(event) => setNext(event.target.value)}
            autoComplete="new-password"
            invalid={tooWeak}
          />
        </Field>
        <Field label="确认新密码" error={mismatch ? '两次输入不一致' : null}>
          <Input
            type="password"
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
            autoComplete="new-password"
            invalid={mismatch}
          />
        </Field>
      </div>

      <div className="mt-4 flex justify-end">
        <Button variant="primary" onClick={() => void onSubmit()} loading={saving} disabled={!canSubmit}>
          修改密码
        </Button>
      </div>
    </Card>
  );
}

function ToggleRow({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-3 rounded-xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] px-3.5 py-3">
      <div className="min-w-0">
        <p className="text-xs font-medium">{label}</p>
        <p className="mt-0.5 text-2xs leading-relaxed text-[var(--color-fg-subtle)]">{hint}</p>
      </div>
      <Switch checked={checked} onChange={onChange} label={label} />
    </div>
  );
}
