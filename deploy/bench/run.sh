#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# run.sh - generic helper for PG-gated tests / future benchmark hosts.
#
# !!! COMPLIANCE RED LINE (2026-10-25, permanent) !!!
# 106.53.197.47 is the user's PRODUCTION machine: it must NEVER be connected to, read, written,
# scanned or used as a target by any script or document. This script refuses to run when the
# local host carries that address (hard guard below) and never dials any IP itself.
# Allowed targets only: Vultr 202.182.123.154 (deployment) and the clean host 134.175.243.146
# (bench/PG16). The active benchmark workflow lives in deploy/bench/clean-host/.
#
# Commands:
#   bash run.sh env                 # print host facts, toolchain, PG reachability (read-only)
#   bash run.sh pg-init             # (idempotent) create database im2a_ci in a LOCAL PG container
#   bash run.sh pg-test             # run the PG-gated migration/seq/ledger contract tests
#   bash run.sh build               # build cmd/conn-gateway + cmd/loadgen (when present)
#   bash run.sh bench [options]     # BLOCKED by default; requires IM2A_ALLOW_PROD_BENCH=1
#       --conns N        connections (default 500)
#       --rate R         frames/sec PER CONNECTION (default 50)
#       --dur D          duration, e.g. 30s (default 30s)
#       --port P         conn-gateway port (default 18081)
#       --test-loss      expect zero message loss; exit non-zero if acked != sent
#   bash run.sh stop                # stop transient bench units
#   bash run.sh selftest            # verify configured NOFILE (bench machine only)
#   bash run.sh cleanup [--drop-db] # stop bench, remove logs/bin; --drop-db drops im2a_ci
#
# Environment:
#   BENCH_ROOT  default /root/im2a-bench
#   GO          default /usr/local/go1.27.3/bin/go
#   PG_CONTAINER default hai-postgres ; PG_DB default im2a_ci ; PG_USER default hai
#   PG_DSN      if exported, pg-test uses it instead of building one from docker inspect.
#   IM2A_ALLOW_PROD_BENCH=1  explicit human approval for the (blocked-by-default) bench path.
set -euo pipefail

# ---------------------------------------------------------------------------------------------
# HARD COMPLIANCE GUARD: never run on the user's production host.
# ---------------------------------------------------------------------------------------------
if ip -4 addr 2>/dev/null | grep -q '106\.53\.197\.47' \
   || hostname -I 2>/dev/null | tr ' ' '\n' | grep -qx '106\.53\.197\.47'; then
  echo "[bench] FATAL: this host is the user's production machine (106.53.197.47) - banned. Refusing." >&2
  exit 1
fi

BENCH_ROOT="${BENCH_ROOT:-/root/im2a-bench}"
GO="${GO:-/usr/local/go1.27.3/bin/go}"
SRC="$BENCH_ROOT/im2agent"
BIN="$BENCH_ROOT/bin"
LOG="$BENCH_ROOT/logs"
PG_CONTAINER="${PG_CONTAINER:-hai-postgres}"
PG_DB="${PG_DB:-im2a_ci}"
PG_USER="${PG_USER:-hai}"
PG_PORT="${PG_PORT:-5432}"
GW_UNIT="im2a-bench-gw"
GW_PORT=18081
CONNS=500
RATE=50
DUR=30s
TEST_LOSS=0

log()  { printf '[bench] %s\n' "$*"; }
die()  { printf '[bench] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

pg_password() {
  # Never printed: used only to build PG_DSN for the test process.
  docker inspect "$PG_CONTAINER" --format '{{range .Config.Env}}{{println .}}{{end}}' \
    | sed -n 's/^POSTGRES_PASSWORD=//p'
}

pg_dsn() {
  if [ -n "${PG_DSN:-}" ]; then printf '%s' "$PG_DSN"; return 0; fi
  local pw; pw="$(pg_password)"
  [ -n "$pw" ] || die "cannot read POSTGRES_PASSWORD from container $PG_CONTAINER"
  printf 'host=127.0.0.1 port=%s user=%s password=%s dbname=%s sslmode=disable' \
    "$PG_PORT" "$PG_USER" "$pw" "$PG_DB"
}

cmd_env() {
  log "host: $(hostname) / $(. /etc/os-release; echo "$PRETTY_NAME") / kernel $(uname -r)"
  log "cpu: $(nproc) x $(lscpu | sed -n 's/^Model name: *//p' | head -1)"
  free -m | sed 's/^/[bench] /'
  df -h / | sed 's/^/[bench] /'
  log "somaxconn=$(sysctl -n net.core.somaxconn 2>/dev/null) syn_backlog=$(sysctl -n net.ipv4.tcp_max_syn_backlog 2>/dev/null) port_range=$(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null)"
  log "nofile: shell=$(ulimit -n) hard=$(ulimit -Hn)"
  log "go($GO): $("$GO" version 2>/dev/null || echo MISSING)"
  log "go default: $(command -v go || echo 'none') -> $(go version 2>/dev/null || true)"
  log "source: $SRC ($( [ -d "$SRC/internal" ] && echo present || echo MISSING))"
  log "PG container $PG_CONTAINER: $(docker inspect -f '{{.State.Status}} {{.Config.Image}}' "$PG_CONTAINER" 2>/dev/null || echo MISSING)"
  local tabs
  tabs="$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -tAc \
    "select count(*) from information_schema.tables where table_schema='public'" 2>/dev/null || echo ERR)"
  log "PG db $PG_DB: public tables=$tabs"
  log "existing services (DO NOT TOUCH): nginx=$(systemctl is-active nginx 2>/dev/null) im2a-gateway=$(systemctl is-active im2a-gateway 2>/dev/null) openclaw-gateway=$(systemctl is-active openclaw-gateway 2>/dev/null) w2m-rabbit=$(systemctl is-active w2m-rabbit 2>/dev/null)"
}

cmd_pg_init() {
  need docker
  local exists
  exists="$(docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -tAc \
    "select 1 from pg_database where datname='$PG_DB'" 2>/dev/null | tr -d '[:space:]')"
  if [ "$exists" = "1" ]; then
    log "database $PG_DB already exists"
  else
    docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -c "CREATE DATABASE $PG_DB OWNER $PG_USER" >/dev/null
    log "database $PG_DB created"
  fi
  docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -c 'select version();' | sed 's/^/[bench] /'
}

run_go_test() { # label, package, -run pattern(optional)
  local label="$1" pkg="$2" pattern="${3:-}"
  local ds; ds="$(pg_dsn)"
  log "=== $label ==="
  if [ -n "$pattern" ]; then
    ( cd "$SRC" && PG_DRIVER=pgx PG_DSN="$ds" "$GO" test "$pkg" -run "$pattern" -v -count=1 )
  else
    ( cd "$SRC" && PG_DRIVER=pgx PG_DSN="$ds" "$GO" test "$pkg" -count=1 )
  fi
}

cmd_pg_test() {
  need docker
  [ -d "$SRC/internal" ] || die "source tree missing: $SRC (sync with: scp bench-src.tgz, see README)"
  cmd_pg_init >/dev/null
  local rc=0
  run_go_test "1) migrations on a FRESH PG16" ./internal/store/ 'TestMigrationContract' || rc=1
  run_go_test "2) seq persistence + ledger consolidation" ./internal/api/ 'TestSeqPersistence|TestLedgerConsolidation' || rc=1
  run_go_test "3) real_test sweep (api + store)" ./internal/api/ || rc=1
  run_go_test "4) real_test sweep (store)" ./internal/store/ || rc=1
  [ "$rc" -eq 0 ] && log "PG CONTRACT TESTS: PASS" || log "PG CONTRACT TESTS: FAIL (see raw output above; migration/code owners must fix - do NOT patch SQL here)"
  return "$rc"
}

cmd_build() {
  [ -d "$SRC" ] || die "source tree missing: $SRC"
  mkdir -p "$BIN"
  log "building cmd/conn-gateway and cmd/loadgen with $("$GO" version)"
  ( cd "$SRC" && "$GO" build -o "$BIN/conn-gateway" ./cmd/conn-gateway )
  ( cd "$SRC" && "$GO" build -o "$BIN/loadgen" ./cmd/loadgen )
  ls -l "$BIN"
}

build_if_needed() {
  [ -x "$BIN/conn-gateway" ] && [ -x "$BIN/loadgen" ] || cmd_build
}

cmd_bench() {
  build_if_needed
  mkdir -p "$LOG"
  local ts; ts="$(date +%Y%m%dT%H%M%SZ)"
  local outdir="$LOG/$ts-conns$CONNS"
  mkdir -p "$outdir"
  systemctl stop "$GW_UNIT" 2>/dev/null || true
  systemctl reset-failed "$GW_UNIT" 2>/dev/null || true
  if ss -lnt 2>/dev/null | grep -qE ":${GW_PORT}[[:space:]]"; then
    die "port $GW_PORT already in use (refusing to disturb existing services)"
  fi
  log "starting conn-gateway as transient unit $GW_UNIT on :$GW_PORT (LimitNOFILE=65535)"
  systemd-run --unit="$GW_UNIT" --collect \
    -p LimitNOFILE=65535 -p MemoryAccounting=yes \
    -p StandardOutput=journal -p StandardError=journal \
    -p Restart=no -p TimeoutStopSec=20 \
    "$BIN/conn-gateway" -addr "127.0.0.1:${GW_PORT}" -uds-disable -echo -dev-insecure >/dev/null
  local i
  for i in $(seq 1 30); do
    ss -lnt 2>/dev/null | grep -qE ":${GW_PORT}[[:space:]]" && break
    sleep 0.5
  done
  ss -lnt 2>/dev/null | grep -qE ":${GW_PORT}[[:space:]]" || { journalctl -u "$GW_UNIT" -n 30 --no-pager; die "conn-gateway did not listen"; }
  log "conn-gateway up: $(systemctl show "$GW_UNIT" -p MemoryCurrent --value) bytes cgroup memory"
  log "running loadgen: -conns $CONNS -rate $RATE -dur $DUR"
  set +e
  "$BIN/loadgen" -conns "$CONNS" -rate "$RATE" -dur "$DUR" -dev-insecure -user-base 1000 2>&1 | tee "$outdir/loadgen.log"
  local lg_rc=$?
  set -e
  {
    echo "conns=$CONNS rate=$RATE dur=$DUR port=$GW_PORT"
    echo "loadgen_exit=$lg_rc"
    echo "gateway_memory_current=$(systemctl show "$GW_UNIT" -p MemoryCurrent --value)"
    echo "host_free_m:"
    free -m
    echo "top_rss:"
    ps -eo pid,rss,comm,args --sort=-rss | head -8
  } | tee "$outdir/summary.txt"
  if [ "$TEST_LOSS" -eq 1 ] && [ "$lg_rc" -ne 0 ]; then
    log "ZERO-LOSS CHECK FAILED (loadgen exit $lg_rc)"
    return "$lg_rc"
  fi
  log "results saved: $outdir"
}

cmd_stop() {
  systemctl stop "$GW_UNIT" 2>/dev/null || true
  systemctl reset-failed "$GW_UNIT" 2>/dev/null || true
  log "stopped $GW_UNIT (loadgen runs in the foreground of cmd_bench and exits on its own)"
}

cmd_selftest() {
  log "shell: soft=$(ulimit -Sn) hard=$(ulimit -Hn)"
  # NOTE: LimitNOFILE is a *service* property; `systemd-run --scope` rejects it
  # ("Unknown assignment"). Verify in a transient service instead, which is also how
  # conn-gateway gets started by cmd_bench.
  local got
  got="$(systemd-run --wait --pipe --collect -p LimitNOFILE=65535 /bin/bash -c 'ulimit -Sn' 2>/dev/null | tr -d '[:space:]')"
  log "systemd transient service: soft nofile=$got"
  [ "$got" = "65535" ] || die "systemd-run LimitNOFILE check failed (got '$got')"
  [ "$(ulimit -Hn)" -ge 65535 ] || die "hard nofile < 65535 in this shell; log out/in or check /etc/security/limits.conf"
  log "NOFILE selftest OK"
}

cmd_cleanup() {
  cmd_stop
  rm -rf "$BIN" "$LOG"
  log "removed $BIN and $LOG (source tree kept at $SRC)"
  if [ "${1:-}" = "--drop-db" ]; then
    docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d postgres -c "DROP DATABASE IF EXISTS $PG_DB" >/dev/null
    log "dropped database $PG_DB"
  else
    log "database $PG_DB kept; drop it when verification is done (--drop-db)"
  fi
  log "NOTE: only temporary artifacts were cleaned. This helper must only be used on allowed hosts"
  log "      (Vultr 202.182.123.154 / clean bench host 134.175.243.146). Never on 106.53.197.47."
}

# ---------------------------------------------------------------- argument parsing
CMD="${1:-env}"; shift || true
case "$CMD" in
  env|pg-init|pg-test|build|stop|selftest|cleanup) ;;
  bench)
    while [ $# -gt 0 ]; do
      case "$1" in
        --conns) CONNS="${2:?}"; shift ;;
        --rate)  RATE="${2:?}"; shift ;;
        --dur)   DUR="${2:?}"; shift ;;
        --port)  GW_PORT="${2:?}"; shift ;;
        --test-loss) TEST_LOSS=1 ;;
        *) die "unknown bench option: $1" ;;
      esac
      shift
    done
    # SAFETY GUARD: bench is opt-in only (clean bench host). The hard compliance guard above
    # already refuses to run on the user's production machine 106.53.197.47.
    if [ "${IM2A_ALLOW_PROD_BENCH:-0}" != "1" ]; then
      die "REFUSING 'bench' by default. Use a clean dedicated benchmark host and re-run with IM2A_ALLOW_PROD_BENCH=1. NEVER target the user's production machine (106.53.197.47) - it is banned."
    fi ;;
  -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
  *) die "unknown command: $CMD (try --help)" ;;
esac

case "$CMD" in
  env)      cmd_env ;;
  pg-init)  cmd_pg_init ;;
  pg-test)  cmd_pg_test ;;
  build)    cmd_build ;;
  bench)    cmd_bench ;;
  stop)     cmd_stop ;;
  selftest) cmd_selftest ;;
  cleanup)  cmd_cleanup "${1:-}" ;;
esac
