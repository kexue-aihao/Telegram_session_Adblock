import {
  createCipheriv,
  createDecipheriv,
  createHash,
  randomBytes,
  timingSafeEqual,
} from 'node:crypto';
import { loadEnv } from '../env.ts';

/**
 * Bot token 的静态加密。
 *
 * 用 AES-256-GCM 而非 CBC：GCM 自带认证标签，能同时保证机密性与完整性 ——
 * 有人改了库里的密文，解密会直接失败而不是解出一段垃圾去骚扰 Telegram。
 * 每次加密都用全新随机 IV（96 bit，GCM 的标准长度）。
 */

const ALGORITHM = 'aes-256-gcm';
const IV_BYTES = 12;

export interface SealedSecret {
  /** base64 密文 */
  cipher: string;
  /** base64 IV */
  iv: string;
  /** base64 GCM 认证标签 */
  tag: string;
}

export function sealSecret(plaintext: string): SealedSecret {
  const { MASTER_KEY } = loadEnv();
  const iv = randomBytes(IV_BYTES);
  const cipher = createCipheriv(ALGORITHM, MASTER_KEY, iv);
  const encrypted = Buffer.concat([cipher.update(plaintext, 'utf8'), cipher.final()]);
  return {
    cipher: encrypted.toString('base64'),
    iv: iv.toString('base64'),
    tag: cipher.getAuthTag().toString('base64'),
  };
}

export function openSecret(sealed: SealedSecret): string {
  const { MASTER_KEY } = loadEnv();
  const decipher = createDecipheriv(ALGORITHM, MASTER_KEY, Buffer.from(sealed.iv, 'base64'));
  decipher.setAuthTag(Buffer.from(sealed.tag, 'base64'));
  try {
    return Buffer.concat([
      decipher.update(Buffer.from(sealed.cipher, 'base64')),
      decipher.final(),
    ]).toString('utf8');
  } catch {
    // 认证失败只有两种可能：密文被篡改，或 MASTER_KEY 换了。
    // 后者是运维常见操作，必须给出可执行的指引而不是一句 "bad decrypt"。
    throw new Error(
      '无法解密 bot token —— MASTER_KEY 与写入时不一致，或密文已损坏。\n' +
        '若确实更换过 MASTER_KEY，请在面板中重新录入该机器人的 token。',
    );
  }
}

/**
 * 生成展示用掩码：`123456789:AAF…xY3`。
 * 明文 token 在任何 API 响应里都不出现，管理员靠掩码辨认是哪一个 bot。
 */
export function maskToken(plaintext: string): string {
  const colon = plaintext.indexOf(':');
  if (colon < 0) {
    return plaintext.length <= 8 ? '••••' : `${plaintext.slice(0, 4)}••••${plaintext.slice(-2)}`;
  }
  const id = plaintext.slice(0, colon);
  const secret = plaintext.slice(colon + 1);
  const head = secret.slice(0, 3);
  const tail = secret.slice(-3);
  return `${id}:${head}••••••${tail}`;
}

/** 会话 token 只存哈希；库被读走也无法直接冒用 */
export function hashToken(token: string): string {
  return createHash('sha256').update(token).digest('hex');
}

/** URL-safe 随机串，用于会话 token */
export function randomToken(bytes = 32): string {
  return randomBytes(bytes).toString('base64url');
}

/** 定长安全比较，避免用 `===` 比较密钥时泄露时序信息 */
export function safeEqual(a: string, b: string): boolean {
  const bufA = Buffer.from(a);
  const bufB = Buffer.from(b);
  if (bufA.length !== bufB.length) return false;
  return timingSafeEqual(bufA, bufB);
}
