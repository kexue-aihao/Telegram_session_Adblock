import { hash, verify } from '@node-rs/argon2';

/**
 * 管理员密码哈希。
 *
 * 用 @node-rs/argon2 而不是 `argon2` 包：后者依赖 node-gyp-build，
 * Windows 上没有 Visual Studio Build Tools 就会回退到本地编译并失败；
 * @node-rs/argon2 直接下发 win32-x64-msvc / linux-x64-gnu 预编译二进制。
 */

/**
 * `Algorithm.Argon2id` 的字面值。
 * @node-rs/argon2 把 Algorithm 声明成了 ambient const enum，
 * 而本项目开了 isolatedModules —— 使用 ambient const enum 会直接报错，
 * 所以这里写字面量 2 并在此说明出处。
 */
const ARGON2ID = 2;

/** OWASP 对 Argon2id 的推荐参数：19 MiB 内存、2 轮、单线程 */
const OPTIONS = {
  algorithm: ARGON2ID,
  memoryCost: 19_456,
  timeCost: 2,
  parallelism: 1,
} as const;

export async function hashPassword(password: string): Promise<string> {
  return hash(password, OPTIONS);
}

/**
 * 校验密码。注意这里**不抛错**，只返回布尔值 ——
 * 调用方需要区分「密码错」与「哈希串损坏」，而前者是常态路径。
 */
export async function verifyPassword(hashed: string, password: string): Promise<boolean> {
  try {
    return await verify(hashed, password, OPTIONS);
  } catch {
    return false;
  }
}
