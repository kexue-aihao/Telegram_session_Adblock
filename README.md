# Telegram 话题式会话管理器 + 广告审计面板

把任意数量的 Telegram 机器人托管在一个自托管面板里。

每个机器人在一个**管理超级群**中，为每个私聊它的用户自动开一个 **Forum Topic**。
用户私聊 → 消息进话题；管理员在话题里回复 → 回到用户私聊。
一人一话题，自动建、自动归档，用户全程看不到群，管理员不用加好友。

内置**正则广告审计规则引擎**：命中即拦截、完整审计留痕、四档阶梯处罚。

```
用户私聊 ──┐                        ┌── 管理员在话题里回复
           ├─► 规则引擎 ──► 话题 ───┤
广告消息 ──┘   （命中即拦截）        └── 面板实时查看与代发
```

---

## 一键部署

在服务器上执行（需要 root）：

```bash
curl -fsSL https://raw.githubusercontent.com/kexue-aihao/Telegram_session_Adblock/master/scripts/deploy.sh | sudo bash
```

脚本会自动判断该走哪条路：

| 检测到的状态 | 行为 |
|---|---|
| 无容器、无数据、无配置 | **首次安装** —— 生成密钥、询问管理员密码、拉镜像、启动 |
| 三者**任一**存在 | **升级** —— 拉新镜像、重启容器，数据与密钥原样保留 |

判断用三者取并集而不是只看容器：容器可能被人手动删了而数据还在，那时若走首次安装路径会重新生成 `MASTER_KEY`，把库里已有的 bot token 全部作废。

跑完后它会打印**反向代理该填什么**，以及后续常用命令。

> ### ⚠️ `MASTER_KEY` 是唯一不可再生的东西
>
> 它用来加密库里存的 bot token。一旦变化，所有已保存的 token 都解不开，
> 表现为面板上每个机器人都报「无法解密 bot token」，必须逐个重新录入。
>
> 脚本在任何情况下都不会覆盖它，升级前还会把 `.env` 备份成 `.env.bak.<时间戳>`。
> **请把 `.env` 一并纳入你的备份。**

<details>
<summary>可选参数</summary>

```bash
--port PORT          监听端口（默认 8787）
--bind ADDR          绑定的宿主地址（默认 127.0.0.1，只对本机可见）
--tag TAG            镜像标签（默认 latest，可指定如 0.2.0）
--dir DIR            数据目录
--admin-password P   首次安装的管理员密码（不传则交互式询问）
--admin-username U   管理员用户名（默认 admin）
--yes                不询问，全部用默认值
--uninstall          卸载容器，保留数据
--purge              卸载并删除数据（不可恢复，必须人工确认）
```

数据目录默认值：检测到 1Panel 时用 `/opt/1panel/apps/telegram-session-adblock`，否则用 `/opt/tgs`。

重复执行本脚本是安全的。

</details>

**需要的前置条件**：一台装了 Docker 的 Linux 服务器，以及一个 Telegram 账号。别的都不需要。

---

## 特性

### 中继

- **一人一话题**，用户首次私聊或 `/start` 时自动创建
- **相册保持原形态**转发 —— 一组图仍是一组图，不会被拆成 N 条独立消息
- **编辑双向镜像**，管理员改了话题里的消息，用户那边同步更新
- **同一用户的消息串行处理**，话题里的顺序与实际发送顺序一致
  （并发处理更新时，先发的消息完全可能后落地，聊天场景里顺序即语义）
- 话题闲置超过配置时长自动关闭；用户再来消息时自动重开
- 刷屏检测：短时间连发超阈值时合并提示，而不是刷爆话题

### 广告拦截

- **归一化后再匹配**：NFKC 全角折叠、去零宽字符、同形字（西里尔 `а`→`a`）、去中文之间的分隔符噪声
  ——`加-微-信`、`加 微 信`、`加<零宽>微信` 都会塌缩成同一个形态
- **检查 entity 里的隐藏链接**：显示文本是「点击查看」、真实 URL 藏在 `text_link` 里，
  是最常见的绕过手法，只看正文完全看不出来
- **命中多条规则时每条都记审计，动作取并集只执行一次**
- 面板内置**正则测试沙盒**：贴入真实广告文案，即时高亮命中位置并显示归一化后的文本

> **关于 ReDoS**：Go 的 `regexp` 是 RE2，用 Thompson NFA 模拟，匹配耗时与输入长度成**线性**关系 ——
> 灾难性回溯在结构上不可能发生。因此这里**没有**正则超时、熔断、静态风险分析那一套，
> 它们解决的是一个不存在的问题。
>
> 代价是 RE2 不支持前后瞻与反向引用。`whole_word` 模式因此改为手工判断词边界，
> 而不是用 `(?<![\w])`。

### 阶梯处罚

终端用户不在管理群里，Telegram 原生的 `restrictChatMember` / `banChatMember` 对他用不了。
所以处罚做在机器人层级：

| 累计违规分 | 处置 |
|---|---|
| 1 | 私聊警告 |
| 3 | **静默** —— 消息不再进话题，用户无感知 |
| 5 | **硬禁言 24 小时** —— 回复禁言提示与解禁时间 |
| 8 | **拉黑** —— 机器人不再响应，话题自动关闭 |

阈值、文案、衰减周期全部可在面板改。违规分默认 7 天无违规后减半。

设计上有一点值得说明：**静默排在禁言之前**。它是这个中继模型下最有效的一档 ——
一旦回复用户「你被静默了」，等于告诉对方「换个号再来」。

### 面板

- 暗色优先的控制台界面，**磨砂玻璃**质感，Light / Dark 双主题
- **WebSocket 实时推送**：新消息、命中、机器人状态、会话列表
- `⌘K` 命令面板
- 七个页面：仪表盘 / 机器人 / 会话 / 规则 / 审计 / 设置
- 会话页可以像聊天软件一样直接以管理员身份回复用户
- 全中文界面

---

## 从零开始

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

### 2. 部署

用上面那条一键部署命令。装完打开 `http://127.0.0.1:8787`（或你配好的域名）登录。

### 3. 添加机器人

面板 →「机器人」→「创建机器人」，两步：

1. 粘贴 BotFather 给的 token → 点「验证 Token」
   - 如果提示处于 Privacy Mode，回到第 1 步的 4 处理
2. 填入管理群 ID → 点「体检这个群」
   - 体检会逐项列出缺什么权限，以及怎么补

用另一个 Telegram 账号私聊这个机器人，管理群里应该立刻出现一个以该用户命名的话题。

---

## 反向代理

容器默认发布到 `127.0.0.1:<端口>`，由反向代理终结 TLS ——
面板持有全部机器人 token 的加密密钥，不适合直接暴露到公网。

### 1Panel

```
网站 → 创建网站 → 反向代理
  主域名      panel.example.com
  代理地址    http://127.0.0.1:8787
  发送域名    $host
```

然后在该网站的「SSL」页签申请证书并开启 HTTPS。

**为什么上游填 `127.0.0.1` 就能通**：1Panel 的 OpenResty 容器使用 `network_mode: host`
（见 [appstore 仓库](https://github.com/1Panel-dev/appstore/blob/dev/apps/openresty/1.31.1.1-2-4-noble/docker-compose.yml)
里 openresty 的 compose 定义），与宿主共享网络栈 —— 所以容器里的 `127.0.0.1`
就是宿主机的 loopback，正好命中 Docker 发布出来的那个端口。

部署脚本还会在检测到 `1panel-network` 时让容器加入它，因此按容器名配上游
（`http://tgs-panel:8787`）同样可行。

### Nginx

```nginx
location / {
    proxy_pass http://127.0.0.1:8787;
    proxy_http_version 1.1;
    # WebSocket 必须转发这两个头，否则面板的实时推送会失效
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

### Caddy

```caddyfile
panel.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

> **实时推送走 WebSocket（`/ws`）。** 反代如果没转发 `Upgrade` 头，面板仍能打开、
> 数据仍会加载，但顶栏会一直显示「连接已断开，正在重连」——
> 是个容易被误判成后端故障的现象。

---

## 其他部署方式

### Docker Compose

仓库里的 `docker-compose.yml` 可以直接用（把 `build:` 段注释掉走预构建镜像，或保留它本地构建）：

```bash
cp .env.example .env   # 填好 MASTER_KEY / SESSION_SECRET / ADMIN_PASSWORD
docker compose up -d
```

镜像基于 `scratch` —— 没有 shell、没有包管理器，全部内容只有一个静态链接的二进制、
CA 证书与前端产物，约 20MB。

### 独立二进制

从 [Releases](https://github.com/kexue-aihao/Telegram_session_Adblock/releases) 下载对应平台的压缩包：

```bash
tar -xzf tgs-0.2.0-linux-amd64.tar.gz
cd tgs-0.2.0-linux-amd64
cp .env.example .env   # 按注释生成 MASTER_KEY 与 SESSION_SECRET
./tgs
```

二进制静态链接（`CGO_ENABLED=0`），不依赖系统库，时区数据库也已编入 ——
可以直接扔进任何 Linux 发行版。放 systemd：

```ini
[Unit]
Description=Telegram Session Adblock
After=network-online.target

[Service]
Type=simple
User=tgs
WorkingDirectory=/opt/tgs
ExecStart=/opt/tgs/tgs
Restart=always
RestartSec=5
EnvironmentFile=/opt/tgs/.env

[Install]
WantedBy=multi-user.target
```

---

## 环境变量

| 变量 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `MASTER_KEY` | ✅ | — | 加密 bot token 的 32 字节主密钥（64 位 hex 或 base64） |
| `SESSION_SECRET` | ✅ | — | 签名会话 Cookie，至少 32 字符 |
| `ADMIN_PASSWORD` | 首次 | — | 仅在库里还没有管理员账号时使用 |
| `ADMIN_USERNAME` | | `admin` | 管理员用户名 |
| `DATABASE_URL` | | `file:./data/app.db` | SQLite 文件路径 |
| `HOST` / `PORT` | | `0.0.0.0` / `8787` | 容器内监听地址 |
| `TZ` | | `Asia/Shanghai` | 影响统计切日与时间展示 |
| `LOG_LEVEL` | | `info` | `debug` `info` `warn` `error` |
| `WEB_DIST` | | 自动探测 | 前端产物目录，镜像里已设好 |
| `PUBLIC_URL` | | — | **尚未实现**：设置后只会在启动日志里收到一条告警 |

> `PUBLIC_URL` / `WEBHOOK_PATH` / `WEBHOOK_SECRET` 目前只是配置层的占位，
> 运行时**只有长轮询**，webhook 模式尚未实现。设了 `PUBLIC_URL` 会看到一条
> 明确的告警日志，而不是静默忽略。

---

## 日常操作

### 在话题里

置顶消息上有等价按钮，不必记命令：

```
/close    关闭当前话题
/reopen   重新打开
/ban      拉黑该用户并关闭话题
/unban    解除拉黑
/reset    清零违规分并解除全部处罚
/info     查看该用户档案
/rules    规则引擎概况
```

### 排查「消息没转过去」

按顺序检查：

1. 面板 →「机器人」→「群体检」—— 它会直接告诉你缺哪一项权限
2. 面板 →「审计」→「广告命中」—— 消息可能被规则拦了
3. 用户是否被静默 / 禁言 / 拉黑（会话详情页顶部会显示生效中的处罚）
4. 是否触发了刷屏阈值（默认 3 秒内超过 8 条）

### 改规则时拿不准

到「规则」页打开编辑器，里面有**实时测试沙盒**：贴上真实的广告文案，
它会高亮命中位置，并显示**归一化后的文本** —— 这样「我写的是 `加微信`，
为什么 `加 微 信` 也被拦了」这类问题一眼就能看明白。

### 忘记管理员密码

删掉库里的管理员记录后重启容器（`ADMIN_PASSWORD` 会重新播种）：

```bash
sqlite3 /opt/tgs/data/app.db "DELETE FROM admin_users;"
docker restart tgs-panel   # 或 systemctl restart tgs
```

---

## 项目结构

```
packages/
├─ server-go/   Go 后端：长轮询、中继管线、规则引擎、HTTP 与 WebSocket
└─ web-vue/     Vue 3 + Vite 面板
```

仓库里还有 `packages/server`（初版 Node 后端）与 `packages/web`（初版 React 前端），
是被取代的实现，**保留作参考，不参与构建与发版**。

### 技术上的几个选择

**后端用 Go，因为正则引擎。** Go 的 `regexp` 是 RE2 —— 线性时间、无回溯，
ReDoS 在结构上不可能发生。这一条直接消掉了原 Node 实现里
「`safe-regex` 静态校验 + `node:vm` 硬超时 + 超时自动熔断」整套防护，
那套东西解决的是一个 RE2 里不存在的问题。

**纯 Go 的 SQLite 驱动（`modernc.org/sqlite`），不引 CGO。**
因此 `CGO_ENABLED=0` 全链路成立，交叉编译不需要任何 C 工具链 ——
arm64 的二进制可以在 amd64 的构建机上直接编出来，镜像能收敛到 `scratch`。

**时区库编进二进制**（`import _ "time/tzdata"`）。
`scratch` 镜像里没有 `/usr/share/zoneinfo`，而时区加载失败是**静默回落到 UTC** ——
表现为统计在北京时间早上 8 点前显示昨天的数字，不报任何错。代价约 450KB。

**存储读写分离成两个连接池。** SQLite 在 WAL 下支持多读一写，
但 `database/sql` 的连接池不知道这件事，会把写事务随机分配导致 `SQLITE_BUSY`。
写池限制成单连接，读池开到 CPU 核数。

**前端动效不引库。** Vue 内置的 `<Transition>` / `<TransitionGroup>` 覆盖了入场、
离场与列表 FLIP 位移，直接操作 DOM class，没有虚拟 DOM 层的开销。
共享元素交给浏览器原生的 View Transitions API，由合成器执行。

**磨砂质感的前提是背后有东西可模糊。** `backdrop-filter` 盖在纯色上只会得到
「稍微亮一点的板子」。所以有一层缓慢漂移的环境光晕，且位置压在侧边栏与顶栏
**覆盖的区域**上 —— 放在空角上等于没放。光晕的深浅两套取值逻辑相反：
暗色要高明度高彩度，浅色要高明度低彩度，且浅色下背景必须先有底色。

---

## 开发

```bash
pnpm install

# 终端 1：后端（Go，热重载需要 air 或手动重启）
cd packages/server-go && go run ./cmd/tgs

# 终端 2：前端（Vite，已把 /api 与 /ws 代理到后端）
pnpm dev
```

前端跑在 <http://localhost:5273>，后端在 8787。

```bash
# 后端检查
cd packages/server-go
gofmt -l .        # 必须无输出
go vet ./...
go test ./... -race

# 前端构建
pnpm build
```

部署脚本有独立的离线验证（用桩件替换 docker，不需要真 Docker）：

```bash
bash scripts/test-deploy.sh scripts/deploy.sh
```

---

## 已知限制

- **删除镜像做不到。** Bot API 不推送「消息被删除」这类更新，机器人无法知道用户或管理员
  在客户端里删了哪条消息。删除只在本方主动删除时镜像。

- **用户在管理群里不存在，Telegram 原生禁言用不了。** 这是私聊中继模式的固有约束，
  阶梯处罚因此在机器人层级实现。若用户恰好也是群成员，可以额外叠加原生处罚。

- **Telegram 只能删除 48 小时内的消息。** 超时后规则命中的「删除原消息」会失败，
  审计里记为 `delete_failed`。

- **单实例部署。** 事件总线是进程内内存实现，SQLite 也是本地文件。
  要多实例需要换掉这两处。

- **webhook 模式尚未实现**，只有长轮询。自托管面板通常没有公网域名，
  长轮询反而是更合适的默认值。

- **`.vue` 文件内部没有类型检查。** `vue-tsc` 3.x 依赖 TypeScript 的 `./lib/tsc` 导出，
  而本仓库锁定的 TS 7 已移除它，目前没有兼容版本。Vite 构建仍能捕获模板语法、
  导入路径与表达式错误。

### 尚未验证的部分

诚实地说清楚：**中继链路（建话题、双向转发、阶梯处罚实际生效）只有代码正确性，
没有运行时证据** —— 那需要真实的 BotFather token 与管理群。

已验证的是：单元测试（43 项，含 ReDoS 免疫与 UTF-16 偏移）、API 端点、
规则沙盒、部署脚本的 38 项断言、镜像构建与发版流水线。
