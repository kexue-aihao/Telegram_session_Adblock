import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { createClient, type Client } from '@libsql/client';
import { drizzle, type LibSQLDatabase } from 'drizzle-orm/libsql';
import { loadEnv } from '../env.ts';
import { logger } from '../core/logger.ts';
import * as schema from './schema.ts';

export type Database = LibSQLDatabase<typeof schema>;

export interface Db {
  db: Database;
  client: Client;
  close: () => Promise<void>;
}

let instance: Db | null = null;

/**
 * libsql 用的是 `file:` URL，如果目录不存在它不会自动创建，
 * 只会在第一次写入时抛一个很难懂的错误。所以这里先建目录。
 */
function ensureDataDir(url: string): void {
  if (!url.startsWith('file:')) return;
  const filePath = url.slice('file:'.length).split('?')[0] ?? '';
  if (!filePath || filePath === ':memory:') return;
  const dir = path.dirname(path.resolve(filePath));
  mkdirSync(dir, { recursive: true });
}

export async function connectDb(): Promise<Db> {
  if (instance) return instance;

  const env = loadEnv();
  ensureDataDir(env.DATABASE_URL);

  const client = createClient({
    url: env.DATABASE_URL,
    // 本地文件库不需要 authToken；远程 libsql 才需要
    ...(process.env.DATABASE_AUTH_TOKEN ? { authToken: process.env.DATABASE_AUTH_TOKEN } : {}),
  });

  // WAL 让「中继写消息」与「面板读列表」不互相阻塞；
  // 外键约束默认是关的，必须显式打开，否则 onDelete: cascade 全部失效。
  // busy_timeout 应对并发写入时的 SQLITE_BUSY。
  await client.execute('PRAGMA journal_mode = WAL');
  await client.execute('PRAGMA foreign_keys = ON');
  await client.execute('PRAGMA busy_timeout = 5000');
  await client.execute('PRAGMA synchronous = NORMAL');

  const db = drizzle(client, { schema });

  logger.debug({ url: env.DATABASE_URL.replace(/:.*@/, ':***@') }, '数据库已连接');

  instance = {
    db,
    client,
    close: async () => {
      client.close();
      instance = null;
    },
  };
  return instance;
}

/** 已连接的前提下取数据库句柄；未初始化时抛错而不是返回 undefined */
export function getDb(): Db {
  if (!instance) {
    throw new Error('数据库尚未初始化 —— 请先 await connectDb()');
  }
  return instance;
}

export { schema };
