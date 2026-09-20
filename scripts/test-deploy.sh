#!/usr/bin/env bash
#
# deploy.sh 的离线验证。
#
# 本机没有 Docker，所以把 docker / id 换成桩件，只验证脚本自身的逻辑：
#   · 首次安装与升级的分支判断
#   · .env 与 .deploy-state 的生成
#   · 宿主端口在升级后是否保持不变（这是最容易写错的一处）
#   · docker-compose.yml 的内容
#   · 1panel-network 存在与不存在时的两种 compose 形态
#
# 它验证不了的是「容器真的能起来」—— 那需要 Docker 与镜像。

set -uo pipefail

SCRIPT="${1:?用法: test-deploy.sh <deploy.sh 路径>}"
WORK="$(mktemp -d)"
DEPLOY="$WORK/opt/tgs"
STUB="$WORK/stub"
mkdir -p "$STUB" "$DEPLOY"

PASS=0; FAIL=0
check() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    printf '  \033[32m✓\033[0m %s\n' "$desc"; PASS=$((PASS+1))
  else
    printf '  \033[31m✗\033[0m %s\n    期望: %s\n    实际: %s\n' "$desc" "$expected" "$actual"
    FAIL=$((FAIL+1))
  fi
}
contains() {
  local desc="$1" needle="$2" haystack="$3"
  if printf '%s' "$haystack" | grep -qF -- "$needle"; then
    printf '  \033[32m✓\033[0m %s\n' "$desc"; PASS=$((PASS+1))
  else
    printf '  \033[31m✗\033[0m %s\n    未找到: %s\n' "$desc" "$needle"; FAIL=$((FAIL+1))
  fi
}
not_contains() {
  local desc="$1" needle="$2" haystack="$3"
  if printf '%s' "$haystack" | grep -qF -- "$needle"; then
    printf '  \033[31m✗\033[0m %s\n    不该出现: %s\n' "$desc" "$needle"; FAIL=$((FAIL+1))
  else
    printf '  \033[32m✓\033[0m %s\n' "$desc"; PASS=$((PASS+1))
  fi
}

# 检查生成的 compose 里有没有「空的顶层映射」。
#
# 这不是杞人忧天：脚本曾经在末尾留过一段**只含注释**的
#
#     volumes:
#       # 数据用目录挂载，不需要命名卷
#
# YAML 会把它解析成 null，而 Compose 要求 volumes 是 mapping，
# 于是在用户机器上炸出「validating ...: volumes must be a mapping」。
# 那段注释读起来完全无害 —— 问题出在那个孤零零的键上。
#
# 早先的测试没抓到它，因为 docker 是桩件、对所有子命令都返回成功，
# 桩件不会解析 YAML。所以这里直接看文本结构：
# 一个顶层键后面若只有空行与注释，就判定为空映射。
check_compose_structure() {
  local desc="$1" file="$2" empties cramped
  empties="$(awk '
    /^[a-zA-Z_][a-zA-Z0-9_]*:/ {
      if (key != "" && !found) print key
      key = $0
      sub(/:.*$/, "", key)
      rest = $0
      sub(/^[^:]*:/, "", rest)
      gsub(/[ \t]/, "", rest)
      found = (rest != "") ? 1 : 0
      next
    }
    # 缩进的、且首字符不是 # 的行才算「有内容」
    /^[ \t]+[^ \t#]/ { if (key != "") found = 1 }
    END { if (key != "" && !found) print key }
  ' "$file")"

  # 顶层键之间应当有空行。这不是 YAML 的要求（列 0 的键本来就会关闭
  # 上层映射），而是可读性 —— 而且它正好能抓到「靠命令替换剥掉换行
  # 凑出来的格式」这类问题：那种写法在功能上没错，但读起来像是错的。
  cramped="$(awk '
    /^[a-zA-Z_]/ { if (prev != "" && prev !~ /^$/) print $1; prev = $0; next }
    { prev = $0 }
  ' "$file")"

  if [ -n "$empties" ] || [ -n "$cramped" ]; then
    printf '  \033[31m✗\033[0m %s\n' "$desc"
    [ -n "$empties" ] && printf '      空映射（会被解析成 null）：%s\n' "$empties"
    [ -n "$cramped" ] && printf '      顶层键前缺空行：%s\n' "$cramped"
    FAIL=$((FAIL+1))
  else
    printf '  \033[32m✓\033[0m %s\n' "$desc"; PASS=$((PASS+1))
  fi
}

# ── 桩件：伪造 root、Docker CLI 与 compose ──────────────────────
cat > "$STUB/id" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = "-u" ] && { echo 0; exit 0; }
exec /usr/bin/id "$@"
EOF

chmod +x "$STUB/id"
export PATH="$STUB:$PATH"
export STUB_HAS_PANEL_NETWORK=0
export STUB_CONTAINER_EXISTS=0
export DOCKER_LOG="$WORK/docker.log"
export DOCKER_STATE="$WORK/docker-state"
mkdir -p "$DOCKER_STATE"

# The stateful stub models old/new containers without touching a real Docker host.
cat > "$STUB/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$DOCKER_LOG"
case "$1" in
  info) exit 0 ;;
  network) [ "${STUB_HAS_PANEL_NETWORK:-0}" = "1" ]; exit $? ;;
  inspect)
    if [ "${2:-}" = "--format" ] && [[ "$3" == *State.Health.Status* ]]; then
      [ "${STUB_UNHEALTHY:-0}" = "1" ] && echo unhealthy || echo healthy
      exit 0
    fi
    if [ ! -f "$DOCKER_STATE/new" ]; then
      [ "${STUB_CONTAINER_EXISTS:-0}" = "1" ] && [ ! -f "$DOCKER_STATE/renamed" ] || exit 1
    fi
    [ "${2:-}" = "--format" ] || exit 0
    case "$3" in
      *State.Health.Status*)
        [ "${STUB_UNHEALTHY:-0}" = "1" ] && echo unhealthy || echo healthy ;;
      *State.Running*)
        if [ -f "$DOCKER_STATE/stopped" ] && [ ! -f "$DOCKER_STATE/new" ]; then echo false; else echo "${STUB_RUNNING:-true}"; fi ;;
      *project.working_dir*)
        if [ -f "$DOCKER_STATE/new" ]; then cat "$DOCKER_STATE/new"; else echo "${STUB_EXISTING_DIR:-}"; fi ;;
      *project\"*) echo "${STUB_PROJECT:-tgs}" ;;
      *Mounts*)
        if [ -f "$DOCKER_STATE/new" ]; then printf '%s/data\n' "$(cat "$DOCKER_STATE/new")"; else echo "${STUB_DATA_MOUNT:-${STUB_EXISTING_DIR:-}/data}"; fi ;;
      *NetworkSettings.Ports*) echo "${STUB_BIND:-127.0.0.1}|${STUB_PORT:-9000}" ;;
    esac
    exit 0 ;;
  pull) [ "${STUB_PULL_FAIL:-0}" != "1" ]; exit $? ;;
  stop) touch "$DOCKER_STATE/stopped"; exit 0 ;;
  start) rm -f "$DOCKER_STATE/stopped"; touch "$DOCKER_STATE/restarted"; exit 0 ;;
  rename)
    if [ "$2" = "tgs-panel" ]; then touch "$DOCKER_STATE/renamed"; else rm -f "$DOCKER_STATE/renamed"; fi
    exit 0 ;;
  rm) exit 0 ;;
  compose)
    shift
    [ "${1:-}" = "version" ] && exit 0
    project=""; file=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -p) project="$2"; shift 2 ;;
        -f) file="$2"; shift 2 ;;
        *) break ;;
      esac
    done
    case "${1:-}" in
      config) [ "${STUB_CONFIG_FAIL:-0}" != "1" ]; exit $? ;;
      pull) [ "${STUB_PULL_FAIL:-0}" != "1" ]; exit $? ;;
      down)
        if [ -f "$DOCKER_STATE/renamed" ] && { [ -z "$project" ] || [ "$project" = "${STUB_PROJECT:-tgs}" ]; }; then
          touch "$DOCKER_STATE/wrong-project"
          exit 1
        fi
        rm -f "$DOCKER_STATE/new"
        exit 0 ;;
      up)
        [ "${STUB_START_FAIL:-0}" != "1" ] || exit 1
        if [ -f "$DOCKER_STATE/renamed" ] || [ "${STUB_TRACK_NEW:-0}" = "1" ]; then
          dirname "$file" > "$DOCKER_STATE/new"
        fi
        exit 0 ;;
    esac
    exit 0 ;;
esac
exit 0
EOF

cat > "$STUB/cp" <<'EOF'
#!/usr/bin/env bash
[ "${STUB_COPY_FAIL:-0}" != "1" ] || exit 1
exec /usr/bin/cp "$@"
EOF
cat > "$STUB/hostname" <<'EOF'
#!/usr/bin/env bash
echo 127.0.0.1
EOF
chmod +x "$STUB/docker" "$STUB/cp" "$STUB/hostname"
if ! command -v flock >/dev/null 2>&1; then
  # Git Bash has no flock; Linux CI uses the real implementation.
  printf '#!/usr/bin/env bash\nexit 0\n' > "$STUB/flock"
  chmod +x "$STUB/flock"
fi

# chown 也要桩掉，并记录调用参数。
#
# 这一条是有来历的：容器以 UID 65532（非 root）运行，而 ./data 是
# **目录挂载** —— Dockerfile 里那句 `COPY --chown=65532` 只对命名卷生效，
# bind mount 用的是宿主目录的属主。
#
# 漏掉 chown 的现象很有迷惑性：容器起得来、几秒后退出，日志里只有
# 「连接数据库失败（/data/app.db）: unable to open database file」，
# 报错完全不提权限。这是真机上踩过的坑。
export CHOWN_LOG="$WORK/chown.log"
cat > "$STUB/chown" <<'EOF'
#!/usr/bin/env bash
echo "$*" >> "${CHOWN_LOG:?}"
exit 0
EOF
chmod +x "$STUB/chown"

deploy() {
  bash -c 'script=$1; opt_root=$2; shift 2; source "$script"; OPT_ROOT="$opt_root"; main' \
    deploy "$SCRIPT" "$WORK/opt" "$@"
}

run_deploy() {
  # Regular upgrade scenarios keep only filesystem state between invocations.
  rm -f "$DOCKER_STATE/new"
  deploy --dir "$DEPLOY" --admin-password "TestPass1234" --yes "$@" 2>&1
}

# ══════════════════ 场景 1：首次安装 ══════════════════
echo
echo "── 场景 1：无任何已有文件 → 首次安装 ──"
out="$(run_deploy)"
contains "识别为首次部署" "未检测到已有安装" "$out"
[ -f "$DEPLOY/.env" ] && check ".env 已生成" "yes" "yes" || check ".env 已生成" "yes" "no"
[ -f "$DEPLOY/.deploy-state" ] && check ".deploy-state 已生成" "yes" "yes" || check ".deploy-state 已生成" "yes" "no"
[ -f "$DEPLOY/docker-compose.yml" ] && check "compose 已生成" "yes" "yes" || check "compose 已生成" "yes" "no"

env1="$(cat "$DEPLOY/.env")"
contains ".env 含 MASTER_KEY" "MASTER_KEY=" "$env1"
contains ".env 含 SESSION_SECRET" "SESSION_SECRET=" "$env1"
contains ".env 含 ADMIN_PASSWORD" "ADMIN_PASSWORD=TestPass1234" "$env1"
check ".env 内 PORT 固定为容器端口" "PORT=8787" "$(grep '^PORT=' "$DEPLOY/.env")"

# 密钥长度必须是 MASTER_KEY=64 hex、SESSION_SECRET=96 hex
mk_len=$(grep '^MASTER_KEY=' "$DEPLOY/.env" | cut -d= -f2 | tr -d '\r' | wc -c)
check "MASTER_KEY 为 64 位十六进制（64+换行=65）" "65" "$mk_len"

comp1="$(cat "$DEPLOY/docker-compose.yml")"
contains "compose 映射到 127.0.0.1:8787" "127.0.0.1:8787:8787" "$comp1"
contains "compose 挂载 ./data" "./data:/data" "$comp1"
contains "compose 用二进制自检做探针" "['CMD', '/tgs', '-healthcheck']" "$comp1"
not_contains "无 1panel-network 时不写 networks 段" "1panel-network" "$comp1"
check_compose_structure "compose 结构无空映射（无 1panel-network）" "$DEPLOY/docker-compose.yml"

# 数据目录的属主必须交给容器内的运行用户。
# 漏掉这一步的现象是容器起得来但立刻退出，日志里只有
# 「unable to open database file」，完全不提权限。
contains "数据目录已 chown 给容器运行用户" \
  "-R 65532:65532 $DEPLOY/data" \
  "$(cat "$CHOWN_LOG" 2>/dev/null || echo '')"

# 记录第一次的密钥与状态，用于稍后比对
KEY1="$(grep '^MASTER_KEY=' "$DEPLOY/.env")"
SECRET1="$(grep '^SESSION_SECRET=' "$DEPLOY/.env")"

# ══════════════════ 场景 2：升级（同参数）══════════════════
echo
echo "── 场景 2：已有文件 → 升级（不传参数）──"
# 造一个「已经有库」的痕迹，让判定更接近真实
mkdir -p "$DEPLOY/data" && echo "fake-db" > "$DEPLOY/data/app.db"

out="$(run_deploy)"
contains "识别为升级" "检测到已安装" "$out"
contains "提示保留配置文件" "保留" "$out"

check "MASTER_KEY 未被改动" "$KEY1" "$(grep '^MASTER_KEY=' "$DEPLOY/.env")"
check "SESSION_SECRET 未被改动" "$SECRET1" "$(grep '^SESSION_SECRET=' "$DEPLOY/.env")"
check "数据库文件未被删除" "fake-db" "$(cat "$DEPLOY/data/app.db")"
check "备份文件已生成" "1" "$(ls "$DEPLOY"/.env.bak.* 2>/dev/null | wc -l | tr -d ' ')"

# ══════════════════ 场景 3：端口保持（关键回归）══════════════════
echo
echo "── 场景 3：自定义端口部署后，升级是否保住端口 ──"
rm -rf "$DEPLOY"; mkdir -p "$DEPLOY"
run_deploy --port 9000 >/dev/null
comp="$(cat "$DEPLOY/docker-compose.yml")"
contains "首次用的是 9000" "127.0.0.1:9000:8787" "$comp"
check_compose_structure "compose 结构无空映射（自定义端口）" "$DEPLOY/docker-compose.yml"

# 再跑一次且**不带 --port** —— 这正是会踩坑的场景
run_deploy >/dev/null
comp="$(cat "$DEPLOY/docker-compose.yml")"
contains "升级后仍是 9000（未被改回默认）" "127.0.0.1:9000:8787" "$comp"
not_contains "没有被改回 8787" "127.0.0.1:8787:8787" "$comp"
state="$(cat "$DEPLOY/.deploy-state")"
contains ".deploy-state 记下宿主端口" "HOST_PORT=9000" "$state"
check ".env 里仍是容器端口 8787" "PORT=8787" "$(grep '^PORT=' "$DEPLOY/.env")"

# ══════════════════ 场景 4：1panel-network 存在 ══════════════════
echo
echo "── 场景 4：1panel-network 存在时加入网络 ──"
rm -rf "$DEPLOY"; mkdir -p "$DEPLOY"
STUB_HAS_PANEL_NETWORK=1 run_deploy >/dev/null
comp="$(cat "$DEPLOY/docker-compose.yml")"
contains "compose 声明了 networks" "networks:" "$comp"
contains "加入 1panel-network" "- 1panel-network" "$comp"
contains "声明为外部网络" "external: true" "$comp"
check_compose_structure "compose 结构无空映射（含 1panel-network）" "$DEPLOY/docker-compose.yml"

# ══════════════════ 场景 5：参数校验 ══════════════════
echo
echo "── 场景 5：参数与密码策略校验 ──"
rm -rf "$DEPLOY"; mkdir -p "$DEPLOY"
out="$(deploy --dir "$DEPLOY" --port abc --yes 2>&1)"; contains "非数字端口被拒" "必须是数字" "$out"
out="$(deploy --dir "$DEPLOY" --port 99999 --yes 2>&1)"; contains "越界端口被拒" "超出范围" "$out"
out="$(deploy --dir "$DEPLOY" --admin-password short1 --yes 2>&1)"; contains "过短密码被拒" "至少 8 位" "$out"
out="$(deploy --dir "$DEPLOY" --admin-password 12345678 --yes 2>&1)"; contains "纯数字密码被拒" "需要包含至少一个字母" "$out"
out="$(deploy --dir "$DEPLOY" --admin-password abcdefgh --yes 2>&1)"; contains "纯字母密码被拒" "需要包含至少一个数字" "$out"
out="$(deploy --dir "$DEPLOY" --yes 2>&1)"; contains "非交互缺密码被拒" "必须用 --admin-password" "$out"
out="$(deploy --bogus 2>&1)"; contains "未知参数被拒" "未知参数" "$out"

# ══════════════════ 场景 6：卸载与清除 ══════════════════
echo
echo "── 场景 6：卸载保留数据，purge 要求确认 ──"
rm -rf "$DEPLOY"; mkdir -p "$DEPLOY"
run_deploy >/dev/null
out="$(deploy --dir "$DEPLOY" --uninstall --yes 2>&1)"
contains "卸载提示数据保留" "数据保留" "$out"
[ -f "$DEPLOY/.env" ] && check "卸载后 .env 仍在" "yes" "yes" || check "卸载后 .env 仍在" "yes" "no"

out="$(deploy --dir "$DEPLOY" --purge --yes 2>&1)"
contains "purge 拒绝 --yes" "不接受 --yes" "$out"
[ -d "$DEPLOY" ] && check "purge 被拒后目录仍在" "yes" "yes" || check "purge 被拒后目录仍在" "yes" "no"

# ══════════════════ 目录迁移 ══════════════════

reset_migration() {
  rm -rf "$WORK/opt" "$WORK/legacy" "$DOCKER_STATE"
  mkdir -p "$WORK/opt" "$WORK/legacy/data" "$DOCKER_STATE"
  : > "$DOCKER_LOG"
  cp "$DEPLOY_ENV_FIXTURE" "$WORK/legacy/.env"
  printf 'database\n' > "$WORK/legacy/data/app.db"
  printf 'uncheckpointed-wal\n' > "$WORK/legacy/data/app.db-wal"
  printf 'shared-memory\n' > "$WORK/legacy/data/app.db-shm"
  printf 'HOST_PORT=9443\nHOST_BIND=0.0.0.0\nHOST_USERNAME=operator\nHOST_PROJECT=tgs\n' > "$WORK/legacy/.deploy-state"
  printf 'services:\n  panel:\n    image: old\n' > "$WORK/legacy/docker-compose.yml"
}

migrate() {
  STUB_CONTAINER_EXISTS=1 STUB_EXISTING_DIR="$WORK/legacy" deploy --yes "$@" 2>&1
}

DEPLOY_ENV_FIXTURE="$WORK/original.env"
cp "$DEPLOY/.env" "$DEPLOY_ENV_FIXTURE"

echo
echo "── 场景 7：自动迁移到 /opt 并保留数据库、WAL、密钥和端口 ──"
reset_migration
out="$(migrate)"; status=$?
check "迁移成功" "0" "$status"
contains "显示迁移完成" "迁移完成" "$out"
check "原目录配置未改动" "$(cat "$DEPLOY_ENV_FIXTURE")" "$(cat "$WORK/legacy/.env")"
check "目标目录密钥未改动" "$(grep '^MASTER_KEY=' "$DEPLOY_ENV_FIXTURE")" "$(grep '^MASTER_KEY=' "$DEPLOY/.env")"
check "目标目录会话密钥未改动" "$(grep '^SESSION_SECRET=' "$DEPLOY_ENV_FIXTURE")" "$(grep '^SESSION_SECRET=' "$DEPLOY/.env")"
check "数据库完整复制" "database" "$(cat "$DEPLOY/data/app.db")"
check "WAL 完整复制" "uncheckpointed-wal" "$(cat "$DEPLOY/data/app.db-wal")"
check "SHM 完整复制" "shared-memory" "$(cat "$DEPLOY/data/app.db-shm")"
check "原数据库保留" "database" "$(cat "$WORK/legacy/data/app.db")"
contains "沿用端口与绑定地址" "0.0.0.0:9443:8787" "$(cat "$DEPLOY/docker-compose.yml")"
contains "记录独立 Compose 项目" "HOST_PROJECT=tgs-migration-" "$(cat "$DEPLOY/.deploy-state")"
contains "打印可重复执行的升级命令" "curl -fsSL" "$out"
check "备份指向活动目录" "$DEPLOY" "$(cat "$WORK/legacy/.migrated-to")"
calls="$(cat "$DOCKER_LOG")"
contains "停机后再迁移" "stop --time 120 tgs-panel" "$calls"
contains "保留原容器供失败恢复" "rename tgs-panel tgs-panel-migration-" "$calls"
check "没有误操作原 Compose 项目" "no" "$([ -f "$DOCKER_STATE/wrong-project" ] && echo yes || echo no)"

# A repeated invocation must use the new data even when an old --dir is supplied.
printf 'new-conversation\n' > "$DEPLOY/data/app.db"
out="$(migrate --dir "$WORK/legacy")"; status=$?
check "旧命令再次运行成功" "0" "$status"
check "重复升级不被旧备份覆盖" "new-conversation" "$(cat "$DEPLOY/data/app.db")"
not_contains "重复执行不再次迁移" "将迁移：" "$out"

echo
echo "── 场景 8：迁移失败恢复旧容器 ──"
for failure in STUB_PULL_FAIL STUB_COPY_FAIL STUB_CONFIG_FAIL STUB_START_FAIL STUB_UNHEALTHY; do
  reset_migration
  out="$(export "$failure=1"; migrate)"; status=$?
  check "$failure 返回失败" "yes" "$([ "$status" -ne 0 ] && echo yes || echo no)"
  check "$failure 保留旧密钥" "$(cat "$DEPLOY_ENV_FIXTURE")" "$(cat "$WORK/legacy/.env")"
  check "$failure 保留旧数据库" "database" "$(cat "$WORK/legacy/data/app.db")"
  if [ "$failure" = "STUB_PULL_FAIL" ]; then
    not_contains "拉取失败不停止旧容器" "stop --time" "$(cat "$DOCKER_LOG")"
  else
    check "$failure 恢复旧容器" "yes" "$([ -f "$DOCKER_STATE/restarted" ] && echo yes || echo no)"
    check "$failure 未误删原项目" "no" "$([ -f "$DOCKER_STATE/wrong-project" ] && echo yes || echo no)"
  fi
done

echo
echo "── 场景 9：阻止覆盖目标和丢失密钥 ──"
reset_migration
mkdir -p "$DEPLOY"
printf 'another-installation\n' > "$DEPLOY/.env"
out="$(migrate)"; status=$?
check "非空目标拒绝迁移" "yes" "$([ "$status" -ne 0 ] && echo yes || echo no)"
check "目标配置不被覆盖" "another-installation" "$(cat "$DEPLOY/.env")"
not_contains "冲突时不停止旧容器" "stop --time" "$(cat "$DOCKER_LOG")"
reset_migration
rm "$WORK/legacy/.env"
out="$(migrate)"; status=$?
check "旧密钥缺失时拒绝迁移" "yes" "$([ "$status" -ne 0 ] && echo yes || echo no)"
not_contains "缺少密钥不停止容器" "stop --time" "$(cat "$DOCKER_LOG")"

echo
echo "── 场景 10：1Panel、已在 /opt 内、无容器和独立数据卷 ──"
reset_migration
mkdir -p "$WORK/opt/1panel"
out="$(migrate)"; status=$?
check "1Panel 默认迁移成功" "0" "$status"
check "1Panel 使用 /opt 下的应用目录" "database" "$(cat "$WORK/opt/1panel/apps/telegram-session-adblock/data/app.db")"
out="$(migrate)"; status=$?
check "已在 /opt 内的实例升级成功" "0" "$status"
not_contains "/opt 内的实例不搬动" "将迁移：" "$out"
reset_migration
out="$(deploy --migrate-from "$WORK/legacy" --yes 2>&1)"; status=$?
check "容器不存在时可显式迁移" "0" "$status"
check "无容器迁移保留 WAL" "uncheckpointed-wal" "$(cat "$DEPLOY/data/app.db-wal")"
not_contains "无容器迁移不调用 stop" "stop --time" "$(cat "$DOCKER_LOG")"
reset_migration
mkdir -p "$WORK/volume"
printf 'volume-database\n' > "$WORK/volume/app.db"
out="$(STUB_DATA_MOUNT="$WORK/volume" migrate)"; status=$?
check "读取真实数据卷迁移成功" "0" "$status"
check "优先复制实际挂载的数据" "volume-database" "$(cat "$DEPLOY/data/app.db")"
reset_migration
rm "$WORK/legacy/.deploy-state"
out="$(STUB_PORT=9555 STUB_BIND=0.0.0.0 migrate)"; status=$?
check "没有状态文件时可迁移" "0" "$status"
contains "从容器恢复原宿主端口" "0.0.0.0:9555:8787" "$(cat "$DEPLOY/docker-compose.yml")"
reset_migration
out="$(deploy --dir "$WORK/new-outside-opt" --yes --admin-password TestPass1234 2>&1)"; status=$?
check "新安装不能放在 /opt 外" "yes" "$([ "$status" -ne 0 ] && echo yes || echo no)"

if [ "$(uname -s)" = "Linux" ]; then
  echo
  echo "── 场景 11：并发部署与符号链接 ──"
  reset_migration
  exec 8>"$WORK/opt/.tgs-deploy.lock"
  flock -n 8
  out="$(migrate)"; status=$?
  check "并发部署被拒绝" "yes" "$([ "$status" -ne 0 ] && echo yes || echo no)"
  not_contains "并发运行不停止旧容器" "stop --time" "$(cat "$DOCKER_LOG")"
  flock -u 8
  exec 8>&-
  mkdir -p "$WORK/outside"
  ln -s "$WORK/outside" "$WORK/opt/link"
  out="$(deploy --dir "$WORK/opt/link/new" --yes --admin-password TestPass1234 2>&1)"; status=$?
  check "不能通过符号链接安装到 /opt 外" "yes" "$([ "$status" -ne 0 ] && echo yes || echo no)"
fi

# ══════════════════ 结果 ══════════════════
echo
printf '\033[1m通过 %d 项，失败 %d 项\033[0m\n' "$PASS" "$FAIL"
rm -rf "$WORK"
[ "$FAIL" -eq 0 ]
