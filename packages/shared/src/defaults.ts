import type { EscalationConfig, EscalationStep } from './schemas/settings.ts';

/**
 * 默认阶梯处罚 —— 与方案里确认的四档一致：
 *   1 次命中 → 私聊警告
 *   3 次命中 → 静默（消息不再进话题）
 *   5 次命中 → 硬禁言 24 小时
 *   8 次命中 → 拉黑 + 关闭话题
 * 全部阈值与文案都可以在 WebUI 的「设置 → 阶梯处罚」里改。
 */
export const DEFAULT_ESCALATION: EscalationConfig = {
  decayDays: 7,
  maxScore: 999,
  steps: [
    {
      id: 'step-warn',
      atScore: 1,
      type: 'warn',
      durationMinutes: null,
      deleteMessage: true,
      notifyAdmins: false,
      enabled: true,
    },
    {
      id: 'step-silence',
      atScore: 3,
      type: 'silence',
      durationMinutes: null,
      deleteMessage: true,
      notifyAdmins: true,
      enabled: true,
    },
    {
      id: 'step-mute',
      atScore: 5,
      type: 'mute',
      durationMinutes: 1440, // 24 小时
      deleteMessage: true,
      notifyAdmins: true,
      enabled: true,
    },
    {
      id: 'step-ban',
      atScore: 8,
      type: 'ban',
      durationMinutes: null,
      deleteMessage: true,
      notifyAdmins: true,
      enabled: true,
    },
  ],
} satisfies EscalationConfig;

export const DEFAULT_TOPIC_NAME_TEMPLATE = '{name} · #{id}';
export const DEFAULT_TOPIC_ICON_COLOR = 0x6fb9f0;

/**
 * 文案模板可用的变量（WebUI 里会作为提示展示）：
 *   {name} {username} {id} {firstName} {lastName}
 *   {ruleName} {matched} {score} {until} {outcome} {botName} {reason}
 */
export const TEMPLATE_VARIABLES = [
  'name',
  'username',
  'id',
  'firstName',
  'lastName',
  'ruleName',
  'matched',
  'score',
  'until',
  'outcome',
  'botName',
  'reason',
] as const;
export type TemplateVariable = (typeof TEMPLATE_VARIABLES)[number];

/** 用户首次 /start 时发送的欢迎语 */
export const DEFAULT_GREETING_TEXT = `你好，{name}！👋

这里是与管理团队的私聊通道 —— 直接把你的问题发给我，管理员会在后台看到并回复你。

请勿发送广告、推广链接或垃圾信息，此类消息会被自动拦截并可能导致你被限制。`;

export const DEFAULT_WARN_TEMPLATE = `⚠️ **警告**

你发送的消息触发了规则「{ruleName}」，已被移除。
当前违规分：**{score}**

请遵守规则，继续违规将升级为禁言或拉黑。`;

export const DEFAULT_MUTE_TEMPLATE = `🔇 **你已被禁言**

原因：触发规则「{ruleName}」
解禁时间：**{until}**

禁言期间你的消息不会被送达管理员。`;

export const DEFAULT_BAN_TEMPLATE = `🚫 **你已被加入黑名单**

原因：多次触发广告拦截规则（「{ruleName}」）。
机器人不再接收你的消息。如有异议请通过其他渠道联系管理团队。`;

/** 静默处罚默认不告知用户 —— 静默的意义就在于让对方无感知 */
export const DEFAULT_SILENCE_TEMPLATE = '';

/** 命中后推送到管理群话题内的告警卡片 */
export const DEFAULT_ALERT_CARD_TEMPLATE = `🛡️ **广告拦截**

**用户**：{name}（\`#{id}\`）
**规则**：{ruleName}
**命中内容**：\`{matched}\`
**处置**：{outcome}
**违规分**：{score}`;

/** 话题创建后置顶的头部消息 */
export const DEFAULT_TOPIC_HEADER_TEMPLATE = `👤 **{name}**
🔗 {username}
🆔 \`{id}\`
🤖 经由 {botName} 转接`;

export interface SeedRule {
  name: string;
  pattern: string;
  flags: string;
  matchMode: 'regex' | 'contains' | 'whole_word';
  target: 'text' | 'caption' | 'text_link' | 'url' | 'mention' | 'forward' | 'all';
  action: 'delete' | 'warn' | 'escalate' | 'notify';
  severity: number;
  priority: number;
  enabled: boolean;
  isSystem: boolean;
  note: string;
}

/**
 * 预置规则 —— 首次启动时写入，管理员可随时改阈值、停用或删除。
 *
 * 刻意避开嵌套量词与前后瞻：这些种子规则必须能通过 `safe-regex`，
 * 否则等于给用户演示了一个「保存即被拒」的坏例子。
 */
export const SEED_RULES: SeedRule[] = [
  {
    name: 'Telegram 群组 / 频道引流链接',
    // `http://|https://` 展开写而不是 `https?://`：后者把 `s?` 套在 `(?:…)?` 里，
    // 构成嵌套量词，会被 safe-regex 判为风险模式而拒绝保存 —— 行为完全一样。
    pattern: String.raw`(?:http://|https://)?(?:t\.me|telegram\.me|telegram\.dog)/(?:joinchat/|\+)?[A-Za-z0-9_-]{5,}`,
    flags: 'iu',
    matchMode: 'regex',
    target: 'all',
    action: 'delete',
    severity: 15,
    priority: 10,
    enabled: true,
    isSystem: true,
    note: '匹配 t.me / telegram.me 的群组邀请链接与频道链接，含隐藏 text_link。',
  },
  {
    name: '加好友引流话术',
    pattern: String.raw`(?:加|扣|私)\s*(?:我|你)?\s*(?:微信|徽信|威信|vx|VX|v信|V信|QQ|qq|扣扣|WhatsApp|Telegram|电报|飞机)`,
    flags: 'iu',
    matchMode: 'regex',
    target: 'all',
    action: 'delete',
    severity: 20,
    priority: 20,
    enabled: true,
    isSystem: true,
    note: '「加微信」「私我QQ」这类最常见的引流话术。',
  },
  {
    name: '加密货币 / 博彩引流',
    pattern: String.raw`\b(?:usdt|btc|eth|trx)\b|比特币|以太坊|博彩|棋牌|时时彩|六合彩|百家乐|带单|喊单|返水`,
    flags: 'iu',
    matchMode: 'regex',
    target: 'all',
    action: 'delete',
    severity: 25,
    priority: 30,
    enabled: true,
    isSystem: true,
    note: '使用 \\b 词边界，避免把 ethernet 之类的正常词误伤。',
  },
  {
    name: '短链接服务',
    pattern: String.raw`\b(?:bit\.ly|tinyurl\.com|is\.gd|cutt\.ly|shorturl\.at|rebrand\.ly|t\.cn|dwz\.cn|suo\.im|urlz\.fr)/[A-Za-z0-9]+`,
    flags: 'iu',
    matchMode: 'regex',
    target: 'all',
    action: 'delete',
    severity: 10,
    priority: 40,
    enabled: true,
    isSystem: true,
    note: '短链接常用作跳转规避，命中即删。可按需补充自有白名单。',
  },
  {
    name: '兼职刷单诈骗话术',
    pattern: String.raw`(?:日入|月入|轻松赚|躺赚|稳定收入|一部手机)\s*\d+\s*(?:元|块|米|w|万)?|刷单|点赞任务|关注任务`,
    flags: 'iu',
    matchMode: 'regex',
    target: 'all',
    action: 'escalate',
    severity: 30,
    priority: 15,
    enabled: true,
    isSystem: true,
    note: '诈骗高发话术，直接走阶梯处罚而非仅删除。',
  },
  {
    name: '超长消息（疑似刷屏）',
    pattern: String.raw`^[\s\S]{1500,}$`,
    flags: 'u',
    matchMode: 'regex',
    target: 'text',
    action: 'notify',
    severity: 5,
    priority: 200,
    enabled: false,
    isSystem: true,
    note: '默认停用。启用后对超长消息只提醒管理员，不做处罚 —— 容易误伤长文咨询。',
  },
];
