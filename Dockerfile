# ═══════════════════════════════════════════════════════════════
# 三阶段构建：前端产物 → Go 静态二进制 → scratch 运行时。
#
# 最终镜像里**没有任何操作系统**：没有 shell、没有包管理器、没有 libc。
# 这不是为了炫技，而是三条实打实的好处：
#
#   1. 体积。~20MB，其中绝大部分是前端产物与 CA 证书；
#      node:24-slim 做的运行时是 ~200MB。
#   2. 攻击面。镜像里没有可执行的东西，拿到 shell 也无从下手 ——
#      因为根本没有 shell。
#   3. 启动。静态链接的二进制直接 exec，没有动态加载、没有运行时初始化，
#      冷启动在几十毫秒量级。
#
# 能这么做的前提是两个刻意的技术选择（都在代码里）：
#   - SQLite 用 modernc.org/sqlite（纯 Go），因此 CGO_ENABLED=0 可行；
#   - main.go 里 import 了 time/tzdata，把时区库编进了二进制，
#     否则 scratch 里没有 /usr/share/zoneinfo，TZ 会静默失效。
# ═══════════════════════════════════════════════════════════════

# ────────────────────────── 阶段 1：前端 ──────────────────────────
FROM node:24-slim AS web-builder
ENV PNPM_HOME=/pnpm
ENV PATH=$PNPM_HOME:$PATH
RUN corepack enable
WORKDIR /repo

# 只先拷贝清单文件，让依赖安装这一层能被缓存 ——
# 只要 lockfile 没变，改业务代码不会触发重新安装。
#
# 注意这里必须把**所有** workspace 成员的 package.json 都拷进来：
# pnpm-lock.yaml 覆盖整个 workspace，少任何一个都会让
# --frozen-lockfile 因「清单与 lockfile 不一致」而失败。
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY packages/shared/package.json packages/shared/
COPY packages/server/package.json packages/server/
COPY packages/web/package.json packages/web/
COPY packages/web-vue/package.json packages/web-vue/
RUN --mount=type=cache,id=pnpm,target=/pnpm/store \
    pnpm install --frozen-lockfile

COPY packages/web-vue packages/web-vue
RUN pnpm --filter @tgs/web-vue build


# ────────────────────────── 阶段 2：Go 二进制 ──────────────────────────
FROM golang:1.26-alpine AS go-builder

# alpine 默认不带 CA 证书，而容器里要访问 https://api.telegram.org。
# 装完从这一层拷进最终镜像。
RUN apk add --no-cache ca-certificates

WORKDIR /src

# 同样先只拷 go.mod / go.sum 以利用缓存
COPY packages/server-go/go.mod packages/server-go/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY packages/server-go/ ./

ARG VERSION=dev
ENV CGO_ENABLED=0 GOOS=linux

# -s -w 去掉符号表与调试信息，13MB → 约 9MB。
# 代价是 panic 的堆栈没有行号 —— 真要排查线上崩溃时，
# 用同一个 VERSION 重新构建一次带符号的二进制即可对上。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
      -ldflags "-s -w -X github.com/tgs/server/internal/api.Version=${VERSION}" \
      -o /out/tgs ./cmd/tgs

# 空目录，用来承接镜像里的 /data 所有者设置（见下）
RUN mkdir -p /out/data


# ────────────────────────── 阶段 3：运行时 ──────────────────────────
FROM scratch

# 时区库已在二进制内（time/tzdata），这里只需要 CA 证书。
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-builder /out/tgs /tgs
COPY --from=web-builder /repo/packages/web-vue/dist /web-dist

# 数据目录。
#
# 用 --chown 而不是在运行时 chown —— scratch 里没有 chown 命令。
# 这里的 UID 65532 是 distroless 的 nonroot 约定值，选它是为了
# 「以后想换成 distroless 时不用再动」。
#
# Docker 会用镜像里这个目录的所有者去初始化**新建的命名卷**，
# 所以匿名卷与命名卷都能直接写；只有 bind mount 需要宿主侧
# 自己 chown 65532。这一步不做的话，容器会以 root 运行 ——
# 对自托管面板虽可接受，但没必要。
COPY --from=go-builder --chown=65532:65532 /out/data /data

ENV APP_ENV=production
ENV HOST=0.0.0.0
ENV PORT=8787
ENV DATABASE_URL=file:/data/app.db
ENV WEB_DIST=/web-dist

VOLUME ["/data"]
EXPOSE 8787
USER 65532:65532

# scratch 里没有 curl/wget，探针只能是二进制自己。
# tgs -healthcheck 会请求 /api/health/ready（含一次真实的数据库 ping），
# 因此「进程活着但库连不上」也会被判为不健康。
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/tgs", "-healthcheck"]

ENTRYPOINT ["/tgs"]
