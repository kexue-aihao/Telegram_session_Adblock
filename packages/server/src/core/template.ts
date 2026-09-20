import { escapeMarkdown } from './text.ts';

/**
 * 文案模板渲染。
 *
 * 模板全部来自面板配置，替换值全部来自 Telegram 用户 —— 也就是说
 * `{name}` 里塞什么完全由对方决定。因此这里做两件事：
 *   1. 变量值一律走 `escapeMarkdown`，昵称里的 `**` / `` ` `` / `[` 不会
 *      把我们的消息解析成意料之外的格式，更不会让发送直接 400 失败；
 *   2. 未提供的变量渲染成空串而不是原样保留 `{foo}`，避免把内部变量名
 *      泄露给终端用户。
 *
 * 模板本身**不转义** —— 它就是写给 Markdown 看的（`**加粗**`、`` `代码` ``）。
 */

export type TemplateVars = Record<string, string | number | null | undefined>;

const PLACEHOLDER = /\{(\w+)\}/g;

export function renderTemplate(template: string, vars: TemplateVars): string {
  if (!template) return '';
  return template.replace(PLACEHOLDER, (_match, name: string) => {
    const value = vars[name];
    if (value === null || value === undefined) return '';
    return escapeMarkdown(String(value));
  });
}

/**
 * 展示用的用户名：`@alice`，没有用户名时回落到昵称。
 * 供模板变量 `{username}` 使用 —— 空字符串比 `@null` 体面得多。
 */
export function displayUsername(username: string | null | undefined): string {
  return username ? `@${username}` : '';
}

/** 中文全名；姓与名之间不加空格，符合中文习惯（英文名两侧本来就是分开的两段） */
export function displayName(contact: {
  firstName?: string | null;
  lastName?: string | null;
  username?: string | null;
}): string {
  const full = [contact.firstName, contact.lastName].filter(Boolean).join(' ').trim();
  if (full) return full;
  if (contact.username) return contact.username;
  return '未知用户';
}

/** 禁言解禁时间；`null` 表示永久 */
export function formatUntil(expiresAt: number | null): string {
  if (expiresAt === null) return '永久';
  return new Date(expiresAt).toLocaleString('zh-CN', {
    timeZone: process.env.TZ ?? 'Asia/Shanghai',
    hour12: false,
  });
}
