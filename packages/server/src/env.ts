import { existsSync } from 'node:fs';
import path from 'node:path';
import { z } from 'zod';

/**
 * 环境变量校验 —— 启动的第一道关卡。
 *
 * 刻意 fail-fast：宁可进程立刻退出并打印「怎么修」，也不要在运行到
 * 解密 bot token 时才发现 MASTER_KEY 不对。所有缺失项一次性汇总报出，
 * 避免修一个报一个。
 */

const REPO_ROOT = path.resolve(import.meta.dirname, '../../..');

/**
 * 按优先级找 .env：项目根目录 → server 包目录 → 当前工作目录。
 * 只加载第一个存在的文件，避免多处配置互相覆盖造成「改了没生效」。
 */
function loadEnvFile(): string | null {
  const candidates = [
    path.join(REPO_ROOT, '.env'),
    path.join(import.meta.dirname, '../../../.env'),
    path.join(process.cwd(), '.env'),
  ];
  for (const file of candidates) {
    if (existsSync(file)) {
      // loadEnvFile 不会覆盖已存在的 process.env，因此真实环境变量优先级更高
      process.loadEnvFile(file);
      return file;
    }
  }
  return null;
}

/**
 * 把 MASTER_KEY 规整成 32 字节。
 *
 * 只接受两种可验证长度的编码，不接受「任意字符串」：
 * 如果允许任意字符串，运维很可能填一个短口令，AES-256 的强度就名存实亡。
 */
function parseMasterKey(raw: string): Buffer {
  const trimmed = raw.trim();

  if (/^[0-9a-fA-F]{64}$/.test(trimmed)) {
    return Buffer.from(trimmed, 'hex');
  }

  // 标准 base64（含 padding 或 44 字符无 padding）解出 32 字节
  if (/^[A-Za-z0-9+/]{43}=?$/.test(trimmed)) {
    const buf = Buffer.from(trimmed, 'base64');
    if (buf.length === 32) return buf;
  }

  throw new Error(
    'MASTER_KEY 必须是 64 位十六进制字符，或编码 32 字节的 base64。\n' +
      '生成一个：node -e "console.log(require(\'crypto\').randomBytes(32).toString(\'hex\'))"',
  );
}

const rawSchema = z.object({
  NODE_ENV: z.enum(['development', 'production', 'test']).default('development'),
  HOST: z.string().default('0.0.0.0'),
  PORT: z.coerce.number().int().min(1).max(65_535).default(8787),
  LOG_LEVEL: z.enum(['fatal', 'error', 'warn', 'info', 'debug', 'trace']).default('info'),
  TZ: z.string().default('Asia/Shanghai'),

  /** 本地 SQLite 文件；生产用 file:/data/app.db 挂卷 */
  DATABASE_URL: z.string().default('file:./data/app.db'),

  /** 加密 bot token 的主密钥（32 字节） */
  MASTER_KEY: z.string().min(1, 'MASTER_KEY 未设置 —— 没有它无法存取 bot token'),
  /** 签名会话 Cookie 的密钥，至少 32 字符 */
  SESSION_SECRET: z.string().min(32, 'SESSION_SECRET 至少 32 个字符'),
  /** 首次启动用它播种管理员密码；之后改自 WebUI */
  ADMIN_PASSWORD: z
    .string()
    .min(8, 'ADMIN_PASSWORD 至少 8 位')
    .refine((v) => /[a-zA-Z]/.test(v) && /[0-9]/.test(v), 'ADMIN_PASSWORD 需同时包含字母和数字')
    .optional(),
  ADMIN_USERNAME: z.string().default('admin'),

  /** 设置后切换为 webhook 模式；为空则用长轮询 */
  PUBLIC_URL: z.string().url().optional(),
  /** webhook 模式的监听路径与密钥 */
  WEBHOOK_PATH: z.string().default('/tg/webhook'),
  WEBHOOK_SECRET: z.string().optional(),

  /** 面板托管的前端产物目录（生产构建后由 server 直接托管） */
  WEB_DIST: z.string().optional(),
});

export interface Env {
  NODE_ENV: 'development' | 'production' | 'test';
  isProduction: boolean;
  isDevelopment: boolean;
  HOST: string;
  PORT: number;
  LOG_LEVEL: 'fatal' | 'error' | 'warn' | 'info' | 'debug' | 'trace';
  TZ: string;
  DATABASE_URL: string;
  MASTER_KEY: Buffer;
  SESSION_SECRET: string;
  ADMIN_PASSWORD: string | undefined;
  ADMIN_USERNAME: string;
  PUBLIC_URL: string | undefined;
  WEBHOOK_PATH: string;
  WEBHOOK_SECRET: string | undefined;
  WEB_DIST: string | undefined;
  /** .env 实际加载路径，仅用于启动日志 */
  envFile: string | null;
}

let cached: Env | null = null;

export function loadEnv(): Env {
  if (cached) return cached;

  const envFile = loadEnvFile();
  const parsed = rawSchema.safeParse(process.env);

  if (!parsed.success) {
    const lines = parsed.error.issues.map((i) => `  • ${i.path.join('.') || '(root)'}: ${i.message}`);
    throw new Error(
      `环境变量校验失败${envFile ? `（已读取 ${envFile}）` : '（未找到 .env，请从 .env.example 复制）'}:\n` +
        lines.join('\n'),
    );
  }

  const data = parsed.data;

  // TZ 必须在任何 Date 格式化之前生效，否则统计会按 UTC 切日
  process.env.TZ = data.TZ;

  // 逐字段显式构造而不是 `...data`：zod 的 `.optional()` 会把字段推成
  // 「可选属性」，而 Env 里它们是「必填但可为 undefined」，展开赋值无法通过
  // 类型检查。显式列出还能在新增环境变量时强制同步更新 Env 接口。
  const env: Env = {
    NODE_ENV: data.NODE_ENV,
    isProduction: data.NODE_ENV === 'production',
    isDevelopment: data.NODE_ENV === 'development',
    HOST: data.HOST,
    PORT: data.PORT,
    LOG_LEVEL: data.LOG_LEVEL,
    TZ: data.TZ,
    DATABASE_URL: data.DATABASE_URL,
    MASTER_KEY: parseMasterKey(data.MASTER_KEY),
    SESSION_SECRET: data.SESSION_SECRET,
    ADMIN_PASSWORD: data.ADMIN_PASSWORD,
    ADMIN_USERNAME: data.ADMIN_USERNAME,
    PUBLIC_URL: data.PUBLIC_URL,
    WEBHOOK_PATH: data.WEBHOOK_PATH,
    WEBHOOK_SECRET: data.WEBHOOK_SECRET,
    WEB_DIST: data.WEB_DIST,
    envFile,
  };

  cached = env;
  return env;
}
