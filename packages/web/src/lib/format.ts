/**
 * 展示层格式化。
 *
 * 全部集中在这里的原因很实际：同一个时间戳在仪表盘、审计列表、会话详情里
 * 各格式化一次，三处的格式迟早会不一致 —— 而「3 分钟前」和
 * 「2026-09-20 23:19」并排出现时，面板会显得很业余。
 */

/** 相对时间：刚刚 / 3 分钟前 / 2 小时前 / 昨天 14:30 / 09-18 14:30 */
export function relativeTime(ts: number | null | undefined): string {
  if (!ts) return '—';
  const diff = Date.now() - ts;

  if (diff < 0) return formatDateTime(ts);
  if (diff < 30_000) return '刚刚';
  if (diff < 60_000) return `${Math.floor(diff / 1000)} 秒前`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  if (diff < 172_800_000) return `昨天 ${formatTime(ts)}`;
  return formatShortDate(ts);
}

export function formatDateTime(ts: number | null | undefined): string {
  if (!ts) return '—';
  return new Date(ts).toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  });
}

export function formatShortDate(ts: number): string {
  return new Date(ts).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

export function formatTime(ts: number): string {
  return new Date(ts).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

/** 倒计时文案：用于禁言解禁时间 */
export function untilText(ts: number | null): string {
  if (ts === null) return '永久';
  const diff = ts - Date.now();
  if (diff <= 0) return '已结束';
  if (diff < 3_600_000) return `${Math.ceil(diff / 60_000)} 分钟后`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时后`;
  return `${Math.floor(diff / 86_400_000)} 天后`;
}

/** 大数字缩写：1234 → 1.2k，避免 KPI 磁贴被长数字撑破 */
export function compactNumber(value: number): string {
  if (value < 1000) return String(value);
  if (value < 1_000_000) return `${(value / 1000).toFixed(value < 10_000 ? 1 : 0)}k`;
  return `${(value / 1_000_000).toFixed(1)}M`;
}

/** 环比变化：+12% / -3% / null（无法比较） */
export function formatDelta(value: number | null): { text: string; tone: 'up' | 'down' | 'flat' } {
  if (value === null) return { text: '—', tone: 'flat' };
  if (value === 0) return { text: '持平', tone: 'flat' };
  return {
    text: `${value > 0 ? '+' : ''}${value}%`,
    tone: value > 0 ? 'up' : 'down',
  };
}

/** 把秒数说成人话 */
export function formatDuration(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds < 3600) return `${Math.ceil(seconds / 60)} 分钟`;
  return `${Math.ceil(seconds / 3600)} 小时`;
}

/** Telegram 数字 ID 的展示：带负号的群 ID 很长，等宽字体 + 不换行 */
export function formatChatId(id: number | null): string {
  if (id === null) return '未绑定';
  return String(id);
}

/** 命中片段的高亮切分：返回 [前缀, 命中, 后缀] 三段，供规则沙盒高亮渲染 */
export function splitHighlight(text: string, matches: { start: number; end: number }[]): {
  text: string;
  hit: boolean;
}[] {
  if (matches.length === 0) return [{ text, hit: false }];

  const sorted = [...matches].sort((a, b) => a.start - b.start);
  const parts: { text: string; hit: boolean }[] = [];
  let cursor = 0;

  for (const match of sorted) {
    // 相邻或重叠的命中要合并，否则会切出一堆空片段
    if (match.start < cursor) continue;
    if (match.start > cursor) parts.push({ text: text.slice(cursor, match.start), hit: false });
    parts.push({ text: text.slice(match.start, match.end), hit: true });
    cursor = match.end;
  }

  if (cursor < text.length) parts.push({ text: text.slice(cursor), hit: false });
  return parts;
}

/** 与 shared 里的 ContentType 对应的中文标签 */
export const CONTENT_TYPE_LABELS: Record<string, string> = {
  text: '文本',
  photo: '图片',
  video: '视频',
  document: '文件',
  audio: '音频',
  voice: '语音',
  sticker: '贴纸',
  animation: '动图',
  video_note: '视频留言',
  location: '位置',
  contact: '联系人',
  poll: '投票',
  dice: '骰子',
  unknown: '其他',
};
