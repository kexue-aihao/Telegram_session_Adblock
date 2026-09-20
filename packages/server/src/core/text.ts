/**
 * 文本归一化 —— 广告投放者绕过关键词的最常用手法。
 *
 * 真实广告几乎不会老老实实写「加微信」：常见变体包括
 *   - 零宽字符插入：`加<ZWSP>微信`
 *   - 全角混排：`加Ｖ信`
 *   - 用双向控制符把关键词切碎：`加<RLO>微<RLO>信`
 * 因此匹配前先做一次归一化，让上述变体都塌缩回同一形态。
 *
 * 归一化只用于**匹配与解释**，不改变落库的原文 —— 审计记录里的
 * `matchedText` 必须能说明「凭什么是它」，所以两边都留。
 */

/**
 * 零宽 / 方向控制 / 变体选择符的码位区间：肉眼不可见但能切开关键词，一律删除。
 *
 * 刻意不把字符直接写进正则字面量：这些码位本身就不可见，粘进源码后
 * 没人能靠读代码确认范围对不对，改错一位会静默失效。用码位表拼装则人可读、可 diff。
 */
const INVISIBLE_RANGES: ReadonlyArray<readonly [number, number]> = [
  [0x00ad, 0x00ad], // SOFT HYPHEN
  [0x061c, 0x061c], // ARABIC LETTER MARK
  [0x180e, 0x180e], // MONGOLIAN VOWEL SEPARATOR
  [0x200b, 0x200f], // ZWSP / ZWNJ / ZWJ / LRM / RLM
  [0x202a, 0x202e], // 双向嵌入与覆盖
  [0x2060, 0x2064], // WORD JOINER 与不可见运算符
  [0x2066, 0x206f], // 双向隔离符与已废弃的格式化符
  [0xfe00, 0xfe0f], // 变体选择符
  [0xfeff, 0xfeff], // BOM / ZWNBSP
];

function codepointClass(ranges: ReadonlyArray<readonly [number, number]>): string {
  return ranges
    .map(([start, end]) =>
      start === end
        ? `\\u{${start.toString(16)}}`
        : `\\u{${start.toString(16)}}-\\u{${end.toString(16)}}`,
    )
    .join('');
}

const INVISIBLE = new RegExp(`[${codepointClass(INVISIBLE_RANGES)}]`, 'gu');

/**
 * 常见同形异义字符 → ASCII。
 * 只收录广告里真实出现频率高的那些（全角字母数字、几个易混的希腊/西里尔字母），
 * 不做全表映射 —— 过度归一化会把正常外文内容也搅乱，反而增加误报。
 */
const HOMOGLYPHS: Record<string, string> = {};

// 全角段整体映射：U+FF01..U+FF5E 与 ASCII U+0021..U+007E 严格一一对应，
// 逐字列举既冗长又容易漏。
for (let code = 0x21; code <= 0x7e; code += 1) {
  HOMOGLYPHS[String.fromCharCode(0xff00 + code)] = String.fromCharCode(code);
}

// 西里尔 / 希腊字母冒充拉丁字母（vx、btc 这类短词最爱用）。同样用码位表：
// 这些字形与拉丁字母几乎一致，粘进源码后无法肉眼核对。
const CONFUSABLES: ReadonlyArray<readonly [number, string]> = [
  [0x0430, 'a'], // 西里尔 a
  [0x0432, 'b'],
  [0x0435, 'e'], // e
  [0x043a, 'k'],
  [0x043c, 'm'],
  [0x043d, 'h'],
  [0x043e, 'o'], // o
  [0x0440, 'p'], // p
  [0x0441, 'c'], // c
  [0x0442, 't'],
  [0x0443, 'y'], // y
  [0x0445, 'x'], // x
  [0x0455, 's'], // s
  [0x0456, 'i'], // i
  [0x0458, 'j'], // j
  [0x03bf, 'o'], // 希腊 o
  [0x03c1, 'p'], // p
  [0x03bd, 'v'], // v
  [0x03c4, 't'], // t
];

for (const [code, ascii] of CONFUSABLES) {
  HOMOGLYPHS[String.fromCharCode(code)] = ascii;
}

/**
 * 中文之间、以及中文与拉丁字母之间的「分隔符噪声」：
 * 加-微-信 / 加 微 信 / 加 Ｖ 信 都应塌缩成同一个形态。
 *
 * 只吃空格与标点、不吃换行 —— 吃掉换行会把前后两行粘成一个词，
 * 制造出原文里根本不存在的命中。
 */
const CJK = '[\\u{4E00}-\\u{9FFF}]';
const SEP = '[ \\t.\\-\\u{00B7}*_~^]';
const CJK_SEPARATORS = new RegExp(
  `(?<=${CJK})${SEP}{1,3}(?=\\S)|(?<=\\S)${SEP}{1,3}(?=${CJK})`,
  'gu',
);

/**
 * 归一化：NFKC → 去不可见字符 → 同形字折叠 → 去中文间插噪声 → 折叠空白。
 *
 * 刻意保留大小写 —— 规则默认带 `i` 标志，匹配端自己处理，
 * 这样 `usdt` 与 `USDT` 的行为与管理员预期一致。
 */
export function normalizeText(input: string): string {
  if (!input) return '';

  const stripped = input.normalize('NFKC').replace(INVISIBLE, '');

  let folded = '';
  for (const ch of stripped) {
    folded += HOMOGLYPHS[ch] ?? ch;
  }

  folded = folded.replace(CJK_SEPARATORS, '');
  // 折叠连续空白为单个空格，并去掉首尾 —— 让 `\s+` 之类的模式行为可预测
  return folded.replace(/\s+/g, ' ').trim();
}

/**
 * 提取 entity 里藏着的真实 URL。
 *
 * 广告最爱的藏链接手法：显示文本是「点击查看」，`text_link` entity 里
 * 藏一个 t.me 邀请链接。只看 `message.text` 完全看不出来，必须单独取。
 */
export interface ExtractedLink {
  url: string;
  kind: 'text_link' | 'url';
  /** entity 覆盖的显示文本 */
  text: string;
}

export interface EntityLike {
  type: string;
  offset: number;
  length: number;
  url?: string;
}

/** Telegram 的 entity offset/length 以 UTF-16 码元计，与 JS 字符串下标一致 */
export function extractLinks(
  text: string,
  entities: readonly EntityLike[] | undefined,
): ExtractedLink[] {
  if (!text || !entities || entities.length === 0) return [];

  const links: ExtractedLink[] = [];
  for (const entity of entities) {
    const covered = text.slice(entity.offset, entity.offset + entity.length);
    if (entity.type === 'text_link' && entity.url) {
      links.push({ url: entity.url, kind: 'text_link', text: covered });
    } else if (entity.type === 'url') {
      links.push({ url: covered, kind: 'url', text: covered });
    }
  }
  return links;
}

/** 各 entity 类型覆盖的文本，用于 `target: 'mention' | 'url' | 'text_link'` */
export function entityTexts(
  text: string,
  entities: readonly EntityLike[] | undefined,
  types: readonly string[],
): string {
  if (!text || !entities || entities.length === 0) return '';
  const parts: string[] = [];
  for (const entity of entities) {
    if (!types.includes(entity.type)) continue;
    if (entity.type === 'text_link' && entity.url) {
      // 隐藏链接要同时算上「显示文本」与「真实 URL」两个面
      parts.push(entity.url, text.slice(entity.offset, entity.offset + entity.length));
      continue;
    }
    parts.push(text.slice(entity.offset, entity.offset + entity.length));
  }
  return parts.join('\n');
}

/**
 * 转义 Telegram Markdown(V1) 的特殊字符。
 *
 * 文案模板里含 `**加粗**` 与 `` `代码` ``，所以必须以 Markdown 解析发送；
 * 而 `{name}` 之类的替换值完全来自用户（昵称里塞 `**` 或 `[` 都很常见），
 * 不转义会让消息直接发送失败（400 can't parse entities），
 * 或者更糟 —— 把用户内容渲染成我们没打算给的格式。
 */
export function escapeMarkdown(input: string): string {
  return input.replace(/[_*`\[]/g, (ch) => `\\${ch}`);
}

/** 单行化，用于把可能含换行的值塞进模板的一行里 */
export function singleLine(input: string): string {
  return input.replace(/\s*\n\s*/g, ' ').trim();
}
