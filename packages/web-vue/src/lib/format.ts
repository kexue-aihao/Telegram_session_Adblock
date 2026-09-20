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

/** 环比变化 */
export function formatDelta(value: number | null): { text: string; tone: 'up' | 'down' | 'flat' } {
  if (value === null) return { text: '—', tone: 'flat' };
  if (value === 0) return { text: '持平', tone: 'flat' };
  return { text: `${value > 0 ? '+' : ''}${value}%`, tone: value > 0 ? 'up' : 'down' };
}

/** 命中片段的高亮切分：返回可逐段渲染的数组 */
export function splitHighlight(
  text: string,
  matches: { start: number; end: number }[],
): { text: string; hit: boolean }[] {
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

export const MATCH_MODE_LABELS: Record<string, string> = {
  regex: '正则表达式',
  contains: '包含文本',
  whole_word: '整词匹配',
};

export const TARGET_LABELS: Record<string, string> = {
  text: '正文',
  caption: '媒体说明文字',
  text_link: '隐藏链接（text_link）',
  url: '显式链接',
  mention: '@ 提及',
  forward: '转发来源',
  all: '全部（正文 + 链接 + 提及）',
};

export const ACTION_LABELS: Record<string, string> = {
  delete: '删除消息',
  warn: '私聊警告',
  escalate: '阶梯处罚',
  notify: '通知管理员',
};

export const OUTCOME_LABELS: Record<string, string> = {
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

export const SANCTION_LABELS: Record<string, string> = {
  warn: '已警告',
  silence: '已静默',
  mute: '已禁言',
  ban: '已拉黑',
};

export const HEALTH_LABELS: Record<string, string> = {
  online: '在线',
  starting: '启动中',
  error: '异常',
  stopped: '已停止',
  unknown: '未知',
};
