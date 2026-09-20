import { channel, type ActorType } from '@tgs/shared';
import { getDb } from '../db/client.ts';
import { auditLog } from '../db/schema.ts';
import { publish } from './bus.ts';
import { logger } from './logger.ts';

/**
 * 面板操作审计（登录、改规则、拉黑……）。
 *
 * 与 `rule_hits`（广告命中审计）刻意分表：两者的写入频率、查询方式、
 * 保留策略都不一样。命中记录是「机器人的工作日志」，动辄几十万行；
 * 这里记录的是「人做了什么」，量小但必须永久可查。
 */

export interface AuditInput {
  actorType: ActorType;
  /** 管理员用户名 / 机器人 id / `system` */
  actorId?: string | null;
  /** 取自 ADMIN_ACTIONS，但允许自定义字符串以便将来扩展 */
  action: string;
  targetType?: string | null;
  targetId?: string | number | null;
  detail?: Record<string, unknown> | null;
  ip?: string | null;
}

/**
 * 写一条审计。
 *
 * **永不抛错**：审计是旁路，不能因为「日志写不进去」就让「拉黑用户」失败。
 * 写失败时降级到应用日志，至少不会彻底丢失线索。
 */
export async function recordAudit(input: AuditInput): Promise<void> {
  const now = Date.now();
  try {
    const { db } = getDb();
    const inserted = await db
      .insert(auditLog)
      .values({
        actorType: input.actorType,
        actorId: input.actorId ?? null,
        action: input.action,
        targetType: input.targetType ?? null,
        targetId: input.targetId === null || input.targetId === undefined ? null : String(input.targetId),
        detail: input.detail ?? null,
        ip: input.ip ?? null,
        createdAt: now,
      })
      .returning({ id: auditLog.id });

    const id = inserted[0]?.id;
    if (id !== undefined) {
      publish(channel.global, 'audit.new', {
        id,
        action: input.action,
        actorType: input.actorType,
        createdAt: now,
      });
    }
  } catch (err) {
    logger.error({ err, audit: input.action }, '写审计日志失败（已忽略，不影响主流程）');
  }
}
