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
STUB="$WORK/stub"
mkdir -p "$STUB" "$WORK/data"

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

# 控制桩件行为的环境变量：
#   STUB_HAS_PANEL_NETWORK=1  假装 1panel-network 存在
#   STUB_CONTAINER_EXISTS=1   假装容器已存在
cat > "$STUB/docker" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  info) exit 0 ;;
  network)
    [ "${3:-}" = "1panel-network" ] && [ "${STUB_HAS_PANEL_NETWORK:-0}" = "1" ] && exit 0
    exit 1 ;;
  inspect)
    if [ "${2:-}" = "--format" ]; then echo "healthy"; exit 0; fi
    [ "${STUB_CONTAINER_EXISTS:-0}" = "1" ] && exit 0
    exit 1 ;;
  compose)
    [ "${2:-}" = "version" ] && exit 0
    # compose 子命令一律当作成功
    exit 0 ;;
  rm) exit 0 ;;
esac
exit 0
EOF
chmod +x "$STUB/id" "$STUB/docker"
export PATH="$STUB:$PATH"
export STUB_HAS_PANEL_NETWORK=0
export STUB_CONTAINER_EXISTS=0

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

run_deploy() {
  # 用 env -i 隔离，避免外部环境变量串味
  bash "$SCRIPT" --dir "$WORK/data" --admin-password "TestPass1234" --yes "$@" 2>&1
}

# ══════════════════ 场景 1：首次安装 ══════════════════
echo
echo "── 场景 1：无任何已有文件 → 首次安装 ──"
out="$(run_deploy)"
contains "识别为首次部署" "未检测到已有安装" "$out"
[ -f "$WORK/data/.env" ] && check ".env 已生成" "yes" "yes" || check ".env 已生成" "yes" "no"
[ -f "$WORK/data/.deploy-state" ] && check ".deploy-state 已生成" "yes" "yes" || check ".deploy-state 已生成" "yes" "no"
[ -f "$WORK/data/docker-compose.yml" ] && check "compose 已生成" "yes" "yes" || check "compose 已生成" "yes" "no"

env1="$(cat "$WORK/data/.env")"
contains ".env 含 MASTER_KEY" "MASTER_KEY=" "$env1"
contains ".env 含 SESSION_SECRET" "SESSION_SECRET=" "$env1"
contains ".env 含 ADMIN_PASSWORD" "ADMIN_PASSWORD=TestPass1234" "$env1"
check ".env 内 PORT 固定为容器端口" "PORT=8787" "$(grep '^PORT=' "$WORK/data/.env")"

# 密钥长度必须是 MASTER_KEY=64 hex、SESSION_SECRET=96 hex
mk_len=$(grep '^MASTER_KEY=' "$WORK/data/.env" | cut -d= -f2 | tr -d '\r' | wc -c)
check "MASTER_KEY 为 64 位十六进制（64+换行=65）" "65" "$mk_len"

comp1="$(cat "$WORK/data/docker-compose.yml")"
contains "compose 映射到 127.0.0.1:8787" "127.0.0.1:8787:8787" "$comp1"
contains "compose 挂载 ./data" "./data:/data" "$comp1"
contains "compose 用二进制自检做探针" "['CMD', '/tgs', '-healthcheck']" "$comp1"
not_contains "无 1panel-network 时不写 networks 段" "1panel-network" "$comp1"
check_compose_structure "compose 结构无空映射（无 1panel-network）" "$WORK/data/docker-compose.yml"

# 数据目录的属主必须交给容器内的运行用户。
# 漏掉这一步的现象是容器起得来但立刻退出，日志里只有
# 「unable to open database file」，完全不提权限。
contains "数据目录已 chown 给容器运行用户" \
  "-R 65532:65532 $WORK/data/data" \
  "$(cat "$CHOWN_LOG" 2>/dev/null || echo '')"

# 记录第一次的密钥与状态，用于稍后比对
KEY1="$(grep '^MASTER_KEY=' "$WORK/data/.env")"
SECRET1="$(grep '^SESSION_SECRET=' "$WORK/data/.env")"

# ══════════════════ 场景 2：升级（同参数）══════════════════
echo
echo "── 场景 2：已有文件 → 升级（不传参数）──"
# 造一个「已经有库」的痕迹，让判定更接近真实
mkdir -p "$WORK/data/data" && echo "fake-db" > "$WORK/data/data/app.db"

out="$(run_deploy)"
contains "识别为升级" "检测到已安装" "$out"
contains "提示保留配置文件" "保留" "$out"

check "MASTER_KEY 未被改动" "$KEY1" "$(grep '^MASTER_KEY=' "$WORK/data/.env")"
check "SESSION_SECRET 未被改动" "$SECRET1" "$(grep '^SESSION_SECRET=' "$WORK/data/.env")"
check "数据库文件未被删除" "fake-db" "$(cat "$WORK/data/data/app.db")"
check "备份文件已生成" "1" "$(ls "$WORK/data"/.env.bak.* 2>/dev/null | wc -l | tr -d ' ')"

# ══════════════════ 场景 3：端口保持（关键回归）══════════════════
echo
echo "── 场景 3：自定义端口部署后，升级是否保住端口 ──"
rm -rf "$WORK/data"; mkdir -p "$WORK/data"
run_deploy --port 9000 >/dev/null
comp="$(cat "$WORK/data/docker-compose.yml")"
contains "首次用的是 9000" "127.0.0.1:9000:8787" "$comp"
check_compose_structure "compose 结构无空映射（自定义端口）" "$WORK/data/docker-compose.yml"

# 再跑一次且**不带 --port** —— 这正是会踩坑的场景
run_deploy >/dev/null
comp="$(cat "$WORK/data/docker-compose.yml")"
contains "升级后仍是 9000（未被改回默认）" "127.0.0.1:9000:8787" "$comp"
not_contains "没有被改回 8787" "127.0.0.1:8787:8787" "$comp"
state="$(cat "$WORK/data/.deploy-state")"
contains ".deploy-state 记下宿主端口" "HOST_PORT=9000" "$state"
check ".env 里仍是容器端口 8787" "PORT=8787" "$(grep '^PORT=' "$WORK/data/.env")"

# ══════════════════ 场景 4：1panel-network 存在 ══════════════════
echo
echo "── 场景 4：1panel-network 存在时加入网络 ──"
rm -rf "$WORK/data"; mkdir -p "$WORK/data"
STUB_HAS_PANEL_NETWORK=1 run_deploy >/dev/null
comp="$(cat "$WORK/data/docker-compose.yml")"
contains "compose 声明了 networks" "networks:" "$comp"
contains "加入 1panel-network" "- 1panel-network" "$comp"
contains "声明为外部网络" "external: true" "$comp"
check_compose_structure "compose 结构无空映射（含 1panel-network）" "$WORK/data/docker-compose.yml"

# ══════════════════ 场景 5：参数校验 ══════════════════
echo
echo "── 场景 5：参数与密码策略校验 ──"
rm -rf "$WORK/data"; mkdir -p "$WORK/data"
out="$(bash "$SCRIPT" --dir "$WORK/data" --port abc --yes 2>&1)"; contains "非数字端口被拒" "必须是数字" "$out"
out="$(bash "$SCRIPT" --dir "$WORK/data" --port 99999 --yes 2>&1)"; contains "越界端口被拒" "超出范围" "$out"
out="$(bash "$SCRIPT" --dir "$WORK/data" --admin-password short1 --yes 2>&1)"; contains "过短密码被拒" "至少 8 位" "$out"
out="$(bash "$SCRIPT" --dir "$WORK/data" --admin-password 12345678 --yes 2>&1)"; contains "纯数字密码被拒" "需要包含至少一个字母" "$out"
out="$(bash "$SCRIPT" --dir "$WORK/data" --admin-password abcdefgh --yes 2>&1)"; contains "纯字母密码被拒" "需要包含至少一个数字" "$out"
out="$(bash "$SCRIPT" --dir "$WORK/data" --yes 2>&1)"; contains "非交互缺密码被拒" "必须用 --admin-password" "$out"
out="$(bash "$SCRIPT" --bogus 2>&1)"; contains "未知参数被拒" "未知参数" "$out"

# ══════════════════ 场景 6：卸载与清除 ══════════════════
echo
echo "── 场景 6：卸载保留数据，purge 要求确认 ──"
rm -rf "$WORK/data"; mkdir -p "$WORK/data"
run_deploy >/dev/null
out="$(bash "$SCRIPT" --dir "$WORK/data" --uninstall --yes 2>&1)"
contains "卸载提示数据保留" "数据保留" "$out"
[ -f "$WORK/data/.env" ] && check "卸载后 .env 仍在" "yes" "yes" || check "卸载后 .env 仍在" "yes" "no"

out="$(bash "$SCRIPT" --dir "$WORK/data" --purge --yes 2>&1)"
contains "purge 拒绝 --yes" "不接受 --yes" "$out"
[ -d "$WORK/data" ] && check "purge 被拒后目录仍在" "yes" "yes" || check "purge 被拒后目录仍在" "yes" "no"

# ══════════════════ 结果 ══════════════════
echo
printf '\033[1m通过 %d 项，失败 %d 项\033[0m\n' "$PASS" "$FAIL"
rm -rf "$WORK"
[ "$FAIL" -eq 0 ]
