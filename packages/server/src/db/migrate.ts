import { existsSync } from 'node:fs';
import path from 'node:path';
import { migrate } from 'drizzle-orm/libsql/migrator';
import { logger } from '../core/logger.ts';
import { connectDb } from './client.ts';

const MIGRATIONS_DIR = path.join(import.meta.dirname, 'migrations');

/**
 * 应用迁移。
 *
 * 迁移文件由 `pnpm db:generate` 生成并提交进仓库 —— 启动时只做「应用」，
 * 不做「推断」，这样生产环境的 schema 变更永远是可审计的 SQL 文件。
 */
export async function runMigrations(): Promise<void> {
  if (!existsSync(MIGRATIONS_DIR)) {
    logger.warn(
      { dir: MIGRATIONS_DIR },
      '未找到迁移目录，跳过迁移。首次运行请执行 pnpm db:generate 生成初始迁移',
    );
    return;
  }

  const { db } = await connectDb();
  await migrate(db, { migrationsFolder: MIGRATIONS_DIR });
  logger.debug('数据库迁移已应用');
}

// 作为脚本直接运行时（pnpm db:migrate）才执行，作为模块被 import 时不执行。
const isDirectRun =
  process.argv[1] !== undefined &&
  path.resolve(process.argv[1]) === path.resolve(import.meta.filename);

if (isDirectRun) {
  runMigrations()
    .then(() => {
      logger.info('迁移完成');
    })
    .catch((err: unknown) => {
      logger.error({ err }, '迁移失败');
      process.exitCode = 1;
    });
}
