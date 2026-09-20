import { z } from 'zod';

export const loginInputSchema = z.object({
  password: z.string().min(1).max(256),
});
export type LoginInput = z.infer<typeof loginInputSchema>;

export const adminSessionSchema = z.object({
  username: z.string(),
  createdAt: z.number().int(),
  expiresAt: z.number().int(),
  ip: z.string().nullable(),
  userAgent: z.string().nullable(),
});
export type AdminSession = z.infer<typeof adminSessionSchema>;

export const loginResultSchema = z.object({
  ok: z.boolean(),
  session: adminSessionSchema.nullable(),
  /** 失败原因；失败时用于前端提示，注意不要把「密码错误」和「账号不存在」区分开 */
  error: z.string().nullable(),
  /** 触发锁定时的剩余等待秒数 */
  lockedForSeconds: z.number().int().nullable(),
  /** 剩余可尝试次数 */
  attemptsLeft: z.number().int().nullable(),
});
export type LoginResult = z.infer<typeof loginResultSchema>;

export const changePasswordInputSchema = z.object({
  currentPassword: z.string().min(1).max(256),
  newPassword: z
    .string()
    .min(8, '新密码至少 8 位')
    .max(256, '新密码过长')
    .refine((v) => /[a-zA-Z]/.test(v) && /[0-9]/.test(v), {
      message: '新密码需要同时包含字母和数字',
    }),
});
export type ChangePasswordInput = z.infer<typeof changePasswordInputSchema>;
