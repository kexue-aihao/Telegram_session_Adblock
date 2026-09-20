import type { EscalationConfig, HitOutcome } from '@tgs/shared';
import { index, integer, sqliteTable, text, uniqueIndex } from 'drizzle-orm/sqlite-core';

/**
 * 全部时间列都以 **epoch 毫秒整数** 存储，而不是 drizzle 的 timestamp 模式。
 * 原因：共用 DTO（@tgs/shared）里的时间字段就是 number，直接存数字可以
 * 让 DB → API 的映射是恒等变换，少一层 Date 往返与序列化陷阱。
 */

// ────────────────────────────── 机器人 ──────────────────────────────

export const bots = sqliteTable(
  'bots',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    name: text('name').notNull(),
    username: text('username').notNull(),
    /** token 以 AES-256-GCM 加密，密文 / IV / 认证标签分列存 */
    tokenCipher: text('token_cipher').notNull(),
    tokenIv: text('token_iv').notNull(),
    tokenTag: text('token_tag').notNull(),
    /** 仅用于展示的掩码，例如 `123456789:AAF…xY3`；明文永不落库、永不回传 */
    tokenMask: text('token_mask').notNull(),
    /** getMe 拿到的机器人自身 id */
    telegramId: integer('telegram_id'),
    adminGroupId: integer('admin_group_id'),
    adminGroupTitle: text('admin_group_title'),
    isEnabled: integer('is_enabled', { mode: 'boolean' }).notNull().default(true),
    healthStatus: text('health_status').notNull().default('unknown'),
    lastError: text('last_error'),
    lastPolledAt: integer('last_polled_at'),
    createdAt: integer('created_at').notNull(),
    updatedAt: integer('updated_at').notNull(),
  },
  (t) => [uniqueIndex('bots_username_uniq').on(t.username)],
);

export const botSettings = sqliteTable('bot_settings', {
  botId: integer('bot_id')
    .primaryKey()
    .references(() => bots.id, { onDelete: 'cascade' }),

  topicNameTemplate: text('topic_name_template').notNull(),
  topicIconColor: integer('topic_icon_color').notNull(),
  autoCloseHours: integer('auto_close_hours'),
  pinTopicHeader: integer('pin_topic_header', { mode: 'boolean' }).notNull().default(true),

  greetingText: text('greeting_text').notNull(),
  warnTemplate: text('warn_template').notNull(),
  muteTemplate: text('mute_template').notNull(),
  banTemplate: text('ban_template').notNull(),
  silenceTemplate: text('silence_template').notNull(),
  alertCardTemplate: text('alert_card_template').notNull(),
  topicHeaderTemplate: text('topic_header_template').notNull(),

  rulesEnabled: integer('rules_enabled', { mode: 'boolean' }).notNull().default(true),
  notifyAdmins: integer('notify_admins', { mode: 'boolean' }).notNull().default(true),
  escalation: text('escalation', { mode: 'json' }).$type<EscalationConfig>().notNull(),
  deleteOriginMessage: integer('delete_origin_message', { mode: 'boolean' }).notNull().default(true),

  mirrorEdits: integer('mirror_edits', { mode: 'boolean' }).notNull().default(true),
  mirrorDeletes: integer('mirror_deletes', { mode: 'boolean' }).notNull().default(true),
  coalesceWindowMs: integer('coalesce_window_ms').notNull().default(400),
  coalesceThreshold: integer('coalesce_threshold').notNull().default(5),
  floodThreshold: integer('flood_threshold').notNull().default(8),
  notifyOnUnreachable: integer('notify_on_unreachable', { mode: 'boolean' }).notNull().default(true),
});

// ────────────────────────────── 终端用户 ──────────────────────────────

export const contacts = sqliteTable(
  'contacts',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    botId: integer('bot_id')
      .notNull()
      .references(() => bots.id, { onDelete: 'cascade' }),
    tgUserId: integer('tg_user_id').notNull(),
    username: text('username'),
    firstName: text('first_name'),
    lastName: text('last_name'),
    languageCode: text('language_code'),
    /** 被管理员拉黑 */
    isBlocked: integer('is_blocked', { mode: 'boolean' }).notNull().default(false),
    /** 用户把机器人屏蔽了（发送时报 403/400 blocked 后置位） */
    isUnreachable: integer('is_unreachable', { mode: 'boolean' }).notNull().default(false),
    violationScore: integer('violation_score').notNull().default(0),
    lastViolationAt: integer('last_violation_at'),
    firstSeenAt: integer('first_seen_at').notNull(),
    lastSeenAt: integer('last_seen_at').notNull(),
    notes: text('notes'),
  },
  (t) => [
    uniqueIndex('contacts_bot_user_uniq').on(t.botId, t.tgUserId),
    index('contacts_score_idx').on(t.botId, t.violationScore),
  ],
);

// ────────────────────────────── 话题 / 会话 ──────────────────────────────

export const topics = sqliteTable(
  'topics',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    botId: integer('bot_id')
      .notNull()
      .references(() => bots.id, { onDelete: 'cascade' }),
    contactId: integer('contact_id')
      .notNull()
      .references(() => contacts.id, { onDelete: 'cascade' }),
    /** Telegram 侧的 message_thread_id，中继热路径靠它反查 */
    messageThreadId: integer('message_thread_id').notNull(),
    title: text('title').notNull(),
    iconColor: integer('icon_color').notNull(),
    status: text('status').notNull().default('open'),
    lastMessageAt: integer('last_message_at'),
    /** 置顶头部消息的 id，用于后续编辑（用户改名等） */
    pinnedHeaderId: integer('pinned_header_id'),
    createdAt: integer('created_at').notNull(),
    closedAt: integer('closed_at'),
  },
  (t) => [
    uniqueIndex('topics_bot_contact_uniq').on(t.botId, t.contactId),
    uniqueIndex('topics_bot_thread_uniq').on(t.botId, t.messageThreadId),
    index('topics_status_recent_idx').on(t.status, t.lastMessageAt),
  ],
);

// ────────────────────────────── 消息映射 ──────────────────────────────

export const messages = sqliteTable(
  'messages',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    botId: integer('bot_id')
      .notNull()
      .references(() => bots.id, { onDelete: 'cascade' }),
    topicId: integer('topic_id')
      .notNull()
      .references(() => topics.id, { onDelete: 'cascade' }),
    /** user_to_admin = 用户私聊 → 话题；admin_to_user 反之 */
    direction: text('direction').notNull(),
    sourceChatId: integer('source_chat_id').notNull(),
    tgMessageId: integer('tg_message_id').notNull(),
    destChatId: integer('dest_chat_id'),
    relayedMessageId: integer('relayed_message_id'),
    contentType: text('content_type').notNull(),
    text: text('text'),
    caption: text('caption'),
    mediaGroupId: text('media_group_id'),
    /** entity 里藏着的链接（广告最爱的藏 URL 手法） */
    hasHiddenLink: integer('has_hidden_link', { mode: 'boolean' }).notNull().default(false),
    hiddenLinks: text('hidden_links', { mode: 'json' }).$type<string[]>(),
    ruleHitId: integer('rule_hit_id'),
    /** admin_to_user 方向才有：发这条消息的管理员显示名 */
    senderLabel: text('sender_label'),
    createdAt: integer('created_at').notNull(),
    editedAt: integer('edited_at'),
    deletedAt: integer('deleted_at'),
  },
  (t) => [
    index('messages_topic_recent_idx').on(t.topicId, t.createdAt),
    // 编辑/删除镜像的两个反查入口
    index('messages_source_idx').on(t.sourceChatId, t.tgMessageId),
    index('messages_dest_idx').on(t.destChatId, t.relayedMessageId),
    index('messages_media_group_idx').on(t.mediaGroupId),
  ],
);

export const messageMedia = sqliteTable(
  'message_media',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    messageId: integer('message_id')
      .notNull()
      .references(() => messages.id, { onDelete: 'cascade' }),
    kind: text('kind').notNull(),
    fileId: text('file_id').notNull(),
    fileUniqueId: text('file_unique_id').notNull(),
    /** 相册里的顺序 */
    position: integer('position').notNull().default(0),
  },
  (t) => [index('message_media_message_idx').on(t.messageId)],
);

// ────────────────────────────── 规则与审计 ──────────────────────────────

export const adRules = sqliteTable(
  'ad_rules',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    /** null = 全局规则，对所有机器人生效 */
    botId: integer('bot_id').references(() => bots.id, { onDelete: 'cascade' }),
    name: text('name').notNull(),
    pattern: text('pattern').notNull(),
    flags: text('flags').notNull(),
    matchMode: text('match_mode').notNull(),
    target: text('target').notNull(),
    action: text('action').notNull(),
    severity: integer('severity').notNull().default(10),
    /** 越小越先匹配 */
    priority: integer('priority').notNull().default(100),
    isEnabled: integer('is_enabled', { mode: 'boolean' }).notNull().default(true),
    isSystem: integer('is_system', { mode: 'boolean' }).notNull().default(false),
    note: text('note'),
    hitCount: integer('hit_count').notNull().default(0),
    lastHitAt: integer('last_hit_at'),
    /** 因正则超时被引擎自动停用的时刻；非空即表示这条规则被判为 ReDoS 风险 */
    autoDisabledAt: integer('auto_disabled_at'),
    autoDisabledReason: text('auto_disabled_reason'),
    createdAt: integer('created_at').notNull(),
    updatedAt: integer('updated_at').notNull(),
  },
  (t) => [index('ad_rules_enabled_priority_idx').on(t.isEnabled, t.priority)],
);

export const ruleHits = sqliteTable(
  'rule_hits',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    /** 规则可能事后被改甚至被删，因此这里留可空外键 + 冗余快照 */
    ruleId: integer('rule_id').references(() => adRules.id, { onDelete: 'set null' }),
    ruleName: text('rule_name').notNull(),
    rulePattern: text('rule_pattern').notNull(),
    ruleFlags: text('rule_flags').notNull(),
    botId: integer('bot_id')
      .notNull()
      .references(() => bots.id, { onDelete: 'cascade' }),
    contactId: integer('contact_id')
      .notNull()
      .references(() => contacts.id, { onDelete: 'cascade' }),
    topicId: integer('topic_id').references(() => topics.id, { onDelete: 'set null' }),
    messageId: integer('message_id'),
    /** 命中的原始文本片段 */
    matchedText: text('matched_text').notNull(),
    /** 归一化后的上下文，用来解释「为什么这也能命中」 */
    normalizedExcerpt: text('normalized_excerpt'),
    /** 实际执行了哪些动作 */
    outcomes: text('outcomes', { mode: 'json' }).$type<HitOutcome[]>().notNull(),
    severity: integer('severity').notNull().default(0),
    createdAt: integer('created_at').notNull(),
  },
  (t) => [
    index('rule_hits_created_idx').on(t.createdAt),
    index('rule_hits_bot_created_idx').on(t.botId, t.createdAt),
    index('rule_hits_rule_idx').on(t.ruleId),
    index('rule_hits_contact_idx').on(t.contactId, t.createdAt),
  ],
);

export const sanctions = sqliteTable(
  'sanctions',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    botId: integer('bot_id')
      .notNull()
      .references(() => bots.id, { onDelete: 'cascade' }),
    contactId: integer('contact_id')
      .notNull()
      .references(() => contacts.id, { onDelete: 'cascade' }),
    type: text('type').notNull(),
    reason: text('reason').notNull(),
    ruleId: integer('rule_id').references(() => adRules.id, { onDelete: 'set null' }),
    /** null = 永久 */
    expiresAt: integer('expires_at'),
    isActive: integer('is_active', { mode: 'boolean' }).notNull().default(true),
    createdBy: text('created_by'),
    /** 触发时的违规分快照，便于复盘阶梯是否算错 */
    scoreAt: integer('score_at').notNull().default(0),
    createdAt: integer('created_at').notNull(),
    liftedAt: integer('lifted_at'),
  },
  (t) => [index('sanctions_contact_active_idx').on(t.contactId, t.isActive)],
);

/** 面板操作审计（登录、改规则、拉黑……），与 rule_hits 分开存放 */
export const auditLog = sqliteTable(
  'audit_log',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    actorType: text('actor_type').notNull(),
    actorId: text('actor_id'),
    action: text('action').notNull(),
    targetType: text('target_type'),
    targetId: text('target_id'),
    detail: text('detail', { mode: 'json' }).$type<Record<string, unknown>>(),
    ip: text('ip'),
    createdAt: integer('created_at').notNull(),
  },
  (t) => [
    index('audit_log_created_idx').on(t.createdAt),
    index('audit_log_action_idx').on(t.action, t.createdAt),
  ],
);

// ────────────────────────────── 统计 ──────────────────────────────

export const statsDaily = sqliteTable(
  'stats_daily',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    /** YYYY-MM-DD（按面板时区切日） */
    date: text('date').notNull(),
    /** 0 = 全部机器人的汇总行 */
    botId: integer('bot_id').notNull().default(0),
    messagesIn: integer('messages_in').notNull().default(0),
    messagesOut: integer('messages_out').notNull().default(0),
    topicsCreated: integer('topics_created').notNull().default(0),
    adsBlocked: integer('ads_blocked').notNull().default(0),
  },
  (t) => [uniqueIndex('stats_daily_date_bot_uniq').on(t.date, t.botId)],
);

// ────────────────────────────── 鉴权 ──────────────────────────────

export const adminUsers = sqliteTable(
  'admin_users',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    username: text('username').notNull(),
    /** Argon2id 编码串，自带盐与参数 */
    passwordHash: text('password_hash').notNull(),
    createdAt: integer('created_at').notNull(),
    updatedAt: integer('updated_at').notNull(),
  },
  (t) => [uniqueIndex('admin_users_username_uniq').on(t.username)],
);

export const adminSessions = sqliteTable(
  'admin_sessions',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    /** 存 token 的 SHA-256，库被读走也无法直接冒用会话 */
    tokenHash: text('token_hash').notNull(),
    adminUserId: integer('admin_user_id')
      .notNull()
      .references(() => adminUsers.id, { onDelete: 'cascade' }),
    ip: text('ip'),
    userAgent: text('user_agent'),
    createdAt: integer('created_at').notNull(),
    expiresAt: integer('expires_at').notNull(),
    revokedAt: integer('revoked_at'),
  },
  (t) => [
    uniqueIndex('admin_sessions_token_uniq').on(t.tokenHash),
    index('admin_sessions_expiry_idx').on(t.expiresAt),
  ],
);

export const loginAttempts = sqliteTable(
  'login_attempts',
  {
    id: integer('id').primaryKey({ autoIncrement: true }),
    ip: text('ip').notNull(),
    succeeded: integer('succeeded', { mode: 'boolean' }).notNull(),
    attemptedAt: integer('attempted_at').notNull(),
  },
  (t) => [index('login_attempts_ip_time_idx').on(t.ip, t.attemptedAt)],
);

// ────────────────────────────── 全局设置 ──────────────────────────────

/** 单行键值表：全局设置项少且零散，不值得为每项建列 */
export const appSettings = sqliteTable('app_settings', {
  key: text('key').primaryKey(),
  value: text('value', { mode: 'json' }).$type<unknown>(),
  updatedAt: integer('updated_at').notNull(),
});

/** 每个 bot 的长轮询 offset，重启后不丢更新 */
export const botOffsets = sqliteTable('bot_offsets', {
  botId: integer('bot_id')
    .primaryKey()
    .references(() => bots.id, { onDelete: 'cascade' }),
  offset: integer('offset').notNull().default(0),
  updatedAt: integer('updated_at').notNull(),
});
