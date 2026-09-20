/**
 * 领域词汇表 —— 服务端与前端共用的唯一事实源。
 *
 * 这里刻意不使用 TypeScript 的 `enum`：Node 24 原生执行 TS 时只能做「类型擦除」，
 * 而 enum 会生成运行时代码，无法被擦除。改用 `as const` 对象 + 联合类型，
 * 既满足 `erasableSyntaxOnly`，又能直接喂给 zod 的 `z.enum()`。
 */

/** 中继消息的内容类型 */
export const CONTENT_TYPES = [
  'text',
  'photo',
  'video',
  'document',
  'audio',
  'voice',
  'sticker',
  'animation',
  'video_note',
  'location',
  'contact',
  'poll',
  'dice',
  'unknown',
] as const;
export type ContentType = (typeof CONTENT_TYPES)[number];

/** 有实际文件负载、需要走文件下载而不是纯文本中继的类型 */
export const MEDIA_CONTENT_TYPES = [
  'photo',
  'video',
  'document',
  'audio',
  'voice',
  'sticker',
  'animation',
  'video_note',
] as const satisfies readonly ContentType[];

/** 话题（一人一会话）的生命周期 */
export const TOPIC_STATUSES = ['open', 'closed', 'deleted'] as const;
export type TopicStatus = (typeof TOPIC_STATUSES)[number];

/** 中继方向：user_to_admin = 用户私聊 → 管理群话题；admin_to_user 反之 */
export const MESSAGE_DIRECTIONS = ['user_to_admin', 'admin_to_user'] as const;
export type MessageDirection = (typeof MESSAGE_DIRECTIONS)[number];

/** 机器人在面板上的健康状态 */
export const BOT_HEALTH_STATUSES = ['unknown', 'starting', 'online', 'error', 'stopped'] as const;
export type BotHealthStatus = (typeof BOT_HEALTH_STATUSES)[number];

/** 规则匹配方式 */
export const MATCH_MODES = ['regex', 'contains', 'whole_word'] as const;
export type MatchMode = (typeof MATCH_MODES)[number];

/**
 * 规则匹配目标 —— 决定拿消息的哪一部分去匹配。
 * 注意 `text_link`：广告最常见的藏链接手法是把 URL 塞进 entity 里，
 * 显示文本完全正常，所以必须单独覆盖 entity 中的隐藏链接。
 */
export const RULE_TARGETS = [
  'text',
  'caption',
  'text_link',
  'url',
  'mention',
  'forward',
  'all',
] as const;
export type RuleTarget = (typeof RULE_TARGETS)[number];

/** 规则命中后可执行的动作 */
export const RULE_ACTIONS = ['delete', 'warn', 'escalate', 'notify'] as const;
export type RuleAction = (typeof RULE_ACTIONS)[number];

/**
 * 处罚类型。
 *
 * 之所以没有 Telegram 原生的 `restrict`/`kick`：在「用户私聊 → 管理群话题」的
 * 中继模型下，终端用户并不在管理群里，`restrictChatMember` / `banChatMember`
 * 对非群成员会直接报错。因此处罚一律在机器人层级实现：
 *   - warn    私聊发送警告文案
 *   - silence 静默：消息不再转发进话题（用户无感知，石沉大海）
 *   - mute    硬禁言：直接回复禁言提示与解禁时间，期间不转发
 *   - ban     拉黑：机器人不再响应，其话题自动关闭
 * 若该用户恰好也是管理群成员，则额外叠加真实的 restrictChatMember。
 */
export const SANCTION_TYPES = ['warn', 'silence', 'mute', 'ban'] as const;
export type SanctionType = (typeof SANCTION_TYPES)[number];

/** 真正会阻断消息转发的处罚（warn 不阻断） */
export const BLOCKING_SANCTION_TYPES = ['silence', 'mute', 'ban'] as const satisfies readonly SanctionType[];

/** 处罚来源 */
export const SANCTION_REASONS = ['rule_hit', 'manual', 'flood', 'escalation'] as const;
export type SanctionReason = (typeof SANCTION_REASONS)[number];

/** 审计日志的操作者类型 */
export const ACTOR_TYPES = ['admin', 'bot', 'system'] as const;
export type ActorType = (typeof ACTOR_TYPES)[number];

/** 面板操作审计的动作名 */
export const ADMIN_ACTIONS = [
  'login.success',
  'login.failure',
  'login.locked',
  'logout',
  'password.changed',
  'bot.created',
  'bot.updated',
  'bot.deleted',
  'bot.enabled',
  'bot.disabled',
  'bot.reloaded',
  'rule.created',
  'rule.updated',
  'rule.deleted',
  'rule.toggled',
  'rule.auto_disabled',
  'rule.reordered',
  'session.closed',
  'session.reopened',
  'session.deleted',
  'message.sent',
  'contact.banned',
  'contact.unbanned',
  'contact.violations_reset',
  'contact.notes_updated',
  'settings.updated',
] as const;
export type AdminAction = (typeof ADMIN_ACTIONS)[number];

/** 规则命中后实际执行的动作记录（写进审计，便于复盘） */
export const HIT_OUTCOMES = [
  'deleted',
  'delete_failed',
  'warned',
  'silenced',
  'muted',
  'banned',
  'notified',
  'notify_failed',
  'logged_only',
  'regex_timeout',
] as const;
export type HitOutcome = (typeof HIT_OUTCOMES)[number];
