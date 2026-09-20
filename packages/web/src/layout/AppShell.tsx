import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router';
import { cn } from '../lib/cn.ts';
import { useConnectionState } from '../lib/hooks.ts';
import { PageTransition, DURATION, SPRING_SOFT } from '../components/motion/index.tsx';
import { Button, StatusDot } from '../components/ui/primitives.tsx';
import {
  IconAudit,
  IconBot,
  IconCommand,
  IconDashboard,
  IconLogout,
  IconMoon,
  IconRules,
  IconSearch,
  IconSessions,
  IconSettings,
  IconSun,
} from '../components/ui/icons.tsx';
import { useAuth } from '../auth.tsx';

/**
 * 应用外壳：常驻侧边栏 + 顶栏 + 内容区。
 *
 * 侧边栏刻意**不参与路由切换的卸载/挂载** —— 它在 layout route 里，
 * 页面切换时只有内容区做淡入上浮。侧边栏跟着闪一下会立刻暴露出
 * 「这是一个多页应用」而不是「一个工具」，那正是廉价感的来源。
 */

interface NavItem {
  to: string;
  label: string;
  icon: ReactNode;
  /** 命令面板里的关键词，中英文都给，方便拼音搜索 */
  keywords: string;
}

const NAV_ITEMS: NavItem[] = [
  { to: '/', label: '仪表盘', icon: <IconDashboard />, keywords: 'dashboard 概览 首页 overview' },
  { to: '/bots', label: '机器人', icon: <IconBot />, keywords: 'bots telegram 令牌 token' },
  { to: '/sessions', label: '会话', icon: <IconSessions />, keywords: 'sessions 话题 topics 私聊' },
  { to: '/rules', label: '规则', icon: <IconRules />, keywords: 'rules 正则 regex 广告 ad' },
  { to: '/audit', label: '审计', icon: <IconAudit />, keywords: 'audit 日志 命中 hits' },
  { to: '/settings', label: '设置', icon: <IconSettings />, keywords: 'settings 配置 密码' },
];

export function AppShell() {
  const location = useLocation();
  const navigate = useNavigate();
  const { session, logout } = useAuth();
  const connection = useConnectionState();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [theme, setTheme] = useState<'dark' | 'light'>(() =>
    document.documentElement.classList.contains('light') ? 'light' : 'dark',
  );

  // ⌘K / Ctrl+K 打开命令面板
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setPaletteOpen((open) => !open);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    document.documentElement.classList.toggle('dark', next === 'dark');
    document.documentElement.classList.toggle('light', next === 'light');
    try {
      localStorage.setItem('tgs.theme', next);
    } catch {
      // 隐私模式下 localStorage 可能不可用，主题仅在本次会话生效即可
    }
  };

  const current = NAV_ITEMS.find(
    (item) => item.to === location.pathname || (item.to !== '/' && location.pathname.startsWith(item.to)),
  );

  return (
    <div className="flex h-full">
      <Sidebar onOpenPalette={() => setPaletteOpen(true)} />

      <div className="flex min-w-0 flex-1 flex-col">
        {/* 顶栏用玻璃质感：内容滚动到它下面时能透出一点，边界因此显得柔和 */}
        <header className="glass sticky top-0 z-30 flex h-14 shrink-0 items-center justify-between gap-4 border-b border-[var(--color-line-subtle)] px-5">
          <div className="flex min-w-0 items-center gap-3">
            <h1 className="truncate text-base font-semibold">{current?.label ?? '面板'}</h1>
          </div>

          <div className="flex shrink-0 items-center gap-1.5">
            {/* 命令面板入口；同时也是 ⌘K 的可发现性提示 */}
            <button
              type="button"
              onClick={() => setPaletteOpen(true)}
              className="group flex h-8 items-center gap-2 rounded-lg border border-[var(--color-line-subtle)] bg-[var(--color-bg-2)] pr-2 pl-2.5 text-xs text-[var(--color-fg-subtle)] transition-colors duration-[var(--duration-micro)] hover:border-[var(--color-line)] hover:text-[var(--color-fg-muted)]"
            >
              <IconSearch className="size-3.5" />
              <span className="hidden sm:inline">搜索</span>
              <kbd className="hidden rounded border border-[var(--color-line-subtle)] px-1 font-mono text-2xs sm:inline">
                ⌘K
              </kbd>
            </button>

            <ConnectionBadge state={connection} />

            <Button variant="ghost" size="icon" onClick={toggleTheme} aria-label="切换主题">
              {theme === 'dark' ? <IconSun className="size-4" /> : <IconMoon className="size-4" />}
            </Button>

            <div className="mx-1 h-4 w-px bg-[var(--color-line-subtle)]" />

            <span className="hidden text-xs text-[var(--color-fg-muted)] sm:inline">
              {session?.username ?? ''}
            </span>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => void logout()}
              aria-label="退出登录"
            >
              <IconLogout className="size-4" />
            </Button>
          </div>
        </header>

        <main className="min-h-0 flex-1 overflow-y-auto">
          <PageTransition routeKey={location.pathname}>
            <Outlet />
          </PageTransition>
        </main>
      </div>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        items={NAV_ITEMS}
        onSelect={(to) => {
          setPaletteOpen(false);
          navigate(to);
        }}
      />
    </div>
  );
}

// ────────────────────────────── 侧边栏 ──────────────────────────────

function Sidebar({ onOpenPalette }: { onOpenPalette: () => void }) {
  return (
    <aside className="flex w-[220px] shrink-0 flex-col border-r border-[var(--color-line-subtle)] bg-[var(--color-bg-1)]">
      <div className="flex h-14 shrink-0 items-center gap-2.5 px-4">
        <div className="flex size-7 items-center justify-center rounded-lg bg-gradient-to-br from-[var(--color-brand-strong)] to-[var(--color-accent)] shadow-[inset_0_1px_0_rgb(255_255_255/0.25)]">
          <svg viewBox="0 0 24 24" className="size-4 text-white" fill="none" aria-hidden="true">
            <path
              d="M20.5 12.5a7.5 7.5 0 0 1-10.9 6.7L4 20.5l1.4-5.3A7.5 7.5 0 1 1 20.5 12.5Z"
              stroke="currentColor"
              strokeWidth="1.8"
              strokeLinejoin="round"
            />
          </svg>
        </div>
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold tracking-tight">会话中继</p>
          <p className="truncate text-2xs text-[var(--color-fg-subtle)]">Telegram Adblock</p>
        </div>
      </div>

      <nav className="flex-1 space-y-0.5 px-2.5 py-2">
        {NAV_ITEMS.map((item) => (
          <NavLink key={item.to} to={item.to} end={item.to === '/'}>
            {({ isActive }) => (
              <span
                className={cn(
                  'relative flex h-8.5 items-center gap-2.5 rounded-lg px-2.5 text-sm transition-colors duration-[var(--duration-micro)] ease-[var(--ease-state)]',
                  isActive
                    ? 'bg-[var(--color-surface-active)] font-medium text-[var(--color-fg)]'
                    : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg)]',
                )}
              >
                {/*
                  选中态用左侧一条 2px 的品牌色指示条，并用 layoutId 让它
                  在切换时平滑滑动 —— 这是侧边栏里最有辨识度的一处细节。
                */}
                {isActive && (
                  <motion.span
                    layoutId="nav-indicator"
                    className="absolute top-1/2 left-0 h-4 w-0.5 -translate-y-1/2 rounded-full bg-[var(--color-brand)]"
                    transition={{ type: 'spring', stiffness: 500, damping: 40 }}
                  />
                )}
                <span className={isActive ? 'text-[var(--color-brand-strong)]' : undefined}>
                  {item.icon}
                </span>
                {item.label}
              </span>
            )}
          </NavLink>
        ))}
      </nav>

      <div className="border-t border-[var(--color-line-subtle)] p-2.5">
        <button
          type="button"
          onClick={onOpenPalette}
          className="flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-xs text-[var(--color-fg-subtle)] transition-colors duration-[var(--duration-micro)] hover:bg-[var(--color-surface-hover)] hover:text-[var(--color-fg-muted)]"
        >
          <IconCommand className="size-3.5" />
          命令面板
        </button>
      </div>
    </aside>
  );
}

// ────────────────────────────── 连接状态 ──────────────────────────────

function ConnectionBadge({ state }: { state: 'connecting' | 'open' | 'closed' }) {
  const labels = {
    open: '实时连接正常',
    connecting: '正在连接…',
    closed: '连接已断开，正在重连',
  } as const;

  const tones = { open: 'success', connecting: 'warn', closed: 'danger' } as const;

  return (
    <span
      className="flex items-center gap-1.5 rounded-full border border-[var(--color-line-subtle)] px-2 py-1 text-2xs text-[var(--color-fg-muted)]"
      title={labels[state]}
    >
      <StatusDot tone={tones[state]} pulse={state !== 'open'} />
      <span className="hidden md:inline">{labels[state]}</span>
    </span>
  );
}

// ────────────────────────────── 命令面板 ──────────────────────────────

function CommandPalette({
  open,
  onClose,
  items,
  onSelect,
}: {
  open: boolean;
  onClose: () => void;
  items: NavItem[];
  onSelect: (to: string) => void;
}) {
  const [query, setQuery] = useState('');
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const reduced = useReducedMotion();

  const results = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    if (!keyword) return items;
    return items.filter(
      (item) =>
        item.label.toLowerCase().includes(keyword) ||
        item.keywords.toLowerCase().includes(keyword),
    );
  }, [items, query]);

  // 每次打开都重置状态，否则会保留上次的搜索词与高亮位置
  useEffect(() => {
    if (open) {
      setQuery('');
      setActiveIndex(0);
      // 等入场动画开始后再聚焦，避免浏览器在元素还在位移时滚动它
      window.setTimeout(() => inputRef.current?.focus(), 40);
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        onClose();
        return;
      }
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        setActiveIndex((index) => Math.min(index + 1, results.length - 1));
        return;
      }
      if (event.key === 'ArrowUp') {
        event.preventDefault();
        setActiveIndex((index) => Math.max(index - 1, 0));
        return;
      }
      if (event.key === 'Enter') {
        const target = results[activeIndex];
        if (target) {
          event.preventDefault();
          onSelect(target.to);
        }
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open, results, activeIndex, onSelect, onClose]);

  return (
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            className="fixed inset-0 z-40 bg-black/50 backdrop-blur-[2px]"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: DURATION.state }}
            onClick={onClose}
          />
          <div className="fixed inset-x-0 top-[18vh] z-50 flex justify-center px-4">
            <motion.div
              role="dialog"
              aria-modal="true"
              aria-label="命令面板"
              initial={reduced ? { opacity: 0 } : { opacity: 0, scale: 0.96, y: -6 }}
              animate={{ opacity: 1, scale: 1, y: 0 }}
              exit={reduced ? { opacity: 0 } : { opacity: 0, scale: 0.97 }}
              transition={reduced ? { duration: DURATION.micro } : { type: 'spring', ...SPRING_SOFT }}
              className="glass w-full max-w-md overflow-hidden rounded-2xl border border-[var(--color-line)] shadow-2xl"
            >
              <div className="flex items-center gap-2.5 border-b border-[var(--color-line-subtle)] px-3.5">
                <IconSearch className="size-4 shrink-0 text-[var(--color-fg-subtle)]" />
                <input
                  ref={inputRef}
                  value={query}
                  onChange={(event) => {
                    setQuery(event.target.value);
                    setActiveIndex(0);
                  }}
                  placeholder="跳转到…"
                  className="h-11 flex-1 bg-transparent text-sm text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)] focus:outline-none"
                />
                <kbd className="rounded border border-[var(--color-line-subtle)] px-1.5 py-0.5 font-mono text-2xs text-[var(--color-fg-subtle)]">
                  Esc
                </kbd>
              </div>

              <div className="max-h-72 overflow-y-auto p-1.5">
                {results.length === 0 ? (
                  <p className="px-3 py-6 text-center text-xs text-[var(--color-fg-subtle)]">
                    没有匹配的结果
                  </p>
                ) : (
                  results.map((item, index) => (
                    <motion.button
                      key={item.to}
                      type="button"
                      onClick={() => onSelect(item.to)}
                      onMouseEnter={() => setActiveIndex(index)}
                      // 结果行错峰入场：从 scale 起点展开，配合容器的 spring
                      initial={reduced ? false : { opacity: 0, y: 4 }}
                      animate={{ opacity: 1, y: 0 }}
                      transition={{ delay: reduced ? 0 : index * 0.025, duration: DURATION.state }}
                      className={cn(
                        'flex w-full items-center gap-2.5 rounded-xl px-2.5 py-2 text-left text-sm transition-colors duration-[var(--duration-micro)]',
                        index === activeIndex
                          ? 'bg-[var(--color-surface-active)] text-[var(--color-fg)]'
                          : 'text-[var(--color-fg-muted)]',
                      )}
                    >
                      <span className="text-[var(--color-fg-subtle)]">{item.icon}</span>
                      <span className="flex-1">{item.label}</span>
                      <span className="text-2xs text-[var(--color-fg-faint)]">{item.to}</span>
                    </motion.button>
                  ))
                )}
              </div>
            </motion.div>
          </div>
        </>
      )}
    </AnimatePresence>
  );
}
