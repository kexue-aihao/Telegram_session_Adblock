import type { AuditLogEntry, Bot, RuleHit, StatsOverview, Timeseries } from '@tgs/shared';
import { useMemo } from 'react';
import { Link } from 'react-router';
import { api } from '../../lib/api.ts';
import { compactNumber, formatDelta, relativeTime } from '../../lib/format.ts';
import { useAsync, useTicker, useWsEvent } from '../../lib/hooks.ts';
import {
  AnimatedNumber,
  Sparkline,
  Stagger,
  StaggerItem,
} from '../../components/motion/index.tsx';
import {
  Badge,
  Card,
  EmptyState,
  ErrorState,
  Mono,
  Page,
  Row,
  SectionHeader,
  Skeleton,
  StatusDot,
} from '../../components/ui/primitives.tsx';
import { IconBot, IconRules, IconSessions, IconWarning } from '../../components/ui/icons.tsx';

/**
 * 仪表盘。
 *
 * 三块内容按「一眼能看出今天有没有出事」排序：KPI 磁贴 → 消息量趋势 →
 * 最近命中与机器人状态。运维打开面板的第一个问题永远是「有没有异常」，
 * 而不是「帮我做个数据分析」。
 */
export function DashboardPage() {
  const ticker = useTicker(20_000);

  const overview = useAsync<StatsOverview>(() => api.get('/api/stats/overview'), [ticker]);
  const series = useAsync<Timeseries>(() => api.get('/api/stats/timeseries', { days: 14 }), []);
  const bots = useAsync<{ items: Bot[] }>(() => api.get('/api/bots'), []);
  const hits = useAsync<{ items: RuleHit[] }>(
    () => api.get('/api/audit/hits', { limit: 6 }),
    [ticker],
  );
  const audit = useAsync<{ items: AuditLogEntry[] }>(
    () => api.get('/api/audit/log', { limit: 6 }),
    [ticker],
  );

  // 实时刷新：命中与新审计会随时推过来，仪表盘不该等下一次轮询
  useWsEvent('rule.hit', () => hits.reload());
  useWsEvent('audit.new', () => audit.reload());
  useWsEvent('bot.status', () => {
    void bots.reload();
    void overview.reload();
  });
  useWsEvent('stats.tick', (payload) => overview.setData(() => payload));

  const sparkPoints = useMemo(
    () => (series.data?.points ?? []).map((point, index) => ({ x: index, y: point.messagesIn })),
    [series.data],
  );

  if (overview.error) {
    return (
      <Page>
        <ErrorState message={overview.error} onRetry={overview.reload} />
      </Page>
    );
  }

  return (
    <Page>
      {/* ── KPI 磁贴 ───────────────────────────────────────── */}
      <Stagger className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StaggerItem index={0}>
          <StatTile
            label="今日消息"
            value={overview.data?.messages.inToday ?? 0}
            delta={overview.data?.deltas.messagesIn ?? null}
            hint={`累计 ${compactNumber(overview.data?.messages.total ?? 0)} 条`}
            loading={overview.loading && !overview.data}
          />
        </StaggerItem>
        <StaggerItem index={1}>
          <StatTile
            label="今日拦截广告"
            value={overview.data?.ads.blockedToday ?? 0}
            delta={overview.data?.deltas.adsBlocked ?? null}
            hint={`近 24 小时 ${overview.data?.ads.blocked24h ?? 0} 条`}
            tone="danger"
            loading={overview.loading && !overview.data}
          />
        </StaggerItem>
        <StaggerItem index={2}>
          <StatTile
            label="进行中的会话"
            value={overview.data?.sessions.open ?? 0}
            delta={overview.data?.deltas.sessionsCreated ?? null}
            hint={`今日新增 ${overview.data?.sessions.createdToday ?? 0} 个`}
            loading={overview.loading && !overview.data}
          />
        </StaggerItem>
        <StaggerItem index={3}>
          <StatTile
            label="在线机器人"
            value={overview.data?.bots.online ?? 0}
            delta={null}
            hint={`共 ${overview.data?.bots.total ?? 0} 个${(overview.data?.bots.error ?? 0) > 0 ? ` · ${overview.data?.bots.error} 个异常` : ''}`}
            tone={(overview.data?.bots.error ?? 0) > 0 ? 'danger' : 'success'}
            loading={overview.loading && !overview.data}
          />
        </StaggerItem>
      </Stagger>

      {/* ── 趋势 + 规则概况 ─────────────────────────────────── */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-[1fr_320px]">
        <Card className="p-4">
          <SectionHeader
            title="近 14 天消息量"
            description="用户私聊进入话题的中继条数"
          />
          <div className="mt-4">
            {series.loading && !series.data ? (
              <Skeleton className="h-24 w-full" />
            ) : sparkPoints.length < 2 ? (
              <EmptyState
                compact
                title="还没有足够的数据"
                description="面板运行满两天后这里会显示趋势曲线。"
              />
            ) : (
              <div className="flex items-end justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex items-baseline gap-2">
                    <span className="text-3xl font-semibold tracking-tight tabular">
                      {compactNumber(
                        (series.data?.points ?? []).reduce((sum, p) => sum + p.messagesIn, 0),
                      )}
                    </span>
                    <span className="text-xs text-[var(--color-fg-subtle)]">条 / 14 天</span>
                  </div>
                  <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                    其中拦截广告{' '}
                    <span className="font-medium text-[var(--color-danger)]">
                      {(series.data?.points ?? []).reduce((sum, p) => sum + p.adsBlocked, 0)}
                    </span>{' '}
                    条
                  </p>
                </div>
                <Sparkline
                  points={sparkPoints}
                  width={280}
                  height={64}
                  className="shrink-0"
                />
              </div>
            )}
          </div>
        </Card>

        <Card className="p-4">
          <SectionHeader title="规则引擎" />
          <div className="mt-4 space-y-2.5">
            <RuleStat label="规则总数" value={overview.data?.rules.total ?? 0} />
            <RuleStat label="已启用" value={overview.data?.rules.enabled ?? 0} tone="success" />
            <RuleStat
              label="因超时被停用"
              value={overview.data?.rules.autoDisabled ?? 0}
              tone={(overview.data?.rules.autoDisabled ?? 0) > 0 ? 'danger' : 'neutral'}
            />
          </div>
          {(overview.data?.rules.autoDisabled ?? 0) > 0 && (
            <Link
              to="/rules"
              className="mt-3 flex items-center gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-2 text-2xs text-[var(--color-danger)] transition-opacity duration-[var(--duration-micro)] hover:opacity-80"
            >
              <IconWarning className="size-3.5" />
              有规则因正则超时被自动停用，请检查
            </Link>
          )}
        </Card>
      </div>

      {/* ── 机器人状态 + 最近命中 ───────────────────────────── */}
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Card className="overflow-hidden">
          <div className="px-4 pt-4">
            <SectionHeader
              title="机器人"
              actions={
                <Link
                  to="/bots"
                  className="text-xs text-[var(--color-brand-strong)] hover:underline"
                >
                  管理
                </Link>
              }
            />
          </div>
          <div className="mt-3">
            {bots.loading && !bots.data ? (
              <div className="space-y-2 p-4">
                <Skeleton className="h-9 w-full" />
                <Skeleton className="h-9 w-full" />
              </div>
            ) : (bots.data?.items.length ?? 0) === 0 ? (
              <EmptyState
                icon={<IconBot />}
                compact
                title="还没有添加机器人"
                description="用 @BotFather 申请一个 token，然后在「机器人」页粘贴进来。"
              />
            ) : (
              bots.data?.items.map((bot) => <BotRow key={bot.id} bot={bot} />)
            )}
          </div>
        </Card>

        <Card className="overflow-hidden">
          <div className="px-4 pt-4">
            <SectionHeader
              title="最近的广告拦截"
              actions={
                <Link
                  to="/audit"
                  className="text-xs text-[var(--color-brand-strong)] hover:underline"
                >
                  全部
                </Link>
              }
            />
          </div>
          <div className="mt-3">
            {hits.loading && !hits.data ? (
              <div className="space-y-2 p-4">
                <Skeleton className="h-9 w-full" />
                <Skeleton className="h-9 w-full" />
              </div>
            ) : (hits.data?.items.length ?? 0) === 0 ? (
              <EmptyState
                icon={<IconRules />}
                compact
                title="暂无拦截记录"
                description="规则命中后会在这里实时出现。"
              />
            ) : (
              hits.data?.items.map((hit, index) => (
                <Row key={hit.id} className={index === 0 ? 'sweep-in' : undefined}>
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 space-y-0.5">
                      <p className="truncate text-sm font-medium">{hit.ruleName}</p>
                      <p className="truncate text-xs text-[var(--color-fg-muted)]">
                        {hit.contactName}
                        {hit.contactUsername && ` · @${hit.contactUsername}`}
                      </p>
                    </div>
                    <span className="shrink-0 text-2xs text-[var(--color-fg-subtle)]">
                      {relativeTime(hit.createdAt)}
                    </span>
                  </div>
                  {hit.matchedText && (
                    <Mono className="mt-1.5 block truncate text-[var(--color-danger)]">
                      {hit.matchedText}
                    </Mono>
                  )}
                </Row>
              ))
            )}
          </div>
        </Card>
      </div>

      {/* ── 面板操作审计 ───────────────────────────────────── */}
      <Card className="overflow-hidden">
        <div className="px-4 pt-4">
          <SectionHeader title="最近的操作" description="谁在什么时候改了什么" />
        </div>
        <div className="mt-3">
          {(audit.data?.items.length ?? 0) === 0 ? (
            <EmptyState icon={<IconSessions />} compact title="暂无操作记录" />
          ) : (
            audit.data?.items.map((entry) => (
              <Row key={entry.id}>
                <div className="flex items-center justify-between gap-3">
                  <div className="flex min-w-0 items-center gap-2.5">
                    <Badge tone={entry.actorType === 'admin' ? 'brand' : 'neutral'}>
                      {entry.actorId ?? entry.actorType}
                    </Badge>
                    <Mono className="truncate text-[var(--color-fg-muted)]">{entry.action}</Mono>
                    {entry.targetId && (
                      <span className="text-2xs text-[var(--color-fg-subtle)]">
                        → {entry.targetType} #{entry.targetId}
                      </span>
                    )}
                  </div>
                  <span className="shrink-0 text-2xs text-[var(--color-fg-subtle)]">
                    {relativeTime(entry.createdAt)}
                  </span>
                </div>
              </Row>
            ))
          )}
        </div>
      </Card>
    </Page>
  );
}

// ────────────────────────────── 子组件 ──────────────────────────────

function StatTile({
  label,
  value,
  delta,
  hint,
  tone = 'neutral',
  loading,
}: {
  label: string;
  value: number;
  delta: number | null;
  hint?: string;
  tone?: 'neutral' | 'success' | 'danger';
  loading?: boolean;
}) {
  const formatted = formatDelta(delta);
  const toneClass = {
    neutral: 'text-[var(--color-fg)]',
    success: 'text-[var(--color-success)]',
    danger: 'text-[var(--color-danger)]',
  }[tone];

  return (
    <Card className="p-4">
      <p className="text-xs text-[var(--color-fg-muted)]">{label}</p>

      <div className="mt-2 flex items-baseline gap-2">
        {loading ? (
          <Skeleton className="h-8 w-16" />
        ) : (
          <AnimatedNumber
            value={value}
            format={(v) => compactNumber(Math.round(v))}
            className={`text-2xl font-semibold tracking-tight ${toneClass}`}
          />
        )}

        {delta !== null && (
          <span
            className={`text-2xs font-medium ${
              formatted.tone === 'up'
                ? 'text-[var(--color-success)]'
                : formatted.tone === 'down'
                  ? 'text-[var(--color-danger)]'
                  : 'text-[var(--color-fg-subtle)]'
            }`}
          >
            {formatted.text}
          </span>
        )}
      </div>

      {hint && <p className="mt-1 text-2xs text-[var(--color-fg-subtle)]">{hint}</p>}
    </Card>
  );
}

function RuleStat({
  label,
  value,
  tone = 'neutral',
}: {
  label: string;
  value: number;
  tone?: 'neutral' | 'success' | 'danger';
}) {
  const toneClass = {
    neutral: 'text-[var(--color-fg)]',
    success: 'text-[var(--color-success)]',
    danger: 'text-[var(--color-danger)]',
  }[tone];

  return (
    <div className="flex items-center justify-between">
      <span className="text-xs text-[var(--color-fg-muted)]">{label}</span>
      <span className={`text-sm font-medium tabular ${toneClass}`}>{value}</span>
    </div>
  );
}

function BotRow({ bot }: { bot: Bot }) {
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

  return (
    <Row>
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <StatusDot tone={tone} pulse={bot.healthStatus === 'starting'} />
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{bot.name}</p>
            <Mono className="text-[var(--color-fg-subtle)]">@{bot.username}</Mono>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {!bot.isEnabled && <Badge tone="neutral">已停用</Badge>}
          <Badge tone={tone}>{labels[bot.healthStatus]}</Badge>
        </div>
      </div>
      {bot.lastError && (
        <p className="mt-1 truncate text-2xs text-[var(--color-danger)]">{bot.lastError}</p>
      )}
    </Row>
  );
}
