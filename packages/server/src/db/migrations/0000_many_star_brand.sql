CREATE TABLE `ad_rules` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`bot_id` integer,
	`name` text NOT NULL,
	`pattern` text NOT NULL,
	`flags` text NOT NULL,
	`match_mode` text NOT NULL,
	`target` text NOT NULL,
	`action` text NOT NULL,
	`severity` integer DEFAULT 10 NOT NULL,
	`priority` integer DEFAULT 100 NOT NULL,
	`is_enabled` integer DEFAULT true NOT NULL,
	`is_system` integer DEFAULT false NOT NULL,
	`note` text,
	`hit_count` integer DEFAULT 0 NOT NULL,
	`last_hit_at` integer,
	`auto_disabled_at` integer,
	`auto_disabled_reason` text,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `ad_rules_enabled_priority_idx` ON `ad_rules` (`is_enabled`,`priority`);--> statement-breakpoint
CREATE TABLE `admin_sessions` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`token_hash` text NOT NULL,
	`admin_user_id` integer NOT NULL,
	`ip` text,
	`user_agent` text,
	`created_at` integer NOT NULL,
	`expires_at` integer NOT NULL,
	`revoked_at` integer,
	FOREIGN KEY (`admin_user_id`) REFERENCES `admin_users`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `admin_sessions_token_uniq` ON `admin_sessions` (`token_hash`);--> statement-breakpoint
CREATE INDEX `admin_sessions_expiry_idx` ON `admin_sessions` (`expires_at`);--> statement-breakpoint
CREATE TABLE `admin_users` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`username` text NOT NULL,
	`password_hash` text NOT NULL,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `admin_users_username_uniq` ON `admin_users` (`username`);--> statement-breakpoint
CREATE TABLE `app_settings` (
	`key` text PRIMARY KEY NOT NULL,
	`value` text,
	`updated_at` integer NOT NULL
);
--> statement-breakpoint
CREATE TABLE `audit_log` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`actor_type` text NOT NULL,
	`actor_id` text,
	`action` text NOT NULL,
	`target_type` text,
	`target_id` text,
	`detail` text,
	`ip` text,
	`created_at` integer NOT NULL
);
--> statement-breakpoint
CREATE INDEX `audit_log_created_idx` ON `audit_log` (`created_at`);--> statement-breakpoint
CREATE INDEX `audit_log_action_idx` ON `audit_log` (`action`,`created_at`);--> statement-breakpoint
CREATE TABLE `bot_offsets` (
	`bot_id` integer PRIMARY KEY NOT NULL,
	`offset` integer DEFAULT 0 NOT NULL,
	`updated_at` integer NOT NULL,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE TABLE `bot_settings` (
	`bot_id` integer PRIMARY KEY NOT NULL,
	`topic_name_template` text NOT NULL,
	`topic_icon_color` integer NOT NULL,
	`auto_close_hours` integer,
	`pin_topic_header` integer DEFAULT true NOT NULL,
	`greeting_text` text NOT NULL,
	`warn_template` text NOT NULL,
	`mute_template` text NOT NULL,
	`ban_template` text NOT NULL,
	`silence_template` text NOT NULL,
	`alert_card_template` text NOT NULL,
	`topic_header_template` text NOT NULL,
	`rules_enabled` integer DEFAULT true NOT NULL,
	`notify_admins` integer DEFAULT true NOT NULL,
	`escalation` text NOT NULL,
	`delete_origin_message` integer DEFAULT true NOT NULL,
	`mirror_edits` integer DEFAULT true NOT NULL,
	`mirror_deletes` integer DEFAULT true NOT NULL,
	`coalesce_window_ms` integer DEFAULT 400 NOT NULL,
	`coalesce_threshold` integer DEFAULT 5 NOT NULL,
	`flood_threshold` integer DEFAULT 8 NOT NULL,
	`notify_on_unreachable` integer DEFAULT true NOT NULL,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE TABLE `bots` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`name` text NOT NULL,
	`username` text NOT NULL,
	`token_cipher` text NOT NULL,
	`token_iv` text NOT NULL,
	`token_tag` text NOT NULL,
	`token_mask` text NOT NULL,
	`telegram_id` integer,
	`admin_group_id` integer,
	`admin_group_title` text,
	`is_enabled` integer DEFAULT true NOT NULL,
	`health_status` text DEFAULT 'unknown' NOT NULL,
	`last_error` text,
	`last_polled_at` integer,
	`created_at` integer NOT NULL,
	`updated_at` integer NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `bots_username_uniq` ON `bots` (`username`);--> statement-breakpoint
CREATE TABLE `contacts` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`bot_id` integer NOT NULL,
	`tg_user_id` integer NOT NULL,
	`username` text,
	`first_name` text,
	`last_name` text,
	`language_code` text,
	`is_blocked` integer DEFAULT false NOT NULL,
	`is_unreachable` integer DEFAULT false NOT NULL,
	`violation_score` integer DEFAULT 0 NOT NULL,
	`last_violation_at` integer,
	`first_seen_at` integer NOT NULL,
	`last_seen_at` integer NOT NULL,
	`notes` text,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `contacts_bot_user_uniq` ON `contacts` (`bot_id`,`tg_user_id`);--> statement-breakpoint
CREATE INDEX `contacts_score_idx` ON `contacts` (`bot_id`,`violation_score`);--> statement-breakpoint
CREATE TABLE `login_attempts` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`ip` text NOT NULL,
	`succeeded` integer NOT NULL,
	`attempted_at` integer NOT NULL
);
--> statement-breakpoint
CREATE INDEX `login_attempts_ip_time_idx` ON `login_attempts` (`ip`,`attempted_at`);--> statement-breakpoint
CREATE TABLE `message_media` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`message_id` integer NOT NULL,
	`kind` text NOT NULL,
	`file_id` text NOT NULL,
	`file_unique_id` text NOT NULL,
	`position` integer DEFAULT 0 NOT NULL,
	FOREIGN KEY (`message_id`) REFERENCES `messages`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `message_media_message_idx` ON `message_media` (`message_id`);--> statement-breakpoint
CREATE TABLE `messages` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`bot_id` integer NOT NULL,
	`topic_id` integer NOT NULL,
	`direction` text NOT NULL,
	`source_chat_id` integer NOT NULL,
	`tg_message_id` integer NOT NULL,
	`dest_chat_id` integer,
	`relayed_message_id` integer,
	`content_type` text NOT NULL,
	`text` text,
	`caption` text,
	`media_group_id` text,
	`has_hidden_link` integer DEFAULT false NOT NULL,
	`hidden_links` text,
	`rule_hit_id` integer,
	`sender_label` text,
	`created_at` integer NOT NULL,
	`edited_at` integer,
	`deleted_at` integer,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`topic_id`) REFERENCES `topics`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `messages_topic_recent_idx` ON `messages` (`topic_id`,`created_at`);--> statement-breakpoint
CREATE INDEX `messages_source_idx` ON `messages` (`source_chat_id`,`tg_message_id`);--> statement-breakpoint
CREATE INDEX `messages_dest_idx` ON `messages` (`dest_chat_id`,`relayed_message_id`);--> statement-breakpoint
CREATE INDEX `messages_media_group_idx` ON `messages` (`media_group_id`);--> statement-breakpoint
CREATE TABLE `rule_hits` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`rule_id` integer,
	`rule_name` text NOT NULL,
	`rule_pattern` text NOT NULL,
	`rule_flags` text NOT NULL,
	`bot_id` integer NOT NULL,
	`contact_id` integer NOT NULL,
	`topic_id` integer,
	`message_id` integer,
	`matched_text` text NOT NULL,
	`normalized_excerpt` text,
	`outcomes` text NOT NULL,
	`severity` integer DEFAULT 0 NOT NULL,
	`created_at` integer NOT NULL,
	FOREIGN KEY (`rule_id`) REFERENCES `ad_rules`(`id`) ON UPDATE no action ON DELETE set null,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`contact_id`) REFERENCES `contacts`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`topic_id`) REFERENCES `topics`(`id`) ON UPDATE no action ON DELETE set null
);
--> statement-breakpoint
CREATE INDEX `rule_hits_created_idx` ON `rule_hits` (`created_at`);--> statement-breakpoint
CREATE INDEX `rule_hits_bot_created_idx` ON `rule_hits` (`bot_id`,`created_at`);--> statement-breakpoint
CREATE INDEX `rule_hits_rule_idx` ON `rule_hits` (`rule_id`);--> statement-breakpoint
CREATE INDEX `rule_hits_contact_idx` ON `rule_hits` (`contact_id`,`created_at`);--> statement-breakpoint
CREATE TABLE `sanctions` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`bot_id` integer NOT NULL,
	`contact_id` integer NOT NULL,
	`type` text NOT NULL,
	`reason` text NOT NULL,
	`rule_id` integer,
	`expires_at` integer,
	`is_active` integer DEFAULT true NOT NULL,
	`created_by` text,
	`score_at` integer DEFAULT 0 NOT NULL,
	`created_at` integer NOT NULL,
	`lifted_at` integer,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`contact_id`) REFERENCES `contacts`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`rule_id`) REFERENCES `ad_rules`(`id`) ON UPDATE no action ON DELETE set null
);
--> statement-breakpoint
CREATE INDEX `sanctions_contact_active_idx` ON `sanctions` (`contact_id`,`is_active`);--> statement-breakpoint
CREATE TABLE `stats_daily` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`date` text NOT NULL,
	`bot_id` integer DEFAULT 0 NOT NULL,
	`messages_in` integer DEFAULT 0 NOT NULL,
	`messages_out` integer DEFAULT 0 NOT NULL,
	`topics_created` integer DEFAULT 0 NOT NULL,
	`ads_blocked` integer DEFAULT 0 NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `stats_daily_date_bot_uniq` ON `stats_daily` (`date`,`bot_id`);--> statement-breakpoint
CREATE TABLE `topics` (
	`id` integer PRIMARY KEY AUTOINCREMENT NOT NULL,
	`bot_id` integer NOT NULL,
	`contact_id` integer NOT NULL,
	`message_thread_id` integer NOT NULL,
	`title` text NOT NULL,
	`icon_color` integer NOT NULL,
	`status` text DEFAULT 'open' NOT NULL,
	`last_message_at` integer,
	`pinned_header_id` integer,
	`created_at` integer NOT NULL,
	`closed_at` integer,
	FOREIGN KEY (`bot_id`) REFERENCES `bots`(`id`) ON UPDATE no action ON DELETE cascade,
	FOREIGN KEY (`contact_id`) REFERENCES `contacts`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `topics_bot_contact_uniq` ON `topics` (`bot_id`,`contact_id`);--> statement-breakpoint
CREATE UNIQUE INDEX `topics_bot_thread_uniq` ON `topics` (`bot_id`,`message_thread_id`);--> statement-breakpoint
CREATE INDEX `topics_status_recent_idx` ON `topics` (`status`,`last_message_at`);