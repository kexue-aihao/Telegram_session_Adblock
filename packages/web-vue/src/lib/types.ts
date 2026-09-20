/**
 * 后端契约类型。
 *
 * 与 Go 服务端的 JSON 字段一一对应。刻意不在前后端之间共享一份类型定义：
 * Go 与 TypeScript 无法共用类型，一份「共享」的 TS 类型只能靠人工同步，
 * 反而会给人「有类型保护」的错觉。这里就是一份普通的 DTO 定义，
 * 改动时以服务端为准。
 */

export interface Bot {
  id: number;
  name: string;
  username: string;
  tokenMask: string;
  telegramId: number | null;
  adminGroupId: number | null;
  adminGroupTitle: string | null;
  isEnabled: boolean;
  healthStatus: 'unknown' | 'starting' | 'online' | 'error' | 'stopped';
  lastError: string | null;
  lastPolledAt: number | null;
  createdAt: number;
  updatedAt: number;
}

export interface BotValidation {
  ok: boolean;
  telegramId: number | null;
  name: string | null;
  username: string | null;
  canJoinGroups: boolean | null;
  canReadAllGroupMessages: boolean | null;
  error: string | null;
}

export interface GroupCheck {
  ok: boolean;
  chatId: number;
  title: string | null;
  isForum: boolean;
  isAdmin: boolean;
  canManageTopics: boolean;
  canDeleteMessages: boolean;
  canRestrictMembers: boolean;
  problems: string[];
}

export interface EscalationStep {
  id: string;
  atScore: number;
  type: 'warn' | 'silence' | 'mute' | 'ban';
  durationMinutes: number | null;
  deleteMessage: boolean;
  notifyAdmins: boolean;
  enabled: boolean;
}

export interface EscalationConfig {
  steps: EscalationStep[];
  decayDays: number | null;
  maxScore: number;
}

export interface BotSettings {
  botId: number;
  topicNameTemplate: string;
  topicIconColor: number;
  autoCloseHours: number | null;
  pinTopicHeader: boolean;
  greetingText: string;
  warnTemplate: string;
  muteTemplate: string;
  banTemplate: string;
  silenceTemplate: string;
  alertCardTemplate: string;
  topicHeaderTemplate: string;
  rulesEnabled: boolean;
  notifyAdmins: boolean;
  escalation: EscalationConfig;
  deleteOriginMessage: boolean;
  mirrorEdits: boolean;
  mirrorDeletes: boolean;
  coalesceWindowMs: number;
  coalesceThreshold: number;
  floodThreshold: number;
  notifyOnUnreachable: boolean;
}

export interface GlobalSettings {
  timezone: string;
  auditRetentionDays: number | null;
  autoDisableOnRegexTimeout: boolean;
  regexTimeoutMs: number;
}

export interface AdRule {
  id: number;
  botId: number | null;
  name: string;
  pattern: string;
  flags: string;
  matchMode: 'regex' | 'contains' | 'whole_word';
  target: 'text' | 'caption' | 'text_link' | 'url' | 'mention' | 'forward' | 'all';
  action: 'delete' | 'warn' | 'escalate' | 'notify';
  severity: number;
  priority: number;
  isEnabled: boolean;
  isSystem: boolean;
  note: string | null;
  hitCount: number;
  lastHitAt: number | null;
  autoDisabled: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface RuleMatch {
  start: number;
  end: number;
  text: string;
  groups: string[];
}

export interface RuleTestResult {
  ok: boolean;
  error: string | null;
  matches: RuleMatch[];
  truncated: boolean;
  timedOut: boolean;
  durationMs: number;
  normalizedSample: string | null;
}

export interface RuleHit {
  id: number;
  ruleId: number | null;
  ruleName: string;
  rulePattern: string;
  ruleFlags: string;
  botId: number;
  botName: string;
  contactId: number;
  contactName: string;
  contactUsername: string | null;
  tgUserId: number;
  topicId: number | null;
  threadId: number | null;
  matchedText: string;
  normalizedExcerpt: string | null;
  outcomes: string[];
  severity: number;
  createdAt: number;
}

export interface SessionSummary {
  id: number;
  botId: number;
  botName: string;
  botUsername: string;
  contactId: number;
  displayName: string;
  username: string | null;
  tgUserId: number;
  threadId: number;
  title: string;
  status: 'open' | 'closed' | 'deleted';
  lastMessageAt: number | null;
  lastMessagePreview: string | null;
  lastMessageDirection: 'user_to_admin' | 'admin_to_user' | null;
  messageCount: number;
  violationScore: number;
  isBlocked: boolean;
  createdAt: number;
  closedAt: number | null;
}

export interface Media {
  kind: string;
  fileId: string;
  fileUniqueId: string;
  position: number;
}

export interface RelayedMessage {
  id: number;
  topicId: number;
  direction: 'user_to_admin' | 'admin_to_user';
  content: {
    type: string;
    text: string | null;
    caption: string | null;
    media: Media[];
    mediaGroupId: string | null;
    hasHiddenLink: boolean;
    hiddenLinks: string[];
  };
  isDeleted: boolean;
  editedAt: number | null;
  createdAt: number;
  ruleHitId: number | null;
  senderLabel: string | null;
}

export interface Sanction {
  id: number;
  botId: number;
  contactId: number;
  type: 'warn' | 'silence' | 'mute' | 'ban';
  reason: string;
  ruleId: number | null;
  expiresAt: number | null;
  isActive: boolean;
  createdBy: string | null;
  createdAt: number;
  liftedAt: number | null;
}

export interface RecentHit {
  id: number;
  ruleName: string;
  matchedText: string;
  outcome: string;
  createdAt: number;
}

export interface SessionDetail {
  session: SessionSummary;
  activeSanctions: Sanction[];
  notes: string | null;
  recentHits: RecentHit[];
}

export interface AuditEntry {
  id: number;
  actorType: string;
  actorId: string | null;
  action: string;
  targetType: string | null;
  targetId: string | null;
  detail: Record<string, unknown> | null;
  ip: string | null;
  createdAt: number;
}

export interface Overview {
  bots: { total: number; online: number; error: number; disabled: number };
  sessions: { open: number; closed: number; createdToday: number };
  contacts: { total: number; blocked: number; flagged: number };
  messages: { inToday: number; outToday: number; total: number };
  ads: { blockedToday: number; blocked24h: number; blockedTotal: number };
  rules: { total: number; enabled: number; autoDisabled: number };
  deltas: {
    messagesIn: number | null;
    adsBlocked: number | null;
    sessionsCreated: number | null;
  };
  generatedAt: number;
}

export interface TimeseriesPoint {
  date: string;
  messagesIn: number;
  messagesOut: number;
  topicsCreated: number;
  adsBlocked: number;
}

export interface AdminSession {
  username: string;
  createdAt: number;
  expiresAt: number;
  ip: string | null;
  userAgent: string | null;
}

export interface Paginated<T> {
  items: T[];
  nextCursor: number | null;
  total: number | null;
}
