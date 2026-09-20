import type { AuditLogEntry, Bot, RuleHit } from '@tgs/shared';
import { AnimatePresence, motion } from 'motion/react';
import { useState } from 'react';
import { api } from '../../lib/api.ts';
import { formatDateTime, relativeTime } from '../../lib/format.ts';
import { useAsync, useDebounced, useTicker, useWsEvent } from '../../lib/hooks.ts';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Input,
  Mono,
  Page,
  Row,
  SectionHeader,
  Select,
  Skeleton,
} from '../../components/ui/primitives.tsx';
import { IconAudit, IconChevronDown, IconWarning } from '../../components/ui/icons.tsx';

/**
 * 审计页。
 *
 * 两个标签：「广告命中」是机器人的工作日志（量极大，重在可筛选与可解释），
 * 「面板操作」是谁改了什么（量小，重在可追溯）。分成两个标签而不是一张表，
 * 是因为两者的查询方式与关注点完全不同。
 */

const OUTCOME_LABELS: Record<string, string> = {
  deleted: '已删除',
  delete_failed: '删除失败',
  warned: '已警告',
  silenced: '已静默',
  muted: '已禁言',
  banned: '已拉黑',
  notified: '已通知管理员',
  notify_failed: '通知失败',
  logged_only: '仅记录',
  regex_timeout: '规则超时',
};

export function AuditPage() {
  const [tab, setTab] = useState<'hits' | 'log'>('hits');

  return (
    <Page>
      <SectionHeader
        title="审计"
        description="命中记录保存了规则的完整快照，因此即使规则后来被改或被删，这里依然能解释「当时是按什么判定的」。"
      />

      <div className="flex items-center gap-1 border-b border-[var(--color-line-subtle)]">
        <TabButton active={tab === 'hits'} onClick={() => setTab('hits')}>
          广告命中
        </TabButton>
        <TabButton active={tab === 'log'} onClick={() => setTab('log')}>
          面板操作
        </TabButton>
      </div>

      {tab === 'hits' ? <HitsTab /> : <LogTab />}
    </Page>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`relative px-3.5 py-2.5 text-sm transition-colors duration-[var(--duration-micro)] ${
        active ? 'text-[var(--color-fg)]' : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]'
      }`}
    >
      {children}
      {/*
        标签下的指示条用 layoutId 在两者之间平滑滑动 —— 与侧边栏
        用的是同一套语言，界面因此显得是一个整体而不是拼起来的。
      */}
      {active && (
        <motion.span
          layoutId="audit-tab"
          className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-[var(--color-brand)]"
          transition={{ type: 'spring', stiffness: 500, damping: 40 }}
        />
      )}
    </button>
  );
}

// ────────────────────────────── 命中标签页 ──────────────────────────────

function HitsTab() {
  const ticker = useTicker(20_000);
  const [query, setQuery] = useState('');
  const [botId, setBotId] = useState<number | undefined>(undefined);
  const debouncedQuery = useDebounced(query, 300);

  const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'), []);

  const hits = useAsync<{ items: RuleHit[]; nextCursor: number | null; total: number | null }>(
    () =>
      api.get('/api/audit/hits', {
        q: debouncedQuery || undefined,
        botId,
        limit: 30,
      }),
    [debouncedQuery, botId, ticker],
  );

  // 新的命中从顶部弹入；它是这个页面存在的理由，必须第一时间可见
  useWsEvent('rule.hit', () => hits.reload());

  const items = hits.data?.items ?? [];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="搜索命中内容、规则名或用户名"
          className="h-8 max-w-xs text-xs"
        />
        <Select
          value={botId ?? ''}
          onChange={(event) =>
            setBotId(event.target.value ? Number.parseInt(event.target.value, 10) : undefined)
          }
          className="h-8 w-44 text-xs"
        >
          <option value="">全部机器人</option>
          {bots.data?.items.map((bot) => (
            <option key={bot.id} value={bot.id}>
              {bot.name}
            </option>
          ))}
        </Select>

        <span className="ml-auto text-2xs text-[var(--color-fg-subtle)]">
          共 {hits.data?.total ?? 0} 条命中
        </span>
      </div>

      {hits.error ? (
        <ErrorState message={hits.error} onRetry={hits.reload} />
      ) : hits.loading && !hits.data ? (
        <div className="space-y-2">
          {[0, 1, 2, 3, 4].map((index) => (
            <Skeleton key={index} className="h-20 rounded-2xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <Card>
          <EmptyState
            icon={<IconAudit />}
            title={query ? '没有匹配的命中记录' : '还没有命中记录'}
            description={
              query
                ? '换个关键词或清空筛选试试。'
                : '当有用户发送触发规则的内容时，这里会实时出现记录。'
            }
          />
        </Card>
      ) : (
        <div className="space-y-2">
          {items.map((hit, index) => (
            <HitCard key={hit.id} hit={hit} fresh={index === 0} />
          ))}

          {hits.data?.nextCursor && (
            <div className="flex justify-center pt-2">
              <Button
                variant="secondary"
                size="sm"
                onClick={async () => {
                  const next = await api.get<{
                    items: RuleHit[];
                    nextCursor: number | null;
                    total: number | null;
                  }>('/api/audit/hits', {
                    cursor: hits.data?.nextCursor ?? undefined,
                    q: debouncedQuery || undefined,
                    botId,
                    limit: 30,
                  });
                  hits.setData((current) =>
                    current
                      ? {
                          items: [...current.items, ...next.items],
                          nextCursor: next.nextCursor,
                          total: current.total,
                        }
                      : next,
                  );
                }}
              >
                加载更多
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function HitCard({ hit, fresh }: { hit: RuleHit; fresh: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const hasProblems = hit.outcomes.includes('delete_failed') || hit.outcomes.includes('regex_timeout');

  return (
    <Card className={fresh ? 'sweep-in' : undefined}>
      <button
        type="button"
        onClick={() => setExpanded((value) => !value)}
        className="flex w-full items-start justify-between gap-3 p-3.5 text-left"
      >
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">{hit.ruleName}</span>
            {hit.outcomes.map((outcome) => (
              <Badge
                key={outcome}
                tone={
                  outcome === 'delete_failed' || outcome === 'regex_timeout' || outcome === 'notify_failed'
                    ? 'danger'
                    : outcome === 'logged_only'
                      ? 'neutral'
                      : 'success'
                }
              >
                {OUTCOME_LABELS[outcome] ?? outcome}
              </Badge>
            ))}
            {hit.severity > 0 && <Badge tone="warn">+{hit.severity} 分</Badge>}
          </div>

          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-[var(--color-fg-subtle)]">
            <span>
              {hit.contactName}
              {hit.contactUsername && ` (@${hit.contactUsername})`}
            </span>
            <span>{hit.botName}</span>
            <span>{relativeTime(hit.createdAt)}</span>
          </div>

          {hit.matchedText && (
            <Mono className="block truncate rounded-lg bg-[var(--color-bg-2)] px-2 py-1.5 text-[var(--color-danger)]">
              {hit.matchedText}
            </Mono>
          )}
        </div>

        <motion.span
          animate={{ rotate: expanded ? 180 : 0 }}
          transition={{ duration: 0.2 }}
          className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]"
        >
          <IconChevronDown className="size-4" />
        </motion.span>
      </button>

      <AnimatePresence initial={false}>
        {expanded && (
          <motion.div
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: 'auto', opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.32, ease: [0.16, 1, 0.3, 1] }}
            className="overflow-hidden"
          >
            <div className="space-y-2.5 border-t border-[var(--color-line-subtle)] px-3.5 py-3">
              <DetailRow label="规则快照">
                <Mono className="text-[var(--color-fg-muted)]">
                  /{hit.rulePattern}/{hit.ruleFlags}
                </Mono>
              </DetailRow>

              {hit.normalizedExcerpt && (
                <DetailRow label="归一化上下文">
                  <Mono className="text-[var(--color-fg-muted)]">{hit.normalizedExcerpt}</Mono>
                </DetailRow>
              )}

              <DetailRow label="用户">
                <span className="text-xs text-[var(--color-fg-muted)]">
                  {hit.contactName} · TG ID <Mono>{hit.tgUserId}</Mono>
                </span>
              </DetailRow>

              {hasProblems && (
                <p className="flex items-start gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs leading-relaxed text-[var(--color-danger)]">
                  <IconWarning className="mt-px size-3.5 shrink-0" />
                  {hit.outcomes.includes('regex_timeout')
                    ? '这条规则在匹配时超时，已被引擎自动停用。请到「规则」页改写它 —— 常见原因是嵌套量词导致灾难性回溯。'
                    : '删除消息失败。通常是超过 Telegram 的 48 小时删除窗口，或机器人缺少「删除消息」权限。'}
                </p>
              )}

              <DetailRow label="发生时间">
                <span className="text-xs text-[var(--color-fg-muted)]">
                  {formatDateTime(hit.createdAt)}
                </span>
              </DetailRow>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </Card>
  );
}

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[92px_1fr] gap-3">
      <span className="text-2xs text-[var(--color-fg-subtle)]">{label}</span>
      <div className="min-w-0 break-words">{children}</div>
    </div>
  );
}

// ────────────────────────────── 操作日志标签页 ──────────────────────────────

function LogTab() {
  const [query, setQuery] = useState('');
  const [action, setAction] = useState('');
  const debouncedQuery = useDebounced(query, 300);

  const logs = useAsync<{ items: AuditLogEntry[]; nextCursor: number | null; total: number | null }>(
    () => api.get('/api/audit/log', { q: debouncedQuery || undefined, action: action || undefined, limit: 50 }),
    [debouncedQuery, action],
  );

  const actions = useAsync<{ items: string[] }>(() => api.get('/api/audit/actions'), []);
  useWsEvent('audit.new', () => logs.reload());

  const items = logs.data?.items ?? [];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="搜索操作、操作者或目标"
          className="h-8 max-w-xs text-xs"
        />
        <Select
          value={action}
          onChange={(event) => setAction(event.target.value)}
          className="h-8 w-52 text-xs"
        >
          <option value="">全部操作</option>
          {actions.data?.items.map((item) => (
            <option key={item} value={item}>
              {item}
            </option>
          ))}
        </Select>
        <span className="ml-auto text-2xs text-[var(--color-fg-subtle)]">
          共 {logs.data?.total ?? 0} 条记录
        </span>
      </div>

      <Card className="overflow-hidden">
        {logs.loading && !logs.data ? (
          <div className="space-y-2 p-4">
            {[0, 1, 2, 3].map((index) => (
              <Skeleton key={index} className="h-10" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <EmptyState
            icon={<IconAudit />}
            compact
            title="没有匹配的操作记录"
            description="登录、修改规则、拉黑用户等操作都会记录在这里。"
          />
        ) : (
          items.map((entry) => (
            <Row key={entry.id}>
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge
                      tone={
                        entry.actorType === 'admin'
                          ? 'brand'
                          : entry.actorType === 'system'
                            ? 'neutral'
                            : 'info'
                      }
                    >
                      {entry.actorId ?? entry.actorType}
                    </Badge>
                    <Mono className="text-[var(--color-fg)]">{entry.action}</Mono>
                    {entry.targetType && (
                      <span className="text-2xs text-[var(--color-fg-subtle)]">
                        {entry.targetType}
                        {entry.targetId ? ` #${entry.targetId}` : ''}
                      </span>
                    )}
                  </div>

                  {entry.detail && Object.keys(entry.detail).length > 0 && (
                    <Mono className="block truncate text-[var(--color-fg-subtle)]">
                      {JSON.stringify(entry.detail)}
                    </Mono>
                  )}
                </div>

                <div className="shrink-0 text-right">
                  <p className="text-2xs text-[var(--color-fg-subtle)]">
                    {relativeTime(entry.createdAt)}
                  </p>
                  {entry.ip && (
                    <Mono className="text-[var(--color-fg-faint)]">{entry.ip}</Mono>
                  )}
                </div>
              </div>
            </Row>
          ))
        )}
      </Card>
    </div>
  );
}
