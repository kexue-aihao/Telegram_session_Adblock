import type { Message } from 'grammy/types';
import type { ContentType, RuleTarget } from '@tgs/shared';
import { entityTexts, extractLinks, normalizeText } from './text.ts';

/**
 * 把 grammY 的 Message 压成中继需要的形态。
 *
 * 单独成文件而不是塞进 handler：中继、规则引擎、编辑镜像三条路径都要
 * 「从一条 Telegram 消息里读出内容类型与文本」，各写一遍必然三份实现慢慢漂移。
 */

export interface MediaItem {
  kind: string;
  fileId: string;
  fileUniqueId: string;
  /** 相册里的顺序 */
  position: number;
}

export interface ExtractedMessage {
  contentType: ContentType;
  text: string | null;
  caption: string | null;
  media: MediaItem[];
  mediaGroupId: string | null;
  /** 是否存在 entity 里藏着的链接 */
  hasHiddenLink: boolean;
  hiddenLinks: string[];
  /** 是否是可中继的内容（排除服务消息、空的系统消息） */
  relayable: boolean;
}

/** 中继会丢掉的信息在面板上无法还原，所以这里只做粗分类 */
export function detectContentType(message: Message): ContentType {
  if (message.text !== undefined) return 'text';
  if (message.photo !== undefined) return 'photo';
  if (message.video !== undefined) return 'video';
  if (message.document !== undefined) return 'document';
  if (message.audio !== undefined) return 'audio';
  if (message.voice !== undefined) return 'voice';
  if (message.sticker !== undefined) return 'sticker';
  if (message.animation !== undefined) return 'animation';
  if (message.video_note !== undefined) return 'video_note';
  if (message.location !== undefined || message.venue !== undefined) return 'location';
  if (message.contact !== undefined) return 'contact';
  if (message.poll !== undefined) return 'poll';
  if (message.dice !== undefined) return 'dice';
  return 'unknown';
}

/**
 * 取出需要落库的媒体文件标识。
 *
 * 相册里的每张图都带 `media_group_id`，中继时用 `copyMessages` 整组转发，
 * 这里把 `position` 记下来是为了将来能按原顺序渲染缩略图列表。
 */
export function extractMedia(message: Message): MediaItem[] {
  const items: MediaItem[] = [];

  if (message.photo && message.photo.length > 0) {
    // Telegram 下发的 photo 是同一张图的多个尺寸，取最大的那个
    const largest = message.photo[message.photo.length - 1];
    if (largest) {
      items.push({
        kind: 'photo',
        fileId: largest.file_id,
        fileUniqueId: largest.file_unique_id,
        position: 0,
      });
    }
  }
  if (message.video) {
    items.push({ ...idsOf('video', message.video), position: items.length });
  }
  if (message.animation) {
    items.push({ ...idsOf('animation', message.animation), position: items.length });
  }
  if (message.video_note) {
    items.push({ ...idsOf('video_note', message.video_note), position: items.length });
  }
  if (message.audio) {
    items.push({ ...idsOf('audio', message.audio), position: items.length });
  }
  if (message.voice) {
    items.push({ ...idsOf('voice', message.voice), position: items.length });
  }
  if (message.document) {
    items.push({ ...idsOf('document', message.document), position: items.length });
  }
  if (message.sticker) {
    items.push({
      kind: 'sticker',
      fileId: message.sticker.file_id,
      fileUniqueId: message.sticker.file_unique_id,
      position: items.length,
    });
  }

  return items;
}

function idsOf(kind: string, file: { file_id: string; file_unique_id: string }) {
  return { kind, fileId: file.file_id, fileUniqueId: file.file_unique_id };
}

/** entity 列表所在的那段文本：带 caption 的消息 entity 属于 caption，不是 text */function entitySource(message: Message): string {
  if (message.text !== undefined) return message.text;
  if (message.caption !== undefined) return message.caption;
  return '';
}

function entitiesOf(message: Message) {
  return message.entities ?? message.caption_entities ?? [];
}

export function extractMessage(message: Message): ExtractedMessage {
  const contentType = detectContentType(message);
  const text = message.text ?? null;
  const caption = message.caption ?? null;
  const source = entitySource(message);
  const entities = entitiesOf(message);

  const links = extractLinks(source, entities);
  // 只有 text_link 才算「隐藏链接」：`url` entity 的链接是明摆着写在正文里的，
  // 管理员一眼能看见，而 text_link 的显示文本完全可以是「点击查看」。
  const hidden = links.filter((link) => link.kind === 'text_link');

  return {
    contentType,
    text,
    caption,
    media: extractMedia(message),
    mediaGroupId: message.media_group_id ?? null,
    hasHiddenLink: hidden.length > 0,
    hiddenLinks: hidden.map((link) => link.url),
    relayable: contentType !== 'unknown' || text !== null || caption !== null,
  };
}

/**
 * 按「匹配目标」把一条消息摊成多份文本。
 *
 * 为什么不合成一份大字符串：`target: 'url'` 的规则不应该被正文里的
 * 「加微信」触发 —— 管理员选 `url` 就是明确表示「只查链接」。
 * 合成一份会让 target 这个字段失去意义，规则会变得难以预测。
 *
 * 同时给出**原文**与**归一化文本**两份：引擎匹配归一化后的，
 * 而 `matchedText` 要能回指原文，两者都留才解释得清「凭什么是它」。
 */
export interface Haystacks {
  raw: Partial<Record<RuleTarget, string>>;
  normalized: Partial<Record<RuleTarget, string>>;
}

export function buildHaystacks(message: Message): Haystacks {
  const source = entitySource(message);
  const entities = entitiesOf(message);

  const raw: Partial<Record<RuleTarget, string>> = {};

  const text = message.text ?? '';
  const caption = message.caption ?? '';
  if (text) raw.text = text;
  if (caption) raw.caption = caption;

  const hidden = entityTexts(source, entities, ['text_link']);
  if (hidden) raw.text_link = hidden;

  const urls = entityTexts(source, entities, ['url']);
  if (urls) raw.url = urls;

  const mentions = entityTexts(source, entities, ['mention', 'text_mention']);
  if (mentions) raw.mention = mentions;

  // 转发消息：把被转发方的署名也算进来 —— 「转自 X 频道」本身就是引流信号
  if (message.forward_origin) {
    const origin = message.forward_origin;
    const parts: string[] = [];
    if ('sender_user_name' in origin && origin.sender_user_name) parts.push(origin.sender_user_name);
    if ('chat' in origin && origin.chat) {
      parts.push(origin.chat.title ?? '');
      if (origin.chat.username) parts.push(`@${origin.chat.username}`);
    }
    if ('sender_user' in origin && origin.sender_user) {
      parts.push(origin.sender_user.first_name ?? '');
      if (origin.sender_user.username) parts.push(`@${origin.sender_user.username}`);
    }
    const joined = parts.filter(Boolean).join('\n');
    if (joined) raw.forward = joined;
  }

  // `all` 是「全都查」：正文 + 说明 + 各类 entity + 转发来源，一份合集。
  // 注意把隐藏链接的真实 URL 也并进来，否则最该查的那部分反而漏了。
  const allParts = [
    text,
    caption,
    hidden,
    urls,
    mentions,
    raw.forward ?? '',
  ].filter(Boolean);
  const all = allParts.join('\n');
  if (all) raw.all = all;

  const normalized: Partial<Record<RuleTarget, string>> = {};
  for (const [target, value] of Object.entries(raw)) {
    if (typeof value === 'string' && value) {
      normalized[target as RuleTarget] = normalizeText(value);
    }
  }

  return { raw, normalized };
}

/** 发信人信息。用结构化类型而不是 grammY 的 User，避免核心层依赖 grammY 的类型 */
export interface IncomingSender {
  id: number;
  username?: string | undefined;
  first_name: string;
  last_name?: string | undefined;
  language_code?: string | undefined;
}

/**
 * 一条待处理消息的完整快照。
 *
 * 之所以从这里开始就不再依赖 grammY 的 `Message` 对象：相册需要先缓冲
 * 几百毫秒再处理，而那时再回头向 Telegram 要这条消息是**做不到**的
 * （Bot API 没有 getMessage）。所有需要的信息必须在收到更新的当下就抽出来。
 */
export interface IncomingMessage {
  chatId: number;
  messageId: number;
  from: IncomingSender;
  content: ExtractedMessage;
  haystacks: Haystacks;
  /** 审计兜底用的原文 */
  rawText: string;
}

export function toIncoming(message: Message): IncomingMessage | null {
  if (!message.from) return null;
  return {
    chatId: message.chat.id,
    messageId: message.message_id,
    from: message.from,
    content: extractMessage(message),
    haystacks: buildHaystacks(message),
    rawText: message.text ?? message.caption ?? '',
  };
}
