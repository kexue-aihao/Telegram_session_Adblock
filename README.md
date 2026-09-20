# Telegram 话题式会话管理器 + 广告审计面板

> ## ⚠️ 当前技术栈
>
> 仓库里同时存在两代实现，**当前在用的是 Go + Vue 那一套**：
>
> | 目录 | 状态 |
> |---|---|
> | `packages/server-go` | ✅ **当前后端**（Go，静态二进制） |
> | `packages/web-vue` | ✅ **当前前端**（Vue 3 + Vite） |
> | `packages/server` | ⚠️ 初版 Node 后端，已被取代，保留作参考 |
> | `packages/web` | ⚠️ 初版 React 前端，已被取代，保留作参考 |
> | `packages/shared` | ⚠️ 仅供初版使用；Go 版不复用（Go 与 TS 无法共享类型） |
>
> 本 README 的**架构与部署部分描述的是 Go + Vue 这一套**。
> 初版两代实现并存是为了保留可对照的实现细节，不建议据此部署。

把任意数量的 Telegram 机器人托管在一个自托管面板里。每个机器人在一个**管理超级群**中为每个私聊用户自动开一个 **Forum Topic**：用户私聊 → 转发进话题；管理员在话题内回复 → 转发回用户私聊。一人一话题，自动建、自动归档。

内置**正则广告审计规则引擎**：面板自定义规则、命中即拦截、完整审计日志、四档阶梯处罚。

```
用户私聊 ──┐                      ┌── 管理员在话题里回复
           ├─► 规则引擎 ──► 话题 ──┤
广告消息 ──┘   （命中即拦截）      └── 面板实时查看与代发
```

---

## 特性

**中继**
- 一人一话题，用户首次私聊或 `/start` 时自动创建
- 相册（媒体组）保持原形态转发，不会被拆成 N 条消息
- 编辑与删除双向镜像
- 同一用户的消息串行处理，话题里的顺序与实际发送顺序一致
- 话题闲置超过配置时长自动关闭；用户再来消息时自动重开

**广告拦截**
- 匹配前归一化：NFKC 全角折叠、去零宽字符、同形字（西里尔 `а`→`a`）、去中文间的分隔符噪声
- 同时检查 `text_link` 里的隐藏链接 —— 显示文本正常、URL 藏在 entity 里是最常见的绕过手法
- **ReDoS 三层防护**：保存时 `safe-regex` 静态校验、运行期 `node:vm` 硬超时、超时后自动停用该规则并告警
- 命中多条规则时每条都记审计，动作取并集只执行一次
- 面板内置正则测试沙盒：贴入真实广告文案即时高亮命中位置

**阶梯处罚**（用户不在管理群里，Telegram 原生禁言用不了，因此处罚在机器人层级实现）

| 累计违规分 | 处置 |
|---|---|
| 1 | 私聊警告 |
| 3 | 静默 —— 消息不再进话题，用户无感知 |
| 5 | 硬禁言 24 小时 —— 回复禁言提示与解禁时间 |
| 8 | 拉黑 —— 机器人不再响应，话题自动关闭 |

阈值、文案、衰减周期全部在面板里可改。违规分默认 7 天无违规后减半。

**面板**
- 暗色优先的控制台界面，Light / Dark 双主题
- WebSocket 实时推送：会话列表、聊天记录、审计流、机器人状态
- `⌘K` 命令面板
- 全部文案中文

---

## 快速开始

### 前置条件

- **Node.js ≥ 22.6**（推荐 24；服务端由 Node 原生执行 TypeScript，没有编译步骤）
- **pnpm 12+**
- 一个 Telegram 账号

### 1. 准备管理群

这一步是 Telegram 侧的硬性前提，**必须先做**，否则机器人无法创建话题。

1. 新建一个**私有超级群**
2. 群设置 → **话题（Topics）** → 开启
3. 在 @BotFather 里创建机器人（`/newbot`），拿到 token
4. **关闭 Privacy Mode**：对 @BotFather 发送 `/setprivacy` → 选择你的机器人 → **Disable**
   > 不关的话机器人读不到群里的普通消息，管理员回复永远不会被转发给用户。这是最常见的一个坑。
5. 把机器人拉进群，**设为管理员**，至少勾选：
   - ✅ 管理话题
   - ✅ 删除消息
6. 拿到群 ID：把群里任意一条消息转发给 @userinfobot，或看群的邀请链接

### 2. 配置环境变量

```bash
cp .env.example .env
```

生成两个必需的密钥：

```bash
node -e "console.log('MASTER_KEY=' + require('crypto').randomBytes(32).toString('hex'))"
node -e "console.log('SESSION_SECRET=' + require('crypto').randomBytes(48).toString('hex'))"
```

把它们和 `ADMIN_PASSWORD`（至少 8 位、含字母和数字）填进 `.env`。

> ⚠️ **`MASTER_KEY` 一旦更换，库里所有 bot token 都会解不开**，需要到面板里逐个重新录入。请务必备份。

### 3. 启动

```bash
pnpm install
pnpm dev
```

打开 <http://localhost:5173>，用 `ADMIN_PASSWORD` 登录。

`pnpm dev` 会并发启动服务端（8787，`tsx watch` 热重载）与前端（5173，Vite HMR，已把 `/api` 与 `/ws` 代理到后端）。

### 4. 添加机器人

面板 →「机器人」→「创建机器人」，三步：

1. 粘贴 BotFather 给的 token → 点「验证 Token」
   - 如果提示处于 Privacy Mode，回到第 1 步的 4 处理
2. 填入管理群 ID → 点「体检这个群」
   - 体检会逐项列出缺什么权限，以及怎么补
3. 完成

用另一个 Telegram 账号私聊这个机器人，管理员群里应该立刻出现一个以该用户命名的话题。

---

## 生产部署

### Docker Compose

```bash
# .env 里填好 MASTER_KEY / SESSION_SECRET / ADMIN_PASSWORD
docker compose up -d
```

面板监听 `127.0.0.1:8787`（只绑回环）。用 Caddy 反代并自动签发证书：

```caddyfile
panel.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

> Caddy 默认会转发 WebSocket，无需额外配置。若用 Nginx，记得补 `Upgrade` 与 `Connection` 头。

数据全部在 `tgs-data` 卷里的 `app.db` 一个文件。备份就是复制它：

```bash
docker compose exec panel node -e "process.stdout.write('')"  # 确认存活
docker run --rm -v tgs-data:/data -v "$PWD:/backup" alpine \
  cp /data/app.db /backup/app-$(date +%F).db
```

### 不用 Docker

```bash
pnpm install
pnpm build          # 构建前端到 packages/web/dist
pnpm start          # 生产模式启动，server 直接托管前端产物
```

放进 systemd：

```ini
[Unit]
Description=Telegram Session Adblock
After=network-online.target

[Service]
Type=simple
User=tgs
WorkingDirectory=/opt/telegram-session-adblock
ExecStart=/usr/bin/node packages/server/src/index.ts
Restart=always
RestartSec=5
EnvironmentFile=/opt/telegram-session-adblock/.env

[Install]
WantedBy=multi-user.target
```

---

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `MASTER_KEY` | ✅ | — | 加密 bot token 的 32 字节主密钥（hex 或 base64） |
| `SESSION_SECRET` | ✅ | — | 签名会话 Cookie，至少 32 字符 |
| `ADMIN_PASSWORD` | 首次 | — | 仅在库里还没有管理员账号时使用 |
| `ADMIN_USERNAME` | | `admin` | 管理员用户名 |
| `DATABASE_URL` | | `file:./data/app.db` | SQLite 文件路径 |
| `HOST` / `PORT` | | `0.0.0.0` / `8787` | 监听地址 |
| `TZ` | | `Asia/Shanghai` | 影响统计切日与时间展示 |
| `LOG_LEVEL` | | `info` | `fatal` `error` `warn` `info` `debug` `trace` |
| `PUBLIC_URL` | | — | 设置后改用 webhook；不设置则用长轮询 |
| `WEB_DIST` | | `packages/web/dist` | 前端产物目录 |

---

## 常用操作

**在话题里**（置顶消息上有等价按钮）

```
/close    关闭当前话题
/reopen   重新打开
/ban      拉黑该用户并关闭话题
/unban    解除拉黑
/reset    清零违规分并解除全部处罚
/info     查看该用户档案
/rules    规则引擎概况
```

**排查「消息没转过去」**

按顺序检查：

1. 面板 →「机器人」→「群体检」—— 它会直接告诉你缺哪一项权限
2. 面板 →「审计」→「广告命中」—— 消息可能被规则拦了
3. 用户是否被静默 / 禁言 / 拉黑（会话详情页顶部会显示生效中的处罚）
4. 是否触发了刷屏阈值（3 秒内超过 8 条）

**写了危险的正则**

保存时会被 `safe-regex` 拒绝。如果侥幸存进去并在运行期超时，引擎会自动停用该规则、在话题里告警、并在仪表盘上标红。到「规则」页用沙盒改写它。

**忘记管理员密码**

删掉数据库里的管理员记录后重启（`ADMIN_PASSWORD` 会重新播种）：

```bash
sqlite3 data/app.db "DELETE FROM admin_users;"
```

---

## 项目结构

```
packages/
├─ shared/   前后端共用的契约层：zod schema、推导类型、WS 事件
├─ server/   Fastify + grammY 多机器人运行时 + 规则引擎
└─ web/      React 19 + Vite + Tailwind v4 面板
```

服务端没有构建步骤 —— Node 24 原生执行 TypeScript，`tsconfig` 开了 `erasableSyntaxOnly` 强制只使用可擦除的语法（禁用 `enum` / `namespace` / 构造函数参数属性）。

**依赖上的两条硬约束**

1. **不允许任何需要 `node-gyp` 的依赖。** Windows 上没有 Visual Studio Build Tools，一旦引入编译型依赖，本地开发与 Docker 构建都会直接失败。因此 SQLite 用 `@libsql/client`（预编译二进制）而不是 `better-sqlite3`，密码哈希用 `@node-rs/argon2` 而不是 `argon2`。
2. **不引入 `re2`。** 它的原生模块需要编译回退，且不支持前后瞻，会拒掉大量合理的广告正则。改用 `safe-regex` 静态校验 + `node:vm` 硬超时。

---

## 已知限制

- **删除镜像做不到。** Bot API 不推送"消息被删除"这类更新，机器人无法知道用户或管理员在客户端里删了哪条消息。删除只在本方主动删除时镜像。
- **用户不在管理群里，`restrictChatMember` / `banChatMember` 用不了。** 这是私聊中继模式的固有约束，阶梯处罚因此在机器人层级实现。若用户恰好也是群成员，可以额外叠加 Telegram 原生处罚。
- **单实例部署。** 事件总线是进程内内存实现（`packages/server/src/core/bus.ts`）。要多实例的话替换这一个文件即可，但 SQLite 本身也需要换成网络数据库。
- **Telegram 只能删除 48 小时内的消息。** 超时后规则命中的"删除原消息"会失败，审计里记为 `delete_failed`。
