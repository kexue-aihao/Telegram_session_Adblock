#!/usr/bin/env bash
set -euo pipefail

# Run on an isolated Linux CI runner after building tgs:ci.
[ "$(uname -s)" = "Linux" ] && [ "$(id -u)" = "0" ] || exit 1
SCRIPT="$(realpath scripts/deploy.sh)"
SMOKE_LEGACY="$(mktemp -d /tmp/tgs-migration-docker.XXXXXX)"
SMOKE_TARGET="/opt/tgs-migration-ci-$$"
SMOKE_FAILED="${SMOKE_TARGET}-rollback"
SMOKE_CONTAINER="tgs-migration-ci-$$"
SMOKE_PROJECT="tgs-legacy-ci-$$"

cleanup() {
  local container_id directory
  while IFS= read -r container_id; do
    [ -z "$container_id" ] || command docker rm -f "$container_id" >/dev/null
  done < <(command docker ps -aq --filter "name=$SMOKE_CONTAINER")
  for directory in "$SMOKE_LEGACY" "$SMOKE_TARGET" "$SMOKE_FAILED"*; do
    case "$directory" in /tmp/tgs-migration-docker.*|/opt/tgs-migration-ci-*) rm -rf -- "$directory" ;; esac
  done
}
trap cleanup EXIT

mkdir -p "$SMOKE_LEGACY/data"
chmod 700 "$SMOKE_LEGACY"
chown 65532:65532 "$SMOKE_LEGACY/data"
cat > "$SMOKE_LEGACY/.env" <<EOF
APP_ENV=production
HOST=0.0.0.0
PORT=8787
DATABASE_URL=file:/data/app.db
MASTER_KEY=$(openssl rand -hex 32)
SESSION_SECRET=$(openssl rand -hex 48)
ADMIN_PASSWORD=MigrationTest1234
EOF
chmod 600 "$SMOKE_LEGACY/.env"
cat > "$SMOKE_LEGACY/.deploy-state" <<EOF
HOST_PORT=18787
HOST_BIND=127.0.0.1
HOST_PROJECT=$SMOKE_PROJECT
EOF
cat > "$SMOKE_LEGACY/docker-compose.yml" <<EOF
services:
  panel:
    image: tgs:ci
    container_name: $SMOKE_CONTAINER
    env_file: [.env]
    ports: ['127.0.0.1:18787:8787']
    volumes: ['./data:/data']
EOF
command docker compose -p "$SMOKE_PROJECT" -f "$SMOKE_LEGACY/docker-compose.yml" up -d
for attempt in $(seq 1 30); do
  if command docker exec "$SMOKE_CONTAINER" /tgs -healthcheck; then break; fi
  sleep 1
done
command docker exec "$SMOKE_CONTAINER" /tgs -healthcheck

python3 - "$SMOKE_LEGACY/data/app.db" <<'PY'
import sqlite3
import sys
with sqlite3.connect(sys.argv[1]) as db:
    db.execute("CREATE TABLE migration_probe (value TEXT)")
    db.execute("INSERT INTO migration_probe VALUES ('preserved')")
PY

# Use the image built in this job. Only registry downloads are replaced;
# Compose, mounts, stop/start/rename and health checks use the real Docker engine.
docker() {
  if [ "${1:-}" = "pull" ] || { [ "${1:-}" = "compose" ] && [ "${*: -1}" = "pull" ]; }; then
    command docker image inspect tgs:ci >/dev/null
  else
    command docker "$@"
  fi
}
run_migration() (
  local target="$1" fail_health="${2:-0}"
  source "$SCRIPT" --yes --dir "$target"
  IMAGE_REPO=tgs
  TAG=ci
  CONTAINER_NAME="$SMOKE_CONTAINER"
  if [ "$fail_health" = "1" ]; then
    wait_healthy() { die "Injected health check failure"; }
  fi
  main
)

run_migration "$SMOKE_TARGET"
cmp "$SMOKE_LEGACY/.env" "$SMOKE_TARGET/.env"
[ "$(command docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "$SMOKE_CONTAINER")" = "$SMOKE_TARGET/data" ]
python3 - "$SMOKE_TARGET/data/app.db" <<'PY'
import sqlite3
import sys
with sqlite3.connect(sys.argv[1]) as db:
    assert db.execute("SELECT value FROM migration_probe").fetchone() == ('preserved',)
PY
command docker exec "$SMOKE_CONTAINER" /tgs -healthcheck
printf 'Real Docker migration and SQLite preservation passed.\n'

# Do not call this function inside an if: Bash would disable errexit in its body.
set +e
run_migration "$SMOKE_FAILED" 1
result=$?
set -e
[ "$result" -ne 0 ]
[ "$(command docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "$SMOKE_CONTAINER")" = "$SMOKE_TARGET/data" ]
for attempt in $(seq 1 30); do
  if command docker exec "$SMOKE_CONTAINER" /tgs -healthcheck; then break; fi
  sleep 1
done
command docker exec "$SMOKE_CONTAINER" /tgs -healthcheck
printf 'Real Docker rollback restored the previous container.\n'
