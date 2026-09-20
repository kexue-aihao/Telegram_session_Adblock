/**
 * @tgs/shared —— 服务端与前端共用的契约层。
 *
 * 这里只放「两侧都必须认同」的东西：领域词汇、zod schema、推导类型、WS 事件。
 * 任何一侧私有的逻辑都不要放进来，否则前端打包体积会被服务端依赖污染。
 */

export * from './constants.ts';
export * from './defaults.ts';
export * from './events.ts';

export * from './schemas/audit.ts';
export * from './schemas/auth.ts';
export * from './schemas/bot.ts';
export * from './schemas/contact.ts';
export * from './schemas/message.ts';
export * from './schemas/rule.ts';
export * from './schemas/settings.ts';
export * from './schemas/stats.ts';
