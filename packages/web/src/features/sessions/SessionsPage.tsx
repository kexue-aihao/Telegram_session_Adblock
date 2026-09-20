import type { RelayedMessage, SessionDetail, SessionSummary } from '@tgs/shared';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { api, ApiError } from '../../lib/api.ts';
import { CONTENT_TYPE_LABELS, relativeTime, untilText } from '../../lib/format.ts';
import { useAsync, useChannel, useDebounced, useTicker, useWsEvent } from '../../lib/hooks.ts';
import { DURATION, EASE_OUT_EXPO } from '../../components/motion/index.tsx';
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Input,
  Mono,
  Row,
  Skeleton,
} from '../../components/ui/primitives.tsx';
import { ConfirmDialog } from '../../components/ui/overlay.tsx';
import { useToast } from '../../components/ui/toast.tsx';
import {
  IconBan,
  IconSend,
  IconSessions,
  IconTrash,
  IconWarning,
} from '../../components/ui/icons.tsx';

/**
 * 会话页。
 *
 * 布局是「左列表 + 右聊天记录」，但两者共享同一个路由（`/sessions/:id`），
 * 所以刷新页面、分享链接都能直接落到某个具体会话上。
 *
 * 最值得注意的一处实现是**共享元素展开**：列表行被点击时，选中态的背景
 * 会以 `layoutId` 平滑地「变形」到右侧面板，而不是两处各自淡入淡出。
 * 这是整个面板里最有记忆点的一处流转，也是最容易做砸的一处 —— 用
 * layout 动画时两侧的元素必须在不同时刻存在，否则会同时出现两份。
 */
export function SessionsPage() {
  const { sessionId } = useParams<{ sessionId?: string }>();
  const navigate = useNavigate();
  const toast = useToast();
  const ticker = useTicker(30_000);

  const [query, setQuery] = useState('');
  const [flaggedOnly, setFlaggedOnly] = useState(false);
  const [status, setStatus] = useState<'open' | 'closed' | undefined>(undefined);
  const debouncedQuery = useDebounced(query, 300);

  const list = useAsync<{ items: SessionSummary[]; nextCursor: number | null; total: number | null }>(
    () =>
      api.get('/api/sessions', {
        q: debouncedQuery || undefined,
        status,
        flaggedOnly: flaggedOnly || undefined,
        limit: 50,
      }),
    [debouncedQuery, status, flaggedOnly, ticker],
  );

  // 会话列表是实时变化的：新建会话、新消息都要立刻反映出来
  useWsEvent('session.created', () => list.reload());
  useWsEvent('session.updated', (payload) => {
    list.setData((current) =>
      current
        ? {
            ...current,
            items: current.items.map((item) => (item.id === payload.id ? payload : item)),
          }
        : current,
    );
  });

  const selectedId = sessionId ? Number.parseInt(sessionId, 10) : null;
  const selected = list.data?.items.find((item) => item.id === selectedId) ?? null;

  return (
    <div className="flex h-full min-h-0">
      {/* ── 左：列表 ─────────────────────────────────────── */}
      <div className="flex w-[320px] shrink-0 flex-col border-r border-[var(--color-line-subtle)]">
        <div className="space-y-2 border-b border-[var(--color-line-subtle)] p-3">
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索昵称、用户名或话题标题"
            className="h-8 text-xs"
          />
          <div className="flex items-center gap-1.5">
            <FilterChip active={status === undefined} onClick={() => setStatus(undefined)}>
              全部
            </FilterChip>
            <FilterChip active={status === 'open'} onClick={() => setStatus('open')}>
              进行中
            </FilterChip>
            <FilterChip active={status === 'closed'} onClick={() => setStatus('closed')}>
              已关闭
            </FilterChip>
            <FilterChip active={flaggedOnly} onClick={() => setFlaggedOnly((v) => !v)}>
              有违规
            </FilterChip>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {list.loading && !list.data ? (
            <div className="space-y-2 p-3">
              {[0, 1, 2, 3, 4].map((index) => (
                <Skeleton key={index} className="h-14" />
              ))}
            </div>
          ) : list.error ? (
            <ErrorState message={list.error} onRetry={list.reload} />
          ) : (list.data?.items.length ?? 0) === 0 ? (
            <EmptyState
              icon={<IconSessions />}
              compact
              title={query ? '没有匹配的会话' : '还没有会话'}
              description={
                query
                  ? '换个关键词试试'
                  : '当有用户私聊机器人时，这里会自动为每个人建立一个话题。'
              }
            />
          ) : (
            list.data?.items.map((item) => (
              <SessionRow
                key={item.id}
                session={item}
                selected={item.id === selectedId}
                onSelect={() => navigate(`/sessions/${item.id}`)}
              />
            ))
          )}
        </div>
      </div>

      {/* ── 右：聊天记录 ─────────────────────────────────── */}
      <div className="flex min-w-0 flex-1 flex-col">
        <AnimatePresence mode="wait">
          {selectedId === null || !selected ? (
            <motion.div
              key="empty"
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: DURATION.state }}
              className="flex flex-1 items-center justify-center"
            >
              <EmptyState
                icon={<IconSessions />}
                title="选择一个会话"
                description="左侧列出了所有与你私聊过的用户。选中后可以查看完整往来并以管理员身份回复。"
              />
            </motion.div>
          ) : (
            <motion.div
              key="detail"
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0 }}
              transition={{ duration: DURATION.layout, ease: EASE_OUT_EXPO }}
              className="flex h-full min-h-0 flex-col"
            >
              <SessionDetailPane session={selected} onDeleted={() => navigate('/sessions')} />
            </motion.div>
          )}
        </AnimatePresence>
      </div>
    </div>
  );
}

// ────────────────────────────── 列表行 ──────────────────────────────

function SessionRow({
  session,
  selected,
  onSelect,
}: {
  session: SessionSummary;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <Row onClick={onSelect} selected={selected} className="relative">
      {/*
        共享元素：选中时这一块背景会「变形」到右侧面板。
        用 layoutId 而不是两边各自做入场动画，是因为前者能表达
        「同一个对象被展开了」这个语义，后者只是两个独立动画。
      */}
      {selected && (
        <motion.span
          layoutId="session-highlight"
          className="pointer-events-none absolute inset-0 bg-[var(--color-surface-active)]"
          transition={{ type: 'spring', stiffness: 500, damping: 40 }}
        />
      )}

      <div className="relative flex items-start gap-2.5">
        <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-3)] text-xs font-medium text-[var(--color-fg-muted)]">
          {(session.displayName[0] ?? '?').toUpperCase()}
        </div>

        <div className="min-w-0 flex-1">
          <div className="flex items-baseline justify-between gap-2">
            <span className="truncate text-sm font-medium">{session.displayName}</span>
            <span className="shrink-0 text-2xs text-[var(--color-fg-subtle)]">
              {relativeTime(session.lastMessageAt ?? session.createdAt)}
            </span>
          </div>

          <p className="mt-0.5 truncate text-xs text-[var(--color-fg-muted)]">
            {session.lastMessageDirection === 'admin_to_user' && (
              <span className="text-[var(--color-fg-subtle)]">你：</span>
            )}
            {session.lastMessagePreview ?? '（暂无消息）'}
          </p>

          <div className="mt-1 flex items-center gap-1.5">
            <Badge tone="neutral">{session.botName}</Badge>
            {session.violationScore > 0 && (
              <Badge tone="danger">违规 {session.violationScore}</Badge>
            )}
            {session.status === 'closed' && <Badge tone="neutral">已关闭</Badge>}
            {session.isBlocked && <Badge tone="danger">已拉黑</Badge>}
          </div>
        </div>
      </div>
    </Row>
  );
}

function FilterChip({
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
      className={`rounded-full px-2.5 py-1 text-2xs transition-colors duration-[var(--duration-micro)] ${
        active
          ? 'bg-[var(--color-brand-soft)] font-medium text-[var(--color-brand-strong)]'
          : 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg-muted)]'
      }`}
    >
      {children}
    </button>
  );
}

// ────────────────────────────── 详情面板 ──────────────────────────────

function SessionDetailPane({
  session,
  onDeleted,
}: {
  session: SessionSummary;
  onDeleted: () => void;
}) {
  const toast = useToast();
  const reduced = useReducedMotion();
  const scrollRef = useRef<HTMLDivElement>(null);

  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [busy, setBusy] = useState(false);

  const detail = useAsync<SessionDetail>(
    () => api.get(`/api/sessions/${session.id}`),
    [session.id],
  );

  const messages = useAsync<{ items: RelayedMessage[]; nextCursor: number | null }>(
    () => api.get(`/api/sessions/${session.id}/messages`, { limit: 50 }),
    [session.id],
  );

  /**
   * 滚动到最新一条。
   *
   * 容器是 `flex-col-reverse`，因此视觉上的「底部」对应 `scrollTop === 0`。
   * 用反向列而不是普通的「滚到 scrollHeight」，是因为前者在**上方插入**更早的
   * 消息时不会产生滚动跳变 —— 浏览器会保持当前视口内容不动。
   * 这正是聊天界面一直用它的原因。
   */
  const scrollToLatest = useCallback(
    (smooth: boolean) => {
      const element = scrollRef.current;
      if (!element) return;
      element.scrollTo({ top: 0, behavior: smooth && !reduced ? 'smooth' : 'auto' });
    },
    [reduced],
  );

  /** 用户是不是本来就贴着最新消息；用于决定新消息到达时要不要自动滚动 */
  const isAtLatest = useCallback(() => {
    const element = scrollRef.current;
    if (!element) return true;
    return element.scrollTop < 120;
  }, []);

  useEffect(() => {
    scrollToLatest(false);
  }, [session.id, scrollToLatest]);

  // 订阅这个话题的频道：只有订阅了才会收到它的新消息
  useChannel(`topic:${session.id}`);

  useWsEvent('message.new', (payload) => {
    if (payload.topicId !== session.id) return;

    // 先判断用户当前的位置，再改数据 —— 插入新节点之后 scrollTop 会被
    // 浏览器调整，那时再判断就已经不准了。
    const shouldFollow = isAtLatest();

    messages.setData((current) =>
      current ? { ...current, items: [payload, ...current.items] } : current,
    );

    // 只在用户本来就贴着最新消息时才自动滚动。否则会把正在翻历史的人
    // 强行拽回底部 —— 这是聊天界面里最招人烦的一类行为。
    if (shouldFollow) {
      // 等一帧让新节点完成布局，否则滚动的目标位置还是旧的
      requestAnimationFrame(() => scrollToLatest(true));
    }
  });

  useWsEvent('message.updated', (payload) => {
    if (payload.topicId !== session.id) return;
    messages.setData((current) =>
      current
        ? { ...current, items: current.items.map((m) => (m.id === payload.id ? payload : m)) }
        : current,
    );
  });

  useWsEvent('message.deleted', (payload) => {
    if (payload.topicId !== session.id) return;
    messages.setData((current) =>
      current
        ? {
            ...current,
            items: current.items.map((m) =>
              m.id === payload.messageId ? { ...m, isDeleted: true } : m,
            ),
          }
        : current,
    );
  });

  /**
   * 消息按 id 倒序存储（最新在前，便于游标分页），渲染前反转成
   * 「旧 → 新」的自然顺序。反转的是数组内容而不是靠 CSS 反向列，
   * 这样选中文本、复制、屏幕阅读器的顺序都是对的。
   */
  const ordered = useMemo(() => [...(messages.data?.items ?? [])].reverse(), [messages.data?.items]);

  async function onSend() {
    const text = draft.trim();
    if (!text || sending) return;

    setSending(true);
    try {
      await api.post(`/api/sessions/${session.id}/messages`, { text });
      setDraft('');
    } catch (err) {
      toast.error('发送失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setSending(false);
    }
  }

  async function onAction(action: 'close' | 'reopen' | 'ban' | 'unban' | 'reset') {
    setBusy(true);
    try {
      if (action === 'close' || action === 'reopen') {
        await api.post(`/api/sessions/${session.id}/${action}`);
        toast.success(action === 'close' ? '会话已关闭' : '会话已重新打开');
      } else {
        const endpoint =
          action === 'reset'
            ? `/api/contacts/${session.contactId}/reset-violations`
            : `/api/contacts/${session.contactId}/${action}`;
        await api.post(endpoint);
        toast.success(
          action === 'ban' ? '已拉黑该用户' : action === 'unban' ? '已解除拉黑' : '违规分已清零',
        );
      }
      detail.reload();
    } catch (err) {
      toast.error('操作失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setBusy(false);
    }
  }

  async function onDelete() {
    setBusy(true);
    try {
      await api.delete(`/api/sessions/${session.id}`);
      toast.success('会话已删除', 'Telegram 侧的话题也已一并删除');
      setConfirmDelete(false);
      onDeleted();
    } catch (err) {
      toast.error('删除失败', err instanceof ApiError ? err.message : '未知错误');
    } finally {
      setBusy(false);
    }
  }

  const activeSanctions = detail.data?.activeSanctions ?? [];

  return (
    <>
      {/* ── 头部：用户信息 + 操作 ───────────────────────── */}
      <header className="glass shrink-0 border-b border-[var(--color-line-subtle)] px-4 py-3">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="truncate text-base">{session.displayName}</h2>
              {session.username && (
                <Mono className="text-[var(--color-fg-subtle)]">@{session.username}</Mono>
              )}
              {session.isBlocked && <Badge tone="danger">已拉黑</Badge>}
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-[var(--color-fg-subtle)]">
              <span>
                TG ID <Mono className="text-[var(--color-fg-muted)]">{session.tgUserId}</Mono>
              </span>
              <span>话题 #{session.threadId}</span>
              <span>{session.messageCount} 条消息</span>
              <span>经由 {session.botName}</span>
              {session.violationScore > 0 && (
                <span className="text-[var(--color-danger)]">违规分 {session.violationScore}</span>
              )}
            </div>
          </div>

          <div className="flex shrink-0 items-center gap-1.5">
            {session.status === 'open' ? (
              <Button size="sm" variant="ghost" onClick={() => void onAction('close')} disabled={busy}>
                关闭
              </Button>
            ) : (
              <Button size="sm" variant="ghost" onClick={() => void onAction('reopen')} disabled={busy}>
                重开
              </Button>
            )}
            {session.isBlocked ? (
              <Button size="sm" variant="ghost" onClick={() => void onAction('unban')} disabled={busy}>
                解除拉黑
              </Button>
            ) : (
              <Button
                size="sm"
                variant="ghost"
                className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
                onClick={() => void onAction('ban')}
                disabled={busy}
              >
                <IconBan className="size-3.5" />
                拉黑
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]"
              onClick={() => setConfirmDelete(true)}
              disabled={busy}
              aria-label="删除会话"
            >
              <IconTrash className="size-3.5" />
            </Button>
          </div>
        </div>

        {/* 生效中的处罚横幅 */}
        {activeSanctions.length > 0 && (
          <motion.div
            initial={{ opacity: 0, height: 0 }}
            animate={{ opacity: 1, height: 'auto' }}
            className="mt-2.5 flex flex-wrap items-center gap-2"
          >
            {activeSanctions.map((sanction) => (
              <span
                key={sanction.id}
                className="flex items-center gap-1.5 rounded-lg bg-[var(--color-danger-soft)] px-2.5 py-1.5 text-2xs text-[var(--color-danger)]"
              >
                <IconWarning className="size-3.5" />
                {SANCTION_LABELS[sanction.type]}
                {sanction.expiresAt && ` · ${untilText(sanction.expiresAt)}解除`}
              </span>
            ))}
            <Button size="sm" variant="ghost" onClick={() => void onAction('reset')} disabled={busy}>
              清零违规分
            </Button>
          </motion.div>
        )}

        {/* 近期命中 */}
        {(detail.data?.recentHits.length ?? 0) > 0 && (
          <div className="mt-2 flex flex-wrap gap-1.5">
            {detail.data?.recentHits.slice(0, 3).map((hit) => (
              <span
                key={hit.id}
                className="rounded-full border border-[var(--color-line-subtle)] px-2 py-0.5 text-2xs text-[var(--color-fg-subtle)]"
                title={`${hit.matchedText} · ${hit.outcome}`}
              >
                {hit.ruleName}
              </span>
            ))}
          </div>
        )}
      </header>

      {/* ── 消息流 ─────────────────────────────────────── */}
      <div ref={scrollRef} className="flex min-h-0 flex-1 flex-col-reverse overflow-y-auto px-4 py-4">
        {messages.loading && !messages.data ? (
          <div className="space-y-3">
            {[0, 1, 2].map((index) => (
              <Skeleton key={index} className="h-12 w-2/3" />
            ))}
          </div>
        ) : (messages.data?.items.length ?? 0) === 0 ? (
          <EmptyState
            compact
            icon={<IconSessions />}
            title="还没有往来消息"
            description="在下方输入框里可以直接以管理员身份给对方发消息。"
          />
        ) : (
          <div className="space-y-2">
            {ordered.map((message) => (
              <MessageBubble key={message.id} message={message} />
            ))}

            {messages.data?.nextCursor && (
              <div className="flex justify-center py-2">
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={async () => {
                    const next = await api.get<{ items: RelayedMessage[]; nextCursor: number | null }>(
                      `/api/sessions/${session.id}/messages`,
                      { cursor: messages.data?.nextCursor ?? undefined, limit: 50 },
                    );
                    messages.setData((current) =>
                      current
                        ? { items: [...current.items, ...next.items], nextCursor: next.nextCursor }
                        : next,
                    );
                  }}
                >
                  加载更早的消息
                </Button>
              </div>
            )}
          </div>
        )}
      </div>

      {/* ── 输入区 ─────────────────────────────────────── */}
      <Composer
        value={draft}
        onChange={setDraft}
        onSend={() => void onSend()}
        sending={sending}
        disabled={session.isBlocked}
        disabledHint="该用户已被拉黑，解除后才能发送消息"
      />

      <ConfirmDialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        onConfirm={() => void onDelete()}
        loading={busy}
        danger
        title="删除会话"
        confirmLabel="确认删除"
        message={
          <>
            这会在 Telegram 里<strong className="text-[var(--color-fg)]">真实删除</strong>
            整个话题及其中的全部消息，本地记录也会标记为已删除。
            <br />
            <br />
            如果只是想停止接收该用户的消息，请改用「关闭」。
          </>
        }
      />
    </>
  );
}

const SANCTION_LABELS: Record<string, string> = {
  warn: '已警告',
  silence: '已静默',
  mute: '已禁言',
  ban: '已拉黑',
};

function MessageBubble({ message }: { message: RelayedMessage }) {
  const isAdmin = message.direction === 'admin_to_user';
  const text = message.content.text ?? message.content.caption;

  return (
    <div className={`flex ${isAdmin ? 'justify-end' : 'justify-start'}`}>
      <div
        className={`max-w-[75%] space-y-1 rounded-2xl px-3.5 py-2.5 ${
          isAdmin
            ? 'bg-[var(--color-brand)]/15 text-[var(--color-fg)]'
            : 'surface-2 text-[var(--color-fg)]'
        } ${message.isDeleted ? 'opacity-45' : ''}`}
      >
        <div className="flex items-center gap-2 text-2xs text-[var(--color-fg-subtle)]">
          <span>{isAdmin ? (message.senderLabel ?? '管理员') : '用户'}</span>
          <span>·</span>
          <span>{relativeTime(message.createdAt)}</span>
          {message.editedAt && <span className="italic">已编辑</span>}
          {message.isDeleted && <span className="text-[var(--color-danger)]">已删除</span>}
        </div>

        {text && (
          <p className="text-sm leading-relaxed break-words whitespace-pre-wrap">{text}</p>
        )}

        {/* 非文本内容用一条摘要表示；媒体不内联播放，避免面板加载大量文件 */}
        {message.content.type !== 'text' && (
          <div className="flex items-center gap-1.5 text-xs text-[var(--color-fg-muted)]">
            <span className="rounded-md bg-[var(--color-surface-active)] px-1.5 py-0.5">
              {CONTENT_TYPE_LABELS[message.content.type] ?? message.content.type}
            </span>
            {message.content.media.length > 1 && <span>×{message.content.media.length}</span>}
          </div>
        )}

        {message.content.hasHiddenLink && (
          <p className="flex items-center gap-1 text-2xs text-[var(--color-warn)]">
            <IconWarning className="size-3" />
            含隐藏链接
          </p>
        )}
      </div>
    </div>
  );
}

function Composer({
  value,
  onChange,
  onSend,
  sending,
  disabled,
  disabledHint,
}: {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
  sending: boolean;
  disabled: boolean;
  disabledHint: string;
}) {
  return (
    <div className="shrink-0 border-t border-[var(--color-line-subtle)] p-3">
      {disabled ? (
        <p className="rounded-xl bg-[var(--color-danger-soft)] px-3 py-2.5 text-xs text-[var(--color-danger)]">
          {disabledHint}
        </p>
      ) : (
        <div className="flex items-end gap-2">
          <textarea
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={(event) => {
              // Enter 发送、Shift+Enter 换行 —— 与所有聊天软件一致
              if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                onSend();
              }
            }}
            placeholder="以管理员身份回复…（Enter 发送，Shift+Enter 换行）"
            rows={1}
            className="max-h-40 min-h-9 flex-1 resize-none rounded-xl border border-[var(--color-line)] bg-[var(--color-bg-2)] px-3 py-2 text-sm text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)] transition-colors duration-[var(--duration-micro)] focus:border-[var(--color-brand)] focus:outline-none"
            style={{ fieldSizing: 'content' } as React.CSSProperties}
          />
          <Button
            variant="primary"
            onClick={onSend}
            loading={sending}
            disabled={!value.trim()}
            aria-label="发送"
          >
            <IconSend className="size-4" />
          </Button>
        </div>
      )}
    </div>
  );
}
