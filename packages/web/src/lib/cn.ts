/**
 * 类名拼接。
 *
 * 刻意不引入 clsx + tailwind-merge 这一套：本项目里冲突的 Tailwind 类
 * 基本都通过「调用方传入的 className 覆盖默认值」这一条约定解决，
 * 为它多带两个依赖并不划算。这里只做「过滤假值 + 空格连接」。
 */
export function cn(...values: (string | false | null | undefined)[]): string {
  return values.filter(Boolean).join(' ');
}
