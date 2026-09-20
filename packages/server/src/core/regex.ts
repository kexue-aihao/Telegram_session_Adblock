import vm from 'node:vm';
import safeRegex from 'safe-regex';
import type { MatchMode } from '@tgs/shared';
import { normalizeText } from './text.ts';

/**
 * 广告规则的匹配引擎。
 *
 * 这里要同时顶住两件事：
 *   1. 管理员从面板里手写的正则是**不可信输入**，写错一个 `(a+)+` 就能把
 *      整个进程卡死 —— 而进程里还挂着所有机器人的长轮询。
 *   2. 匹配发生在消息中继的热路径上，不能因为要防护就慢到影响转发。
 *
 * 防护分三层，任何一层单独都不够：
 *   - 静态：保存规则时用 safe-regex 拒掉嵌套量词这类已知的指数级模式；
 *   - 兜底：静态检查覆盖不到的写法（回溯爆炸但结构不嵌套）靠运行期超时截断；
 *   - 限额：模式长度与输入长度都设上限，把单次匹配的成本锁死在一个量级内。
 *
 * 超时用 `node:vm` 实现：正则的 exec 跑在独立 context 里，超时后 V8 会中断
 * 该段脚本。注意这是**尽力而为** —— V8 对正则的回溯中断不是每个版本都及时，
 * 所以它只是兜底，不是可以省掉静态检查的理由。
 */

/** 允许的正则标志。刻意不含 `d`（indices）与 `v`，它们对规则没有意义。 */
const ALLOWED_FLAGS = /^[gimsuy]*$/;

/** 单次匹配的输入上限；再长的消息截断后匹配，避免拿 20KB 去喂可能的坏正则 */
export const MAX_MATCH_INPUT = 8_000;

/** 单条规则在一次匹配里最多收集多少个命中片段（沙盒展示用） */
export const MAX_MATCHES = 500;

export interface RegexSafety {
  safe: boolean;
  reason: string | null;
}

export interface MatcherSpec {
  pattern: string;
  flags: string;
  matchMode: MatchMode;
}

export interface MatchSegment {
  start: number;
  end: number;
  text: string;
  groups: (string | null)[];
}

export interface MatchResult {
  matched: boolean;
  /** 第一个命中片段；用于审计记录里的 `matchedText` */
  matchedText: string;
  matches: MatchSegment[];
  truncated: boolean;
  timedOut: boolean;
  error: string | null;
  durationMs: number;
}

/**
 * 保存规则前的静态校验。
 *
 * 同时做三件事：标志白名单、能否编译、是否被 safe-regex 判为风险模式。
 * 顺序有意义 —— 编译不过的模式没必要再送去静态分析（safe-regex 内部
 * 用 regexp-tree 解析，遇到非法语法会抛异常）。
 */
export function checkPatternSafety(pattern: string, flags: string): RegexSafety {
  if (!ALLOWED_FLAGS.test(flags)) {
    return { safe: false, reason: '标志只允许 g i m s u y 这几个字母' };
  }
  if (pattern.length === 0) {
    return { safe: false, reason: '模式不能为空' };
  }
  if (pattern.length > 2000) {
    return { safe: false, reason: '模式过长（上限 2000 字符）' };
  }

  try {
    // 只编译，不执行 —— 语法错误的模式在这里就该被挡下
    new RegExp(pattern, flags);
  } catch (err) {
    return { safe: false, reason: `正则语法错误：${(err as Error).message}` };
  }

  let safe: boolean;
  try {
    safe = safeRegex(pattern);
  } catch (err) {
    // safe-regex 解析不了（通常是用了它不支持的语法）时**放行**：
    // 拒绝一种合法写法比放过一个可疑写法对管理员的伤害更大，
    // 运行期超时仍然兜着。
    return {
      safe: true,
      reason: `未能静态分析（${(err as Error).message}），将依赖运行期超时保护`,
    };
  }

  if (!safe) {
    return {
      safe: false,
      reason: '检测到可能灾难性回溯的模式（嵌套量词 / 重复的选择分支），请改写后重试',
    };
  }
  return { safe: true, reason: null };
}

/** 转义正则元字符，供 `contains` / `whole_word` 把字面量当模式用 */
export function escapeRegexLiteral(literal: string): string {
  return literal.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

interface Compiled {
  regex: RegExp | null;
  /** 非正则模式（contains / whole_word）走字符串查找，无需 vm */
  literal: string | null;
  error: string | null;
}

/**
 * 把规则编译成可复用的执行形态。`contains` / `whole_word` 也统一走
 * 正则路径：`whole_word` 需要词边界断言，用正则表达最直接，
 * 而 `contains` 走 indexOf 更快，留字面量分支。
 */
export function compileMatcher(spec: MatcherSpec): Compiled {
  try {
    if (spec.matchMode === 'contains') {
      return { regex: null, literal: spec.pattern, error: null };
    }
    if (spec.matchMode === 'whole_word') {
      // \b 对中文无意义（中文没有词边界），所以只在两侧是「非字母数字下划线」
      // 时判定命中。对 `vx`、`QQ` 这类英数词足够，也不会误伤中文里的正常词。
      //
      // 必须保留/补上 `g`：下面的枚举靠 exec 循环推进 lastIndex，
      // 非全局的 exec 每次都返回同一个首个命中，会原地死循环到 max。
      const base = spec.flags.replace(/g/g, '');
      return {
        regex: new RegExp(`(?<![\\w])${escapeRegexLiteral(spec.pattern)}(?![\\w])`, `${base}g`),
        literal: null,
        error: null,
      };
    }
    // 正则模式统一补 `g`：下面靠 exec 循环枚举全部命中，非全局会原地死循环
    const flags = spec.flags.includes('g') ? spec.flags : `${spec.flags}g`;
    return { regex: new RegExp(spec.pattern, flags), literal: null, error: null };
  } catch (err) {
    return { regex: null, literal: null, error: (err as Error).message };
  }
}

/**
 * 在独立 vm context 里枚举全部命中，超时即中断。
 *
 * 脚本用 `function` 而非箭头函数、变量挂在 context 上而不是闭包捕获：
 * 这段代码是在**另一个 realm** 里跑的，闭包不会跟着过去。
 */
const ENUMERATE = new vm.Script(`
  (function () {
    var re = __re, s = __input, max = __max, out = [];
    var m;
    while ((m = re.exec(s)) !== null) {
      out.push({ index: m.index, match: m[0], groups: m.slice(1) });
      if (out.length >= max) break;
      // 零宽命中会让 lastIndex 原地踏步 —— 不推进就会无限循环
      if (m[0].length === 0) re.lastIndex += 1;
    }
    return out;
  })()
`);

interface RawMatch {
  index: number;
  match: string;
  groups: (string | null)[];
}

/**
 * 执行一次匹配。永不抛异常 —— 出错以 `error` 字段返回，
 * 因为规则引擎跑在消息中继路径上，一条坏规则不该让用户的消息丢失。
 */
export function runMatcher(spec: MatcherSpec, input: string, timeoutMs: number): MatchResult {
  const startedAt = performance.now();
  const bounded = input.length > MAX_MATCH_INPUT ? input.slice(0, MAX_MATCH_INPUT) : input;

  const compiled = compileMatcher(spec);
  if (compiled.error !== null) {
    return {
      matched: false,
      matchedText: '',
      matches: [],
      truncated: false,
      timedOut: false,
      error: compiled.error,
      durationMs: performance.now() - startedAt,
    };
  }

  if (compiled.literal !== null) {
    return finishContains(compiled.literal, spec.flags, bounded, startedAt);
  }

  const regex = compiled.regex as RegExp;
  regex.lastIndex = 0;

  try {
    const context = vm.createContext({ __re: regex, __input: bounded, __max: MAX_MATCHES });
    const raw = ENUMERATE.runInContext(context, { timeout: timeoutMs }) as RawMatch[];
    const segments: MatchSegment[] = raw.map((m) => ({
      start: m.index,
      end: m.index + m.match.length,
      text: m.match,
      groups: m.groups ?? [],
    }));
    return {
      matched: segments.length > 0,
      matchedText: segments[0]?.text ?? '',
      matches: segments,
      truncated: segments.length >= MAX_MATCHES,
      timedOut: false,
      error: null,
      durationMs: performance.now() - startedAt,
    };
  } catch (err) {
    const message = (err as Error).message ?? String(err);
    const timedOut = message.includes('Script execution timed out');
    return {
      matched: false,
      matchedText: '',
      matches: [],
      truncated: false,
      timedOut,
      // 超时不是「规则写错了」而是「规则危险」，措辞上要能区分，
      // 面板据此决定是否自动停用该规则。
      error: timedOut ? null : message,
      durationMs: performance.now() - startedAt,
    };
  }
}

function finishContains(
  literal: string,
  flags: string,
  input: string,
  startedAt: number,
): MatchResult {
  const caseInsensitive = flags.includes('i');
  const haystack = caseInsensitive ? input.toLowerCase() : input;
  const needle = caseInsensitive ? literal.toLowerCase() : literal;

  const matches: MatchSegment[] = [];
  let from = 0;
  while (matches.length < MAX_MATCHES) {
    const at = haystack.indexOf(needle, from);
    if (at < 0) break;
    matches.push({
      start: at,
      end: at + needle.length,
      // 从原文切片，保留原始大小写
      text: input.slice(at, at + needle.length),
      groups: [],
    });
    from = at + Math.max(needle.length, 1);
  }

  return {
    matched: matches.length > 0,
    matchedText: matches[0]?.text ?? '',
    matches,
    truncated: matches.length >= MAX_MATCHES,
    timedOut: false,
    error: null,
    durationMs: performance.now() - startedAt,
  };
}

/**
 * 归一化后匹配的便捷封装。
 *
 * 规则始终匹配**归一化文本**，因为归一化的意义就是让变体塌缩；
 * 若改回匹配原文，`加​微信` 这类绕过会瞬间全部生效。
 */
export function runMatcherNormalized(
  spec: MatcherSpec,
  rawInput: string,
  timeoutMs: number,
): MatchResult & { normalized: string } {
  const normalized = normalizeText(rawInput);
  return { ...runMatcher(spec, normalized, timeoutMs), normalized };
}
