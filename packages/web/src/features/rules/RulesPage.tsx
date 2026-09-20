import type { AdRule, RuleTestResult } from '@tgs/shared';
import { motion } from 'motion/react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { api, ApiError } from '../../lib/api.ts';
import { relativeTime, splitHighlight } from '../../lib/format.ts';
import { useAsync, useDebounced } from '../../lib/hooks.ts';
import { DURATION, EASE_OUT_EXPO } from '../../components/motion/index.tsx';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  Input,
  Mono,
  Select,
  Switch,
  Textarea,
} from '../../components/ui/primitives.tsx';
import { Drawer, Modal } from '../../components/ui/overlay.tsx';
import { useToast } from '../../components/ui/toast.tsx';
import {
  IconCheck,
  IconEdit,
  IconPlus,
  IconRules,
  IconTrash,
  IconWarning,
} from '../../components/ui/icons.tsx';

/**
 * 规则页。
 *
 * 列表本身很普通，**正则测试沙盒**才是这个页面的核心价值：正则的写法与
 * 匹配结果之间的关系对多数运维并不直观，让他们「保存后再看线上效果」
 * 等于拿真实用户做实验。沙盒直接复用服务端引擎的匹配函数，
 * 因此所见即运行时所得。
 */

export const MATCH_MODE_LABELS = {
  regex: '正则表达式',
  contains: '包含文本',
  whole_word: '整词匹配',
} as const;

export const TARGET_LABELS = {
  text: '正文',
  caption: '媒体说明文字',
  text_link: '隐藏链接（text_link）',
  url: '显式链接',
  mention: '@ 提及',
  forward: '转发来源',
  all: '全部（正文 + 链接 + 提及）',
} as const;

export const ACTION_LABELS = {
  delete: '删除消息',
  warn: '私聊警告',
  escalate: '阶梯处罚',
  notify: '通知管理员',
} as const;

export function RulesPage() {
  const toast = useToast();
  const [editing, setEditing] = useState<AdRule | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AdRule | null>(null);
  const [deleting, setDeleting] = useState(false);

  const rules = useAsync<{ items: AdRule[] }>(() => api.get('/api/rules'), []);

  const grouped = useMemo(() => {
    const items = rules.data?.items ?? [];
    return {
      system: items.filter((rule) => rule.isSystem),
      custom: items.filter((rule) => !rule.isSystem),
    };
  }, [rules.data]);

  async function toggle(rule: AdRule, next: boolean) {
    // 乐观更新：开关的状态切换必须立刻可见，等一次网络往返会让它显得迟钝
    rules.setData((current) =>
      current
        ? { ...current, items: current.items.map((r) => (r.id === rule.id ? { ...r, isEnabled: next } : r)) }
        : current,
    );

    try {
      await api.post(`/api/rules/${rule.id}/toggle`, { isEnabled: next });
    } catch (err) {
      // 失败就回滚，否则界面会显示一个与服务端不符的状态
      rules.setData((current) =>
        current
          ? {
              ...current,
              items: current.items.map((r) => (r.id === rule.id ? { ...r, isEnabled: !next } : r)),
            }
          : current,
      );
      toast.error('切换失败', err instanceof ApiError ? err.message : '未知错误');
    }
  }

  async function onDelete() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.delete(`/api/rules/${deleteTarget.id}`);
      toast.success('规则已删除', '历史命中记录仍然保留');
      setDeleteTarget(null);
      rules.reload();
    } catch (err) {
      toast.error('删除失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setDeleting(false);
    }
  }

  return (
    <div className="mx-auto w-full max-w-[1200px] space-y-5 p-5 lg:p-6">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-0.5">
          <h2 className="text-lg">广告拦截规则</h2>
          <p className="text-xs text-[var(--color-fg-muted)]">
            消息在转发进话题之前先经过规则引擎；命中即拦截，不会留下能被截图的时间窗。
            规则按优先级从小到大依次匹配，命中多条时动作取并集。
          </p>
        </div>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <IconPlus className="size-4" />
          新建规则
        </Button>
      </div>

      {rules.loading && !rules.data ? (
        <div className="space-y-2">
          {[0, 1, 2].map((index) => (
            <div key={index} className="skeleton h-20 rounded-2xl" />
          ))}
        </div>
      ) : (
        <>
          <RuleGroup
            title="自定义规则"
            description="你自己添加的规则，优先级与启停都可以自由调整。"
            rules={grouped.custom}
            onToggle={toggle}
            onEdit={setEditing}
            onDelete={setDeleteTarget}
            onCreate={() => setCreating(true)}
          />

          <RuleGroup
            title="预置规则"
            description="系统自带的基础规则，可以停用或修改，但不建议删除 —— 删掉后重新安装会再次出现。"
            rules={grouped.system}
            onToggle={toggle}
            onEdit={setEditing}
            onDelete={setDeleteTarget}
          />
        </>
      )}

      <RuleEditor
        open={creating || editing !== null}
        rule={editing}
        onClose={() => {
          setCreating(false);
          setEditing(null);
        }}
        onSaved={() => {
          setCreating(false);
          setEditing(null);
          rules.reload();
        }}
      />

      <ConfirmDialogWrapper
        target={deleteTarget}
        loading={deleting}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => void onDelete()}
      />
    </div>
  );
}

function RuleGroup({
  title,
  description,
  rules,
  onToggle,
  onEdit,
  onDelete,
  onCreate,
}: {
  title: string;
  description: string;
  rules: AdRule[];
  onToggle: (rule: AdRule, next: boolean) => void;
  onEdit: (rule: AdRule) => void;
  onDelete: (rule: AdRule) => void;
  onCreate?: () => void;
}) {
  return (
    <section className="space-y-2">
      <div className="space-y-0.5">
        <h3 className="text-sm font-medium">{title}</h3>
        <p className="text-2xs text-[var(--color-fg-subtle)]">{description}</p>
      </div>

      {rules.length === 0 ? (
        <Card>
          <EmptyState
            icon={<IconRules />}
            compact
            title="还没有自定义规则"
            description="预置规则已经能拦下大部分常见广告；再补充几条针对你自己业务的规则会更准。"
            action={
              onCreate && (
                <Button size="sm" variant="primary" onClick={onCreate}>
                  <IconPlus className="size-3.5" />
                  新建规则
                </Button>
              )
            }
          />
        </Card>
      ) : (
        <div className="space-y-2">
          {rules.map((rule) => (
            <RuleCard
              key={rule.id}
              rule={rule}
              onToggle={onToggle}
              onEdit={onEdit}
              onDelete={onDelete}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function RuleCard({
  rule,
  onToggle,
  onEdit,
  onDelete,
}: {
  rule: AdRule;
  onToggle: (rule: AdRule, next: boolean) => void;
  onEdit: (rule: AdRule) => void;
  onDelete: (rule: AdRule) => void;
}) {
  return (
    <Card
      className={`p-3.5 transition-opacity duration-[var(--duration-state)] ${
        rule.isEnabled ? '' : 'opacity-55'
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">{rule.name}</span>
            <Badge tone="neutral">
              {MATCH_MODE_LABELS[rule.matchMode]}
            </Badge>
            <Badge tone={rule.action === 'escalate' ? 'danger' : 'brand'}>
              {ACTION_LABELS[rule.action]}
            </Badge>
            <Badge tone="neutral">优先级 {rule.priority}</Badge>
            {rule.hitCount > 0 && <Badge tone="warn">命中 {rule.hitCount} 次</Badge>}
          </div>

          <Mono className="block truncate rounded-lg bg-[var(--color-bg-2)] px-2 py-1.5 text-[var(--color-fg-muted)]">
            /{rule.pattern}/{rule.flags}
          </Mono>

          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-[var(--color-fg-subtle)]">
            <span>目标：{TARGET_LABELS[rule.target]}</span>
            <span>违规分 +{rule.severity}</span>
            {rule.botId !== null && <span>仅对机器人 #{rule.botId} 生效</span>}
            {rule.hitCount > 0 && <span>最近命中 {relativeTime(rule.lastHitAt)}</span>}
          </div>

          {rule.note && (
            <p className="text-2xs leading-relaxed text-[var(--color-fg-subtle)]">{rule.note}</p>
          )}
        </div>

        <div className="flex shrink-0 flex-col items-end gap-2">
          <Switch
            checked={rule.isEnabled}
            onChange={(next) => onToggle(rule, next)}
            label={`启用规则 ${rule.name}`}
          />
          <div className="flex items-center gap-0.5">
            <Button size="sm" variant="ghost" onClick={() => onEdit(rule)} aria-label="编辑">
              <IconEdit className="size-3.5" />
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
              onClick={() => onDelete(rule)}
              aria-label="删除"
            >
              <IconTrash className="size-3.5" />
            </Button>
          </div>
        </div>
      </div>
    </Card>
  );
}

function ConfirmDialogWrapper({
  target,
  loading,
  onClose,
  onConfirm,
}: {
  target: AdRule | null;
  loading: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal
      open={target !== null}
      onClose={onClose}
      title="删除规则"
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={loading}>
            取消
          </Button>
          <Button variant="danger" onClick={onConfirm} loading={loading}>
            确认删除
          </Button>
        </>
      }
    >
      <p className="text-sm leading-relaxed text-[var(--color-fg-muted)]">
        将删除规则「{target?.name}」。
        <br />
        <br />
        已经产生的命中记录<strong className="text-[var(--color-fg)]">不会</strong>被删除 ——
        审计里保存了规则的完整快照，因此历史记录依然能解释「当时是按什么判定的」。
      </p>
    </Modal>
  );
}

// ══════════════════════════ 规则编辑器 ══════════════════════════

interface RuleDraft {
  name: string;
  pattern: string;
  flags: string;
  matchMode: AdRule['matchMode'];
  target: AdRule['target'];
  action: AdRule['action'];
  severity: number;
  priority: number;
  isEnabled: boolean;
  note: string;
}

const EMPTY_DRAFT: RuleDraft = {
  name: '',
  pattern: '',
  flags: 'iu',
  matchMode: 'regex',
  target: 'all',
  action: 'delete',
  severity: 10,
  priority: 100,
  isEnabled: true,
  note: '',
};

function RuleEditor({
  open,
  rule,
  onClose,
  onSaved,
}: {
  open: boolean;
  rule: AdRule | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [draft, setDraft] = useState<RuleDraft>(EMPTY_DRAFT);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setDraft(
      rule
        ? {
            name: rule.name,
            pattern: rule.pattern,
            flags: rule.flags,
            matchMode: rule.matchMode,
            target: rule.target,
            action: rule.action,
            severity: rule.severity,
            priority: rule.priority,
            isEnabled: rule.isEnabled,
            note: rule.note ?? '',
          }
        : EMPTY_DRAFT,
    );
  }, [open, rule]);

  async function onSave() {
    setSaving(true);
    const payload = {
      name: draft.name.trim(),
      pattern: draft.pattern,
      flags: draft.flags,
      matchMode: draft.matchMode,
      target: draft.target,
      action: draft.action,
      severity: draft.severity,
      priority: draft.priority,
      isEnabled: draft.isEnabled,
      note: draft.note.trim() || null,
    };

    try {
      if (rule) {
        await api.patch(`/api/rules/${rule.id}`, payload);
        toast.success('规则已更新');
      } else {
        await api.post('/api/rules', payload);
        toast.success('规则已创建');
      }
      onSaved();
    } catch (err) {
      toast.error('保存失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={rule ? '编辑规则' : '新建规则'}
      width={620}
    >
      <div className="space-y-5">
        <Field label="规则名称" hint="给自己看的，写清楚它拦的是什么">
          <Input
            value={draft.name}
            onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            placeholder="例如：游戏代练广告"
          />
        </Field>

        <RegexSandbox draft={draft} onChange={(patch) => setDraft({ ...draft, ...patch })} />

        <div className="grid grid-cols-2 gap-3">
          <Field label="匹配方式">
            <Select
              value={draft.matchMode}
              onChange={(event) =>
                setDraft({ ...draft, matchMode: event.target.value as RuleDraft['matchMode'] })
              }
            >
              {Object.entries(MATCH_MODE_LABELS).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="匹配目标" hint="选择消息的哪一部分参与匹配">
            <Select
              value={draft.target}
              onChange={(event) =>
                setDraft({ ...draft, target: event.target.value as RuleDraft['target'] })
              }
            >
              {Object.entries(TARGET_LABELS).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          </Field>
        </div>

        <div className="grid grid-cols-3 gap-3">
          <Field label="命中动作">
            <Select
              value={draft.action}
              onChange={(event) =>
                setDraft({ ...draft, action: event.target.value as RuleDraft['action'] })
              }
            >
              {Object.entries(ACTION_LABELS).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </Select>
          </Field>

          <Field label="违规分" hint="累加到阶梯处罚">
            <Input
              type="number"
              min={0}
              max={1000}
              value={draft.severity}
              onChange={(event) =>
                setDraft({ ...draft, severity: Number.parseInt(event.target.value, 10) || 0 })
              }
            />
          </Field>

          <Field label="优先级" hint="越小越先匹配">
            <Input
              type="number"
              min={0}
              max={10000}
              value={draft.priority}
              onChange={(event) =>
                setDraft({ ...draft, priority: Number.parseInt(event.target.value, 10) || 0 })
              }
            />
          </Field>
        </div>

        <Field label="备注" hint="可选">
          <Textarea
            value={draft.note}
            onChange={(event) => setDraft({ ...draft, note: event.target.value })}
            rows={2}
            placeholder="记录这条规则的来历，方便日后回头改"
          />
        </Field>

        <div className="flex items-center justify-between rounded-xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] px-3.5 py-3">
          <div>
            <p className="text-xs font-medium">启用这条规则</p>
            <p className="text-2xs text-[var(--color-fg-subtle)]">
              停用的规则不参与匹配，但会保留全部配置
            </p>
          </div>
          <Switch
            checked={draft.isEnabled}
            onChange={(next) => setDraft({ ...draft, isEnabled: next })}
            label="启用规则"
          />
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-[var(--color-line-subtle)] pt-4">
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button
            variant="primary"
            onClick={() => void onSave()}
            loading={saving}
            disabled={!draft.name.trim() || !draft.pattern}
          >
            {rule ? '保存修改' : '创建规则'}
          </Button>
        </div>
      </div>
    </Drawer>
  );
}

/**
 * 正则测试沙盒。
 *
 * 三个设计要点：
 *  1. 输入防抖 400ms 后自动跑一次，不需要点「测试」按钮 ——
 *     调正则是一个反复试错的过程，每改一次都点一下按钮会非常烦。
 *  2. 命中片段在原文里**高亮**，而不是只列出匹配到的字符串 ——
 *     看到它在上下文里的位置才判断得出这条规则会不会误伤。
 *  3. 同时显示归一化后的文本。管理员最常问的就是「我写的是加微信，
 *     为什么『加<零宽>微<零宽>信』也被拦了」，把归一化结果摆出来
 *     这个问题就不言自明。
 */
function RegexSandbox({
  draft,
  onChange,
}: {
  draft: RuleDraft;
  onChange: (patch: Partial<RuleDraft>) => void;
}) {
  const [sample, setSample] = useState('你好，加微信详聊 vx: abc123，或访问 https://t.me/example');
  const [result, setResult] = useState<RuleTestResult | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const debouncedPattern = useDebounced(draft.pattern, 400);
  const debouncedSample = useDebounced(sample, 400);
  const requestId = useRef(0);

  useEffect(() => {
    if (!debouncedPattern) {
      setResult(null);
      setError(null);
      return;
    }

    const current = ++requestId.current;
    setRunning(true);

    void (async () => {
      try {
        const response = await api.post<RuleTestResult>('/api/rules/test', {
          pattern: debouncedPattern,
          flags: draft.flags,
          matchMode: draft.matchMode,
          sample: debouncedSample,
        });
        // 丢弃过期响应：慢请求回来时覆盖掉新结果会让高亮跳到旧位置
        if (current !== requestId.current) return;
        setResult(response);
        setError(null);
      } catch (err) {
        if (current !== requestId.current) return;
        setResult(null);
        setError(err instanceof ApiError ? err.message : '测试失败');
      } finally {
        if (current === requestId.current) setRunning(false);
      }
    })();
  }, [debouncedPattern, debouncedSample, draft.flags, draft.matchMode]);

  const highlighted = useMemo(() => {
    if (!result || result.matches.length === 0) return null;
    return splitHighlight(result.normalizedSample ?? sample, result.matches);
  }, [result, sample]);

  const syntaxError = error !== null;

  return (
    <section className="space-y-3 rounded-2xl border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] p-3.5">
      <div className="flex items-center justify-between gap-3">
        <div>
          <p className="text-xs font-medium">测试沙盒</p>
          <p className="text-2xs text-[var(--color-fg-subtle)]">
            与线上完全相同的匹配引擎与归一化流程
          </p>
        </div>
        {running && (
          <span className="text-2xs text-[var(--color-fg-subtle)]">匹配中…</span>
        )}
        {!running && result && (
          <Badge tone={result.matches.length > 0 ? 'success' : 'neutral'}>
            {result.matches.length > 0
              ? `命中 ${result.matches.length} 处 · ${result.durationMs}ms`
              : '未命中'}
          </Badge>
        )}
      </div>

      <div className="flex gap-2">
        <div className="relative flex-1">
          <span className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 font-mono text-xs text-[var(--color-fg-faint)]">
            /
          </span>
          <Input
            value={draft.pattern}
            onChange={(event) => onChange({ pattern: event.target.value })}
            placeholder="加\s*(微信|vx|QQ)"
            spellCheck={false}
            className="pr-10 pl-5 font-mono text-xs"
            invalid={syntaxError}
          />
          <span className="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 font-mono text-xs text-[var(--color-fg-faint)]">
            /{draft.flags}
          </span>
        </div>

        <Input
          value={draft.flags}
          onChange={(event) => onChange({ flags: event.target.value.replace(/[^gimsuy]/g, '') })}
          className="w-20 text-center font-mono text-xs"
          spellCheck={false}
          aria-label="正则标志"
          title="允许的标志：g i m s u y"
        />
      </div>

      {(syntaxError || result?.timedOut) && (
        <motion.p
          initial={{ opacity: 0, y: -2 }}
          animate={{ opacity: 1, y: 0 }}
          className="flex items-start gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]"
        >
          <IconWarning className="mt-px size-3.5 shrink-0" />
          {syntaxError
            ? error
            : '匹配超时 —— 这条正则可能造成灾难性回溯，保存时会被拒绝。请改写为更具体的模式。'}
        </motion.p>
      )}

      <div className="space-y-1.5">
        <label className="text-2xs font-medium text-[var(--color-fg-muted)]">样本文本</label>
        <Textarea
          value={sample}
          onChange={(event) => setSample(event.target.value)}
          rows={3}
          spellCheck={false}
          className="text-xs"
          placeholder="粘贴一段真实的广告文案，看这条规则会不会命中它"
        />
      </div>

      {highlighted && (
        <motion.div
          initial={{ opacity: 0, y: 4 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: DURATION.state, ease: EASE_OUT_EXPO }}
          className="space-y-2"
        >
          <div className="space-y-1">
            <p className="text-2xs font-medium text-[var(--color-fg-muted)]">
              归一化后的文本（规则实际匹配的就是它）
            </p>
            <p className="rounded-lg bg-[var(--color-bg-1)] px-2.5 py-2 text-xs leading-relaxed break-words">
              {highlighted.map((part, index) =>
                part.hit ? (
                  <mark
                    key={index}
                    className="rounded bg-[var(--color-danger-soft)] px-0.5 text-[var(--color-danger)]"
                  >
                    {part.text}
                  </mark>
                ) : (
                  <span key={index} className="text-[var(--color-fg-muted)]">
                    {part.text}
                  </span>
                ),
              )}
            </p>
          </div>

          {result && result.matches.some((match) => match.groups.length > 0) && (
            <div className="space-y-1">
              <p className="text-2xs font-medium text-[var(--color-fg-muted)]">捕获组</p>
              <div className="space-y-1">
                {result.matches.slice(0, 5).map((match, index) => (
                  <div key={index} className="flex flex-wrap items-center gap-1.5">
                    <Mono className="text-[var(--color-fg-subtle)]">#{index + 1}</Mono>
                    {match.groups.map((group, groupIndex) =>
                      group === null ? null : (
                        <Badge key={groupIndex} tone="brand">
                          ${groupIndex + 1} = {group}
                        </Badge>
                      ),
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          <p className="flex items-center gap-1.5 text-2xs text-[var(--color-success)]">
            <IconCheck className="size-3.5" />
            这条规则通过了静态安全检查，可以保存
          </p>
        </motion.div>
      )}

      {result && result.matches.length === 0 && !syntaxError && !result.timedOut && draft.pattern && (
        <p className="text-2xs text-[var(--color-fg-subtle)]">
          这条规则没有命中上面的样本。如果样本里确实有该拦的内容，可以放宽模式
          —— 比如用 <Mono className="text-[var(--color-fg-muted)]">\s*</Mono> 允许词之间夹空格。
        </p>
      )}
    </section>
  );
}
