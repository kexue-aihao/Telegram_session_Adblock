#!/usr/bin/env bash
#
# Telegram 会话中继面板 —— 一键部署 / 升级
#
# 首次运行会安装，再次运行会升级；数据与密钥在整个过程中保持不动。
#
#   curl -fsSL https://raw.githubusercontent.com/kexue-aihao/Telegram_session_Adblock/master/scripts/deploy.sh | sudo bash
#
# 或者先下载再执行（想改参数时更方便）：
#
#   curl -fsSLO .../deploy.sh && sudo bash deploy.sh --port 8787
#
# ──────────────────────────────────────────────────────────────
# 与 1Panel 的兼容性
# ──────────────────────────────────────────────────────────────
#
# 脚本按 1Panel 的实际约定设计，这些约定核对过它的开源仓库：
#
#   · 1Panel 的应用装在 /opt/1panel/apps/<应用>/<版本>/，
#     每个应用一个 docker-compose.yml。本脚本在检测到 1Panel 时
#     把数据目录放到 /opt/1panel/apps/telegram-session-adblock/，
#     这样它能被 1Panel 的文件管理器与备份功能覆盖到。
#
#   · 1Panel 的 OpenResty 容器用 `network_mode: host`
#     （见 appstore 仓库 apps/openresty/*/docker-compose.yml）。
#     这一点决定了「反向代理到 127.0.0.1:端口」为什么可行：
#     OpenResty 与宿主共享网络栈，容器里的 127.0.0.1 就是宿主的 loopback。
#     因此本脚本默认把端口发布到 127.0.0.1 上，正好对上它的默认用法。
#
#   · 1Panel 会创建一个名为 1panel-network 的外部网络。
#     本脚本在检测到它时让容器也加入 —— 这样除了 127.0.0.1:端口，
#     还能用容器名访问。反代面板如果按容器名配上游也能通。
#
# ──────────────────────────────────────────────────────────────
# 一条必须知道的红线
# ──────────────────────────────────────────────────────────────
#
# MASTER_KEY 用来加密库里存的 bot token。**它一旦变化，
# 已保存的 token 全部解不开**，表现为面板上每个机器人都报
# 「无法解密 bot token」，必须逐个重新录入。
#
# 所以本脚本在任何情况下都不会覆盖已有的 MASTER_KEY，
# 升级前也会先备份 .env。如果你手动改过它，请自己确保改回去。

set -euo pipefail

# ══════════════════════════ 常量 ══════════════════════════

IMAGE_REPO="ghcr.io/kexue-aihao/telegram_session_adblock"
CONTAINER_NAME="tgs-panel"
DEFAULT_PORT=8787
DEFAULT_TAG="latest"
PANEL_NETWORK="1panel-network"

# ══════════════════════════ 输出 ══════════════════════════
#
# 只在输出到终端时上色。`curl | bash` 与重定向到日志时不上色，
# 否则日志文件里会混进一堆转义序列。

if [ -t 1 ]; then
  B=$'\033[1m'; DIM=$'\033[2m'; R=$'\033[0m'
  RED=$'\033[31m'; GRN=$'\033[32m'; YEL=$'\033[33m'; BLU=$'\033[34m'
else
  B=''; DIM=''; R=''; RED=''; GRN=''; YEL=''; BLU=''
fi

step() { printf '\n%s▸ %s%s\n' "$B$BLU" "$*" "$R"; }
info() { printf '  %s\n' "$*"; }
ok()   { printf '  %s✓%s %s\n' "$GRN" "$R" "$*"; }
warn() { printf '  %s!%s %s\n' "$YEL" "$R" "$*" >&2; }
die()  { printf '\n%s✗ %s%s\n' "$RED" "$*" "$R" >&2; exit 1; }

# ══════════════════════════ 参数 ══════════════════════════

PORT="$DEFAULT_PORT"
BIND_ADDR="127.0.0.1"
TAG="$DEFAULT_TAG"
DATA_DIR=""
ADMIN_PASSWORD=""
ADMIN_USERNAME="admin"
ASSUME_YES=0
ACTION="deploy"

usage() {
  cat <<EOF
${B}Telegram 会话中继面板 —— 一键部署 / 升级${R}

用法：
  sudo bash deploy.sh [选项]

选项：
  --port PORT          监听端口（默认 $DEFAULT_PORT）
  --bind ADDR          绑定的宿主地址（默认 127.0.0.1，只对本机可见）
  --tag TAG            镜像标签（默认 $DEFAULT_TAG，可指定如 0.1.0）
  --dir DIR            数据目录（默认自动选择，见下）
  --admin-password P   首次安装时的管理员密码（不传则交互式询问）
  --admin-username U   管理员用户名（默认 admin）
  --yes                不询问，全部用默认值（用于自动化）
  --uninstall          卸载容器（${B}保留数据${R}）
  --purge              卸载容器${B}并删除数据${R}（不可恢复）
  -h, --help           显示本帮助

数据目录的默认值：
  检测到 1Panel   → /opt/1panel/apps/telegram-session-adblock
  否则            → /opt/tgs

升级时会保留全部数据与密钥。重复执行本脚本是安全的。
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port)           PORT="${2:-}"; shift 2 ;;
    --bind)           BIND_ADDR="${2:-}"; shift 2 ;;
    --tag)            TAG="${2:-}"; shift 2 ;;
    --dir)            DATA_DIR="${2:-}"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="${2:-}"; shift 2 ;;
    --admin-username) ADMIN_USERNAME="${2:-}"; shift 2 ;;
    --yes|-y)         ASSUME_YES=1; shift ;;
    --uninstall)      ACTION="uninstall"; shift ;;
    --purge)          ACTION="purge"; shift ;;
    -h|--help)        usage; exit 0 ;;
    *)                die "未知参数：$1（用 --help 查看用法）" ;;
  esac
done

# ══════════════════════════ 交互读取 ══════════════════════════
#
# 这个脚本的主要用法之一是 `curl ... | sudo bash`，
# 那时 stdin 是那根管道而不是终端 —— 直接 `read` 会立刻读到 EOF，
# 表现为「密码框一闪而过、密码变成空」。所以必须显式从 /dev/tty 读。

read_secret() {
  local prompt="$1" var=""
  if [ -e /dev/tty ] && [ -r /dev/tty ]; then
    printf '  %s' "$prompt" >&2
    IFS= read -rs var < /dev/tty || true
    printf '\n' >&2
  fi
  printf '%s' "$var"
}

read_line() {
  local prompt="$1" var=""
  if [ -e /dev/tty ] && [ -r /dev/tty ]; then
    printf '  %s' "$prompt" >&2
    IFS= read -r var < /dev/tty || true
  fi
  printf '%s' "$var"
}

confirm() {
  local prompt="$1" answer=""
  [ "$ASSUME_YES" = "1" ] && return 0
  answer="$(read_line "$prompt [y/N] ")"
  case "$answer" in [yY]*) return 0 ;; *) return 1 ;; esac
}

# ══════════════════════════ 前置检查 ══════════════════════════

detect_compose() {
  # 优先用 compose v2 插件形式。v1 的 docker-compose 已停止维护，
  # 但仍有一些老机器在用，两条都试一遍。
  if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
  else
    die "找不到 docker compose。请先安装 Docker 与 Compose 插件。"
  fi
}

preflight() {
  step "环境检查"

  [ "$(id -u)" -eq 0 ] || die "需要 root 权限。请用 sudo 运行。"

  command -v docker >/dev/null 2>&1 || die "未安装 Docker。1Panel 用户可在「容器 → 安装」中安装。"
  docker info >/dev/null 2>&1 || die "Docker 守护进程没有响应。试试 systemctl start docker。"

  detect_compose
  ok "Docker 可用（$COMPOSE）"

  case "$PORT" in
    ''|*[!0-9]*) die "--port 必须是数字，收到：$PORT" ;;
  esac
  if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
    die "--port 超出范围：$PORT"
  fi

  # 数据目录的默认值：优先与 1Panel 的应用目录并列，
  # 这样它的文件管理器与备份功能能覆盖到。
  if [ -z "$DATA_DIR" ]; then
    if [ -d /opt/1panel ]; then
      DATA_DIR="/opt/1panel/apps/telegram-session-adblock"
      IS_1PANEL=1
      ok "检测到 1Panel，数据目录将放在它下面"
    else
      DATA_DIR="/opt/tgs"
      IS_1PANEL=0
    fi
  else
    IS_1PANEL=$([ -d /opt/1panel ] && echo 1 || echo 0)
  fi

  ok "数据目录：$DATA_DIR"
  ok "监听地址：$BIND_ADDR:$PORT"

  STATE_FILE="$DATA_DIR/.deploy-state"
}

# ══════════════════════════ 安装状态检测 ══════════════════════════
#
# 「已存在」的判据是三个条件的**并集**：
#   容器存在 / 数据目录存在 / .env 存在
#
# 用并集而不是只看容器：容器可能被人手动删了而数据还在，
# 那种情况下必须走升级路径 —— 否则会重新生成 MASTER_KEY，
# 把库里已有的 bot token 全部作废。

detect_state() {
  HAS_CONTAINER=0
  HAS_DATA=0
  HAS_ENV=0

  docker inspect "$CONTAINER_NAME" >/dev/null 2>&1 && HAS_CONTAINER=1
  [ -f "$DATA_DIR/data/app.db" ] && HAS_DATA=1
  [ -f "$DATA_DIR/.env" ] && HAS_ENV=1

  if [ "$HAS_CONTAINER" = "1" ] || [ "$HAS_DATA" = "1" ] || [ "$HAS_ENV" = "1" ]; then
    MODE="upgrade"
  else
    MODE="install"
  fi
}

# ══════════════════════════ 密钥与环境变量 ══════════════════════════

gen_hex() { openssl rand -hex "$1" 2>/dev/null || head -c "$(( $1 * 2 ))" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

write_env() {
  local env_file="$DATA_DIR/.env"

  if [ "$MODE" = "install" ]; then
    step "生成配置"

    local master_key session_secret
    master_key="$(gen_hex 32)"
    session_secret="$(gen_hex 48)"

    # 首次安装的密码：优先用参数，否则交互式问
    if [ -z "$ADMIN_PASSWORD" ]; then
      if [ "$ASSUME_YES" = "1" ]; then
        die "非交互模式必须用 --admin-password 指定管理员密码。"
      fi
      info "请设置面板的管理员密码（至少 8 位，需同时包含字母和数字）"
      local p1 p2
      p1="$(read_secret '密码：')"
      p2="$(read_secret '再输一次：')"
      [ -n "$p1" ] || die "密码不能为空。"
      [ "$p1" = "$p2" ] || die "两次输入不一致。"
      ADMIN_PASSWORD="$p1"
    fi

    # 密码策略与后端 env.ts 里的校验保持一致。
    # 在这里先挡住，比让容器启动时才失败体验好得多。
    if [ "${#ADMIN_PASSWORD}" -lt 8 ]; then
      die "管理员密码至少 8 位。"
    fi
    case "$ADMIN_PASSWORD" in
      *[a-zA-Z]*) : ;;
      *) die "管理员密码需要包含至少一个字母。" ;;
    esac
    case "$ADMIN_PASSWORD" in
      *[0-9]*) : ;;
      *) die "管理员密码需要包含至少一个数字。" ;;
    esac

    umask 077
    cat > "$env_file" <<EOF
# 由 deploy.sh 生成于 $(date -Iseconds)
#
# 这个文件里有 MASTER_KEY —— 它用来加密库里存的 bot token。
# 一旦变化，已保存的 token 全部解不开，必须到面板里逐个重新录入。
# 请把它纳入备份。

APP_ENV=production
HOST=0.0.0.0
PORT=8787
TZ=${TZ:-Asia/Shanghai}
LOG_LEVEL=info
DATABASE_URL=file:/data/app.db

MASTER_KEY=${master_key}
SESSION_SECRET=${session_secret}
ADMIN_PASSWORD=${ADMIN_PASSWORD}
ADMIN_USERNAME=${ADMIN_USERNAME}

# 让容器里的时区与宿主一致（TZ 上面那行是应用时区，这行是系统时区）
TZ_SYSTEM=$(cat /etc/timezone 2>/dev/null || echo "Asia/Shanghai")
EOF
    chmod 600 "$env_file"
    ok "已生成 $env_file"
    ok "MASTER_KEY 与 SESSION_SECRET 已随机生成"

    else
    step "沿用已有配置"

    # 升级路径**绝不重写** .env。特别是 MASTER_KEY：
    # 重新生成会让库里所有 bot token 失效。
    [ -f "$env_file" ] || die "$env_file 不存在，但检测到已有数据。请从备份恢复，或用 --purge 清空后重装。"
    ok "保留 $env_file（含 MASTER_KEY，未改动）"

    # 从脚本自己的状态文件里读回宿主端口与标签。
    #
    # 刻意**不从 .env 读端口**：那里面那个 PORT 是**容器内**的监听端口，
    # 恒为 8787（compose 里映射成 ${宿主端口}:8787）。拿它当宿主端口用，
    # 会把当初用 --port 9000 部署的实例改回 8787，
    # 而反向代理指向的 9000 会立刻 502 —— 且升级过程看起来完全正常。
    if [ -f "$STATE_FILE" ]; then
      # shellcheck disable=SC1090
      . "$STATE_FILE"
      if [ -n "${HOST_PORT:-}" ] && [ "$PORT" = "$DEFAULT_PORT" ]; then
        PORT="$HOST_PORT"
        info "沿用已有宿主端口：$PORT"
      fi
      if [ -n "${HOST_BIND:-}" ] && [ "$BIND_ADDR" = "127.0.0.1" ]; then
        BIND_ADDR="$HOST_BIND"
      fi
      if [ -n "${HOST_USERNAME:-}" ] && [ "$ADMIN_USERNAME" = "admin" ]; then
        ADMIN_USERNAME="$HOST_USERNAME"
      fi
    else
      warn "找不到 $STATE_FILE，无法确认上次用的宿主端口。"
      warn "当前将使用 --port 指定的 ${PORT}；如果反代原来指向别的端口，请显式传 --port。"
    fi

    # 升级前备份 .env。它是唯一不可再生的东西（数据在卷里，密钥在这里）。
    local backup="$env_file.bak.$(date +%Y%m%d-%H%M%S)"
    cp -p "$env_file" "$backup"
    chmod 600 "$backup"
    ok "已备份到 $(basename "$backup")"
  fi

  # .env 里的 PORT 永远是容器内的监听端口，固定 8787。
  # 宿主端口由 compose 的端口映射决定，记在 STATE_FILE 里。
  if grep -qE '^PORT=' "$env_file"; then
    sed -i.tmp "s|^PORT=.*|PORT=8787|" "$env_file" && rm -f "$env_file.tmp"
  fi

  write_state
}

# 记录脚本自己的状态。
#
# 与 .env 分开存：.env 是**应用**的配置（会被容器读取），
# 这里是**部署脚本**的记忆（宿主端口、绑定的地址）。
# 混在一起的话，应用侧多出一堆它不认识的变量，排查配置问题时很干扰。
write_state() {
  umask 077
  cat > "$STATE_FILE" <<EOF
# 由 deploy.sh 维护 —— 记录本次部署的宿主侧参数。
# .env 是给容器读的应用配置；这里是给脚本读的部署状态。两者刻意分开。
HOST_PORT=${PORT}
HOST_BIND=${BIND_ADDR}
HOST_TAG=${TAG}
HOST_USERNAME=${ADMIN_USERNAME}
DEPLOYED_AT=$(date -Iseconds)
EOF
  chmod 600 "$STATE_FILE"
}

# ══════════════════════════ 生成 compose ══════════════════════════

write_compose() {
  local compose_file="$DATA_DIR/docker-compose.yml"

  local networks_block="" network_line=""
  if docker network inspect "$PANEL_NETWORK" >/dev/null 2>&1; then
    # 1Panel 创建了这个网络，让容器也加入：
    # 除了 127.0.0.1:端口，还能用容器名访问，兼容按容器名配上游的反代。
    network_line="      - ${PANEL_NETWORK}"
    networks_block=$'\n'"networks:"$'\n'"  ${PANEL_NETWORK}:"$'\n'"    external: true"
  fi

  cat > "$compose_file" <<EOF
# 由 deploy.sh 生成 —— 重新运行脚本会覆盖本文件。
# 要持久化地改配置，请改同目录下的 .env，或给脚本传参数。

services:
  panel:
    image: ${IMAGE_REPO}:${TAG}
    container_name: ${CONTAINER_NAME}
    restart: unless-stopped

    env_file:
      - .env

    ports:
      # 只绑到 ${BIND_ADDR}。默认的 127.0.0.1 意味着只有本机（以及
      # 1Panel 那个用 host 网络的 OpenResty）能访问 ——
      # 对外由反向代理终结 TLS，面板本身不直接暴露。
      - "${BIND_ADDR}:${PORT}:8787"

    volumes:
      - ./data:/data

    # 镜像基于 scratch：没有 shell，也没有 curl/wget，
    # 所以探针只能是二进制自己的 -healthcheck 子命令。
    healthcheck:
      test: ['CMD', '/tgs', '-healthcheck']
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 15s

    # 容器内不需要写入任何地方，除了 /data 卷与 tmpfs
    read_only: true
    tmpfs:
      - /tmp

    security_opt:
      - no-new-privileges:true
$([ -n "$network_line" ] && printf '\n    networks:\n%s\n' "$network_line")${networks_block}

volumes:
  # 数据直接落在 ${DATA_DIR}/data，
  # 不使用命名卷 —— 1Panel 的备份与文件管理器只认目录。
EOF

  ok "已写入 $compose_file"
}

# ══════════════════════════ 容器生命周期 ══════════════════════════

pull_image() {
  step "拉取镜像"
  info "${IMAGE_REPO}:${TAG}"

  if ! $COMPOSE -f "$DATA_DIR/docker-compose.yml" pull; then
    die "镜像拉取失败。检查网络，或确认标签 '$TAG' 存在：
    https://github.com/kexue-aihao/Telegram_session_Adblock/pkgs/container/telegram_session_adblock"
  fi
  ok "镜像就绪"
}

start_container() {
  step "启动容器"

  # down 只移除容器，不动 ./data 目录 ——
  # 那里有 SQLite 库（含 bot token 与全部中继消息）。
  $COMPOSE -f "$DATA_DIR/docker-compose.yml" down --remove-orphans >/dev/null 2>&1 || true

  if ! $COMPOSE -f "$DATA_DIR/docker-compose.yml" up -d; then
    die "容器启动失败。用下面的命令看详细原因：
    cd $DATA_DIR && $COMPOSE logs --tail 50"
  fi
  ok "容器已启动"
}

wait_healthy() {
  step "等待服务就绪"

  local i health
  for i in $(seq 1 30); do
    health="$(docker inspect --format '{{.State.Health.Status}}' "$CONTAINER_NAME" 2>/dev/null || echo "unknown")"

    case "$health" in
      healthy)
        ok "服务已就绪"
        return 0
        ;;
      unhealthy)
        printf '\n'
        warn "容器报告不健康。最近日志："
        $COMPOSE -f "$DATA_DIR/docker-compose.yml" logs --tail 30 2>&1 | sed 's/^/    /' >&2
        die "启动失败。最常见的原因是 ADMIN_PASSWORD 不合规，或 $DATA_DIR 权限不对。"
        ;;
    esac
    printf '  %s…（%d/30，当前 %s）\r' "$DIM" "$i" "$health"
    sleep 2
  done

  printf '\n'
  warn "等待超时。容器可能仍在启动中，用下面的命令查看："
  warn "  cd $DATA_DIR && $COMPOSE logs -f"
}

# ══════════════════════════ 卸载 ══════════════════════════

do_uninstall() {
  step "卸载容器"
  [ -d "$DATA_DIR" ] || die "找不到 $DATA_DIR。"

  $COMPOSE -f "$DATA_DIR/docker-compose.yml" down --remove-orphans 2>/dev/null || \
    docker rm -f "$CONTAINER_NAME" 2>/dev/null || true

  ok "容器已移除"
  info "数据保留在 $DATA_DIR（含 MASTER_KEY 与全部会话记录）"
  info "要彻底删除，运行：sudo bash $0 --purge"
}

do_purge() {
  step "彻底卸载"

  # 这一步会删掉 MASTER_KEY 与全部数据，**不可恢复**。
  # 所以显式要求确认，且不接受 --yes 跳过 —— 自动化脚本不该能
  # 一键抹掉别人的生产数据。
  if [ "$ASSUME_YES" = "1" ]; then
    die "--purge 不接受 --yes。这是不可恢复的操作，必须人工确认。"
  fi

  if ! confirm "这会删除 $DATA_DIR 下的全部数据（含 MASTER_KEY 与会话记录），且不可恢复。确定？"; then
    info "已取消。"
    exit 0
  fi

  $COMPOSE -f "$DATA_DIR/docker-compose.yml" down --remove-orphans 2>/dev/null || \
    docker rm -f "$CONTAINER_NAME" 2>/dev/null || true

  rm -rf "$DATA_DIR"
  ok "已删除 $DATA_DIR"
}

# ══════════════════════════ 完成提示 ══════════════════════════

print_summary() {
  local host_ip
  host_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -n "$host_ip" ] || host_ip="<服务器IP>"

  cat <<EOF

${B}${GRN}════════════════════════════════════════════════${R}
${B}${GRN}  $([ "$MODE" = "install" ] && echo "部署完成" || echo "升级完成")${R}
${B}${GRN}════════════════════════════════════════════════${R}

  ${B}访问地址${R}   http://${BIND_ADDR}:${PORT}
  ${B}数据目录${R}   ${DATA_DIR}
  ${B}容器名${R}     ${CONTAINER_NAME}

EOF

  if [ "$MODE" = "install" ]; then
    cat <<EOF
  ${B}管理员账号${R} ${ADMIN_USERNAME} / ${DIM}（你在安装时设置的密码）${R}

EOF
  else
    cat <<EOF
  ${B}管理员账号${R} ${ADMIN_USERNAME} ${DIM}（密码未改动）${R}

EOF
  fi

  # 反向代理的说明按 1Panel 的实际用法写。
  # 上游填 127.0.0.1:端口 之所以可行，是因为它的 OpenResty 用 host 网络。
  cat <<EOF
${B}── 配置反向代理 ──────────────────────────────${R}

  ${B}1Panel 用户：${R}

    网站 → 创建网站 → 反向代理
      主域名      你的域名
      代理地址    http://127.0.0.1:${PORT}
      发送域名    \$host

    然后在该网站的「SSL」页签申请证书并开启 HTTPS。
    ${DIM}1Panel 的 OpenResty 使用 host 网络模式，容器内的 127.0.0.1
    就是宿主机的 loopback，所以这个上游地址可以直接用。${R}
$([ "$IS_1PANEL" = "1" ] && echo "
    ${DIM}容器已同时加入 ${PANEL_NETWORK}，若你的反代按容器名配上游，
    也可以填 http://${CONTAINER_NAME}:8787${R}")

  ${B}其他环境（Nginx / Caddy）：${R}

    Nginx:  proxy_pass http://127.0.0.1:${PORT};
    Caddy:  reverse_proxy 127.0.0.1:${PORT}

${B}── 常用操作 ──────────────────────────────────${R}

  看日志    cd ${DATA_DIR} && $COMPOSE logs -f
  重启      cd ${DATA_DIR} && $COMPOSE restart
  升级      sudo bash $0
  卸载      sudo bash $0 --uninstall

${B}── 接下来 ────────────────────────────────────${R}

  1. 登录面板，到「机器人 → 创建机器人」粘贴 @BotFather 的 token
  2. 按向导提示绑定管理群（它会体检并列出缺哪项权限）
  3. 用另一个账号私聊机器人，管理群里会自动出现话题

  ${DIM}详细步骤见 README 的「快速开始」。${R}

EOF
}

# ══════════════════════════ 主流程 ══════════════════════════

main() {
  printf '\n%sTelegram 会话中继面板 —— 部署脚本%s\n' "$B" "$R"

  preflight

  if [ "$ACTION" = "uninstall" ]; then do_uninstall; exit 0; fi
  if [ "$ACTION" = "purge" ]; then do_purge; exit 0; fi

  detect_state
  if [ "$MODE" = "upgrade" ]; then
    step "检测到已安装"
    [ "$HAS_CONTAINER" = "1" ] && info "容器 $CONTAINER_NAME 已存在"
    [ "$HAS_DATA" = "1" ]      && info "数据库已存在（会话记录会被保留）"
    [ "$HAS_ENV" = "1" ]       && info "配置文件已存在（MASTER_KEY 会被保留）"
    info "将执行${B}升级${R}：拉取新镜像并重启容器"
  else
    step "未检测到已有安装，执行首次部署"
  fi

  mkdir -p "$DATA_DIR/data"
  chmod 700 "$DATA_DIR"

  write_env
  write_compose
  pull_image
  start_container
  wait_healthy
  print_summary
}

main "$@"
