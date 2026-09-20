-- 全部时间列都以 **epoch 毫秒整数** 存储，而不是 SQLite 的 datetime 文本。
-- 原因：共用 DTO 里的时间字段就是整数毫秒，直接存数字让 DB → JSON 的映射
-- 是恒等变换，少一层时区往返与格式化陷阱。
--
-- 全部布尔列用 INTEGER 0/1（SQLite 没有原生 bool）。

-- ────────────────────────────── 机器人 ──────────────────────────────

CREATE TABLE IF NOT EXISTS bots (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL,
    username          TEXT    NOT NULL,
    -- token 以 AES-256-GCM 加密，密文 / IV / 认证标签分列存
    token_cipher      TEXT    NOT NULL,
    token_iv          TEXT    NOT NULL,
    token_tag         TEXT    NOT NULL,
    -- 仅用于展示的掩码；明文永不落库、永不回传
    token_mask        TEXT    NOT NULL,
    telegram_id       INTEGER,
    admin_group_id    INTEGER,
    admin_group_title TEXT,
    is_enabled        INTEGER NOT NULL DEFAULT 1,
    health_status     TEXT    NOT NULL DEFAULT 'unknown',
    last_error        TEXT,
    last_polled_at    INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS bots_username_uniq ON bots (username);

CREATE TABLE IF NOT EXISTS bot_settings (
    bot_id                INTEGER PRIMARY KEY REFERENCES bots (id) ON DELETE CASCADE,
    topic_name_template   TEXT    NOT NULL,
    topic_icon_color      INTEGER NOT NULL,
    auto_close_hours      INTEGER,
    pin_topic_header      INTEGER NOT NULL DEFAULT 1,
    greeting_text         TEXT    NOT NULL,
    warn_template         TEXT    NOT NULL,
    mute_template         TEXT    NOT NULL,
    ban_template          TEXT    NOT NULL,
    silence_template      TEXT    NOT NULL,
    alert_card_template   TEXT    NOT NULL,
    topic_header_template TEXT    NOT NULL,
    rules_enabled         INTEGER NOT NULL DEFAULT 1,
    notify_admins         INTEGER NOT NULL DEFAULT 1,
    -- 阶梯处罚配置整块存 JSON：档位数量与阈值都可由管理员改动，
    -- 拆成列会让「加一档」变成一次表结构变更
    escalation            TEXT    NOT NULL,
    delete_origin_message INTEGER NOT NULL DEFAULT 1,
    mirror_edits          INTEGER NOT NULL DEFAULT 1,
    mirror_deletes        INTEGER NOT NULL DEFAULT 1,
    coalesce_window_ms    INTEGER NOT NULL DEFAULT 400,
    coalesce_threshold    INTEGER NOT NULL DEFAULT 5,
    flood_threshold       INTEGER NOT NULL DEFAULT 8,
    notify_on_unreachable INTEGER NOT NULL DEFAULT 1
);

-- 每个 bot 的长轮询 offset，重启后不丢更新（避免用户消息被中继两次）
CREATE TABLE IF NOT EXISTS bot_offsets (
    bot_id     INTEGER PRIMARY KEY REFERENCES bots (id) ON DELETE CASCADE,
    offset     INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);

-- ────────────────────────────── 终端用户 ──────────────────────────────

CREATE TABLE IF NOT EXISTS contacts (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id            INTEGER NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    tg_user_id        INTEGER NOT NULL,
    username          TEXT,
    first_name        TEXT,
    last_name         TEXT,
    language_code     TEXT,
    is_blocked        INTEGER NOT NULL DEFAULT 0,
    is_unreachable    INTEGER NOT NULL DEFAULT 0,
    violation_score   INTEGER NOT NULL DEFAULT 0,
    last_violation_at INTEGER,
    first_seen_at     INTEGER NOT NULL,
    last_seen_at      INTEGER NOT NULL,
    notes             TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS contacts_bot_user_uniq ON contacts (bot_id, tg_user_id);
CREATE INDEX IF NOT EXISTS contacts_score_idx ON contacts (bot_id, violation_score);

-- ────────────────────────────── 话题 / 会话 ──────────────────────────────

CREATE TABLE IF NOT EXISTS topics (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id            INTEGER NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    contact_id        INTEGER NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    -- Telegram 侧的 message_thread_id，中继热路径靠它反查
    message_thread_id INTEGER NOT NULL,
    title             TEXT    NOT NULL,
    icon_color        INTEGER NOT NULL,
    status            TEXT    NOT NULL DEFAULT 'open',
    last_message_at   INTEGER,
    pinned_header_id  INTEGER,
    created_at        INTEGER NOT NULL,
    closed_at         INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS topics_bot_contact_uniq ON topics (bot_id, contact_id);
CREATE UNIQUE INDEX IF NOT EXISTS topics_bot_thread_uniq ON topics (bot_id, message_thread_id);
CREATE INDEX IF NOT EXISTS topics_status_recent_idx ON topics (status, last_message_at);

-- ────────────────────────────── 消息映射 ──────────────────────────────

CREATE TABLE IF NOT EXISTS messages (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id             INTEGER NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    topic_id           INTEGER NOT NULL REFERENCES topics (id) ON DELETE CASCADE,
    direction          TEXT    NOT NULL,
    source_chat_id     INTEGER NOT NULL,
    tg_message_id      INTEGER NOT NULL,
    dest_chat_id       INTEGER,
    relayed_message_id INTEGER,
    content_type       TEXT    NOT NULL,
    text               TEXT,
    caption            TEXT,
    media_group_id     TEXT,
    has_hidden_link    INTEGER NOT NULL DEFAULT 0,
    hidden_links       TEXT,
    rule_hit_id        INTEGER,
    sender_label       TEXT,
    created_at         INTEGER NOT NULL,
    edited_at          INTEGER,
    deleted_at         INTEGER
);
CREATE INDEX IF NOT EXISTS messages_topic_recent_idx ON messages (topic_id, created_at);
-- 编辑/删除镜像的两个反查入口
CREATE INDEX IF NOT EXISTS messages_source_idx ON messages (source_chat_id, tg_message_id);
CREATE INDEX IF NOT EXISTS messages_dest_idx ON messages (dest_chat_id, relayed_message_id);
CREATE INDEX IF NOT EXISTS messages_media_group_idx ON messages (media_group_id);

CREATE TABLE IF NOT EXISTS message_media (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id     INTEGER NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    kind           TEXT    NOT NULL,
    file_id        TEXT    NOT NULL,
    file_unique_id TEXT    NOT NULL,
    position       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS message_media_message_idx ON message_media (message_id);

-- ────────────────────────────── 规则与审计 ──────────────────────────────

CREATE TABLE IF NOT EXISTS ad_rules (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    -- NULL = 全局规则，对所有机器人生效
    bot_id               INTEGER REFERENCES bots (id) ON DELETE CASCADE,
    name                 TEXT    NOT NULL,
    pattern              TEXT    NOT NULL,
    flags                TEXT    NOT NULL,
    match_mode           TEXT    NOT NULL,
    target               TEXT    NOT NULL,
    action               TEXT    NOT NULL,
    severity             INTEGER NOT NULL DEFAULT 10,
    priority             INTEGER NOT NULL DEFAULT 100,
    is_enabled           INTEGER NOT NULL DEFAULT 1,
    is_system            INTEGER NOT NULL DEFAULT 0,
    note                 TEXT,
    hit_count            INTEGER NOT NULL DEFAULT 0,
    last_hit_at          INTEGER,
    -- 因正则执行异常被引擎自动停用的时刻
    auto_disabled_at     INTEGER,
    auto_disabled_reason TEXT,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ad_rules_enabled_priority_idx ON ad_rules (is_enabled, priority);

CREATE TABLE IF NOT EXISTS rule_hits (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 规则可能事后被改甚至被删，因此留可空外键 + 冗余快照
    rule_id            INTEGER REFERENCES ad_rules (id) ON DELETE SET NULL,
    rule_name          TEXT    NOT NULL,
    rule_pattern       TEXT    NOT NULL,
    rule_flags         TEXT    NOT NULL,
    bot_id             INTEGER NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    contact_id         INTEGER NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    topic_id           INTEGER REFERENCES topics (id) ON DELETE SET NULL,
    message_id         INTEGER,
    matched_text       TEXT    NOT NULL,
    normalized_excerpt TEXT,
    outcomes           TEXT    NOT NULL,
    severity           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS rule_hits_created_idx ON rule_hits (created_at);
CREATE INDEX IF NOT EXISTS rule_hits_bot_created_idx ON rule_hits (bot_id, created_at);
CREATE INDEX IF NOT EXISTS rule_hits_rule_idx ON rule_hits (rule_id);
CREATE INDEX IF NOT EXISTS rule_hits_contact_idx ON rule_hits (contact_id, created_at);

CREATE TABLE IF NOT EXISTS sanctions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    bot_id     INTEGER NOT NULL REFERENCES bots (id) ON DELETE CASCADE,
    contact_id INTEGER NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    type       TEXT    NOT NULL,
    reason     TEXT    NOT NULL,
    rule_id    INTEGER REFERENCES ad_rules (id) ON DELETE SET NULL,
    expires_at INTEGER,
    is_active  INTEGER NOT NULL DEFAULT 1,
    created_by TEXT,
    score_at   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    lifted_at  INTEGER
);
CREATE INDEX IF NOT EXISTS sanctions_contact_active_idx ON sanctions (contact_id, is_active);

-- 面板操作审计，与 rule_hits 分开存放：
-- 前者是「机器人拦了什么」（几十万行，按时间翻页），
-- 后者是「人做了什么」（量小但必须永久可查）。
CREATE TABLE IF NOT EXISTS audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_type  TEXT    NOT NULL,
    actor_id    TEXT,
    action      TEXT    NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    detail      TEXT,
    ip          TEXT,
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_log_created_idx ON audit_log (created_at);
CREATE INDEX IF NOT EXISTS audit_log_action_idx ON audit_log (action, created_at);

-- ────────────────────────────── 统计 ──────────────────────────────

CREATE TABLE IF NOT EXISTS stats_daily (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    date           TEXT    NOT NULL,
    -- 0 = 全部机器人的汇总行
    bot_id         INTEGER NOT NULL DEFAULT 0,
    messages_in    INTEGER NOT NULL DEFAULT 0,
    messages_out   INTEGER NOT NULL DEFAULT 0,
    topics_created INTEGER NOT NULL DEFAULT 0,
    ads_blocked    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS stats_daily_date_bot_uniq ON stats_daily (date, bot_id);

-- ────────────────────────────── 鉴权 ──────────────────────────────

CREATE TABLE IF NOT EXISTS admin_users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS admin_users_username_uniq ON admin_users (username);

CREATE TABLE IF NOT EXISTS admin_sessions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- 存 token 的 SHA-256，库被读走也无法直接冒用会话
    token_hash    TEXT    NOT NULL,
    admin_user_id INTEGER NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    ip            TEXT,
    user_agent    TEXT,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    revoked_at    INTEGER
);
CREATE UNIQUE INDEX IF NOT EXISTS admin_sessions_token_uniq ON admin_sessions (token_hash);
CREATE INDEX IF NOT EXISTS admin_sessions_expiry_idx ON admin_sessions (expires_at);

CREATE TABLE IF NOT EXISTS login_attempts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ip           TEXT    NOT NULL,
    succeeded    INTEGER NOT NULL,
    attempted_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS login_attempts_ip_time_idx ON login_attempts (ip, attempted_at);

-- ────────────────────────────── 全局设置 ──────────────────────────────

-- 单行键值表：全局设置项少且零散，不值得为每项建列
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT,
    updated_at INTEGER NOT NULL
);
