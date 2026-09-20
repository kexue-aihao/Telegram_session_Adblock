import { defineConfig } from 'drizzle-kit';

/**
 * 只用于 `drizzle-kit generate`（生成 SQL 迁移文件）与 `studio`。
 * 运行时的连接由 src/db/client.ts 负责。
 *
 * 方言选 `turso` 而不是 `sqlite`：项目用的是 @libsql/client 驱动，
 * turso 方言才对应 libsql。generate 是纯静态的，不会真的连库。
 */
export default defineConfig({
  dialect: 'turso',
  schema: './src/db/schema.ts',
  out: './src/db/migrations',
  dbCredentials: {
    url: process.env.DATABASE_URL ?? 'file:./data/app.db',
  },
  verbose: true,
  strict: true,
});
