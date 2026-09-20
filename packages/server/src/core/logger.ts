import { pino } from 'pino';
import { loadEnv } from '../env.ts';

const env = loadEnv();

/**
 * 结构化日志。
 *
 * 开发期用 pino-pretty 只是为了可读性；生产输出裸 JSON 交给采集器。
 * `redact` 是硬性要求：bot token 会出现在 grammY 的错误上下文里，
 * 一旦被打进日志盘就等于明文落盘，加密存储就白做了。
 */
export const logger = pino({
  level: env.LOG_LEVEL,
  base: { service: 'tgs-server' },
  redact: {
    paths: [
      'token',
      '*.token',
      'req.headers.authorization',
      'req.headers.cookie',
      'res.headers["set-cookie"]',
      'botToken',
      '*.botToken',
      'password',
      '*.password',
    ],
    censor: '[REDACTED]',
  },
  ...(env.isDevelopment
    ? {
        transport: {
          target: 'pino-pretty',
          options: {
            colorize: true,
            translateTime: 'HH:MM:ss.l',
            ignore: 'pid,hostname,service',
            singleLine: false,
          },
        },
      }
    : {}),
});

export type Logger = typeof logger;
