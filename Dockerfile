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
#
# `--platform=$BUILDPLATFORM` 是关键：它让这个阶段**只在原生架构上跑一次**，
# 而不是为每个目标架构各跑一遍。
#
# 前端产物是与架构无关的静态文件，为 arm64 镜像在 QEMU 里重新跑一遍
# npm install + vite build 要花好几分钟，且产物字节级相同 —— 纯浪费。
FROM --platform=$BUILDPLATFORM node:24-slim AS web-builder
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

# tsconfig.base.json 必须一起拷进来。
#
# packages/web-vue/tsconfig.json 用 `extends: "../../tsconfig.base.json"`
# 引用了它 —— 而 Vite 底层的 rolldown 会解析 tsconfig，文件缺失时直接
# 以「Tsconfig not found」退出（code 1），报错信息里不会提到 Dockerfile，
# 第一次遇到很容易往别处找原因。
COPY tsconfig.base.json ./
COPY packages/web-vue packages/web-vue
RUN pnpm --filter @tgs/web-vue build


# ────────────────────────── 阶段 2：Go 二进制 ──────────────────────────
#
# 同样固定在构建平台上，再靠 Go 自己的交叉编译产出目标架构的二进制。
#
# 这是 CGO_ENABLED=0 换来的直接好处：纯 Go 代码的交叉编译不需要任何
# C 工具链，所以 arm64 的二进制可以在 amd64 的构建机上直接编出来，
# 完全绕开 QEMU 模拟 —— 多架构构建从「几十分钟」降到「一两分钟」。
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go-builder

# alpine 默认不带 CA 证书，而容器里要访问 https://api.telegram.org。
# 装完从这一层拷进最终镜像。
#
# `file` 是为了下面那条产物架构断言 —— busybox 不提供它。
# 这是构建阶段的依赖，不会进入最终镜像，所以体积代价为零。
RUN apk add --no-cache ca-certificates file

WORKDIR /src

# 同样先只拷 go.mod / go.sum 以利用缓存
COPY packages/server-go/go.mod packages/server-go/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY packages/server-go/ ./

# TARGETOS / TARGETARCH 由 BuildKit 按 --platform 自动注入，
# 在这个阶段里它们代表**目标**架构（而 BUILDPLATFORM 代表构建机架构）。
#
# 必须在 FROM 之后重新声明才可见，而且不能写成 ENV ——
# 写死 GOOS=linux 而不管 GOARCH 是一个很隐蔽的坑：单看代码「编译通过、
# 镜像也建出来了」，但 arm64 镜像里装的其实是 amd64 的二进制，
# 只有在真机上跑才会以 exec format error 暴露。
ARG TARGETOS
ARG TARGETARCH

ARG VERSION=dev
ENV CGO_ENABLED=0

# -s -w 去掉符号表与调试信息，13MB → 约 9MB。
# 代价是 panic 的堆栈没有行号 —— 真要排查线上崩溃时，
# 用同一个 VERSION 重新构建一次带符号的二进制即可对上。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags "-s -w -X github.com/tgs/server/internal/api.Version=${VERSION}" \
      -o /out/tgs ./cmd/tgs

# 顺带断言一下产物架构，让「交叉编译没生效」这类问题在构建期就炸掉，
# 而不是等到用户在 arm64 机器上 docker run 才发现。
RUN set -eux; \
    expected="${TARGETARCH}"; \
    case "$expected" in \
      amd64) want="x86-64" ;; \
      arm64) want="aarch64" ;; \
      *) want="" ;; \
    esac; \
    if [ -n "$want" ]; then \
      got=$(file -b /out/tgs 2>/dev/null || echo "unknown"); \
      echo "产物架构: $got（期望含 $want）"; \
      echo "$got" | grep -q "$want" || { \
        echo "::error::交叉编译未生效：目标 $TARGETARCH 但产物是 $got"; exit 1; \
      }; \
    fi

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
# 这里的 UID 65532 是 distroless 的 nonroot 约定值，与下面的 USER 一致。
#
# ── 这一行只解决一半问题，别被它误导 ────────────────────────
#
# Docker 用镜像里这个目录的属主去初始化**新建的命名卷**，
# 所以命名卷开箱即用。但**目录挂载（bind mount）完全不走这条路** ——
# 挂载点就是宿主目录本身，属主是宿主上的那个。
#
# 而容器**始终**以 USER 指定的 65532 运行（这一行改变不了这一点），
# 于是宿主目录若是 root 属主，容器能起来、几秒后退出，日志里只有
#
#     连接数据库失败（/data/app.db）: unable to open database file (14)
#
# 完全不提权限。所以部署脚本里有一条对应的 `chown -R 65532:65532`，
# 两处必须一起看。
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
