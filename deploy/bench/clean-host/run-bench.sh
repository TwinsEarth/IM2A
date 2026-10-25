#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# run-bench.sh - connection-gateway capacity benchmark for the CLEAN Ubuntu 24.04 host
#                134.175.243.146 / VM-0-5-ubuntu (4 vCPU, 8 GB RAM, 50 GB disk).
#
# COMPLIANCE RED LINE (2026-10-25, permanent): 106.53.197.47 is the user's PRODUCTION machine.
# Never connect to / read / write / scan it. This script only runs locally on the clean bench
# host and never dials any external host (its targets are 127.0.0.1 only).
#
# This host is the disposable/clean benchmark + deployment-validation machine (NOT production).
# It runs: Go 1.27.3 (/usr/local/go1.27.3), PostgreSQL 16 (native, loopback 127.0.0.1),
# conn-gateway + loadgen built from /home/ubuntu/im2a-bench/im2agent.
#
# Stages: 500 / 2000 / 5000 WS connections, rate = 1 frame/s/connection, with a 5000-conn
# 300 s steady-state stage. Records p50/p95/p99, dial/read errors, reconnects, gateway RSS,
# fds, threads, cgroup memory, host available memory and swap before/after every stage.
#
# Usage:  bash run-bench.sh            # runs all stages, logs under ~/im2a-bench/bench-results
#         nohup setsid bash run-bench.sh > bench-run.log 2>&1 &   # detached (recommended)
#
# Preconditions (see README):
#   * binaries at ~/im2a-bench/bin/{conn-gateway,loadgen} (build: go build ./cmd/...)
#   * nothing else heavy running on the box (this is a clean bench host)
#   * passwordless sudo for transient systemd units + sysctl
set -u

# Hard compliance guard: refuse to run if this host carries the banned production address.
if ip -4 addr 2>/dev/null | grep -q '106\.53\.197\.47' \
   || hostname -I 2>/dev/null | tr ' ' '\n' | grep -qx '106\.53\.197\.47'; then
  echo "[bench] FATAL: banned host (106.53.197.47 = user production) - refusing." >&2
  exit 1
fi

B=/home/ubuntu/im2a-bench
BIN=$B/bin
OUT=$B/bench-results
UNIT=conn-gw-bench
GWBIN=$BIN/conn-gateway
LG=$BIN/loadgen
TARGET=ws://127.0.0.1:18081/v1/ws
GW_PORT=18081

export OPENCLAW_ENABLED=off DSBOT_ENABLED=off
mkdir -p "$OUT"

cleanup() {
  sudo -n systemctl stop "$UNIT" 2>/dev/null || true
  sudo -n systemctl reset-failed "$UNIT" 2>/dev/null || true
}

free_line() {
  free -m | awk '/^Mem|内存/ {printf "avail=%sMB used=%sMB ", $7, $3} /^Swap|交换/ {printf "swap_used=%sMB", $3}'
}

gw_stat() {
  local pid="$1"
  if [ -r "/proc/$pid/status" ]; then
    echo "  [gw] rss_kb=$(awk '/VmRSS/{print $2}' "/proc/$pid/status") fds=$(sudo -n ls "/proc/$pid/fd" 2>/dev/null | wc -l) threads=$(awk '/Threads/{print $2}' "/proc/$pid/status") cgroup_mem=$(sudo -n systemctl show "$UNIT" -p MemoryCurrent --value) cpu_ticks=$(awk '{print $14+$15}' "/proc/$pid/stat")"
  else
    echo "  [gw] PID $pid gone"
  fi
}

stage() { # conns rate dur
  local n="$1" rate="$2" dur="$3"
  echo
  echo "########## STAGE conns=$n rate=${rate}/s/conn dur=$dur ##########"
  echo "before: $(free_line)"; gw_stat "$PID"
  local t0; t0=$(date +%s)
  "$LG" -target "$TARGET" -conns "$n" -rate "$rate" -dur "$dur" \
    -dev-insecure -user-base 1000 > "$OUT/loadgen-$n.log" 2>&1 &
  local lgpid=$!
  local elapsed=0
  while kill -0 "$lgpid" 2>/dev/null; do
    sleep 30
    elapsed=$(( $(date +%s) - t0 ))
    echo "  t+${elapsed}s: $(free_line)"; gw_stat "$PID"
  done
  wait "$lgpid"; local rc=$?
  local t1; t1=$(date +%s)
  echo "loadgen_exit=$rc wall=$((t1 - t0))s"
  echo "--- loadgen raw output ---"
  cat "$OUT/loadgen-$n.log"
  echo "after: $(free_line)"; gw_stat "$PID"
  curl -s -m 5 "http://127.0.0.1:${GW_PORT}/metrics" > "$OUT/metrics-$n.txt" 2>/dev/null || true
  echo "--- gateway /metrics (head) ---"
  head -25 "$OUT/metrics-$n.txt" 2>/dev/null || true
  echo "--- ss -s ---"
  ss -s | head -6
  echo "idle 30s before next stage..."
  sleep 30
}

echo "########## BENCH START $(date -Is) on $(hostname) ##########"
cleanup
echo "=== host before ==="; free -m; uptime; nproc
# accept 5k simultaneous handshakes (temporary, not persisted)
sudo -n sysctl -w net.core.somaxconn=8192 >/dev/null
sudo -n sysctl -w net.ipv4.tcp_max_syn_backlog=8192 >/dev/null
sudo -n sysctl -w net.ipv4.ip_local_port_range="10000 65000" >/dev/null
echo "sysctl: somaxconn=$(sysctl -n net.core.somaxconn) syn_backlog=$(sysctl -n net.ipv4.tcp_max_syn_backlog) port_range=$(sysctl -n net.ipv4.ip_local_port_range)"

echo "=== start conn-gateway as transient unit (LimitNOFILE=65535) ==="
sudo -n systemd-run --unit="$UNIT" --collect \
  -p LimitNOFILE=65535 -p MemoryAccounting=yes \
  -p StandardOutput=journal -p StandardError=journal \
  --setenv=OPENCLAW_ENABLED=off --setenv=DSBOT_ENABLED=off \
  "$GWBIN" -addr "127.0.0.1:${GW_PORT}" -uds-disable -echo -dev-insecure -debug >/dev/null 2>&1
for _ in $(seq 1 60); do
  curl -sf -m 2 "http://127.0.0.1:${GW_PORT}/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done
echo "healthz: $(curl -s -m 3 "http://127.0.0.1:${GW_PORT}/healthz")"
PID=$(sudo -n systemctl show "$UNIT" -p ExecMainPID --value)
echo "gateway_pid=$PID limitNOFILE=$(grep 'Max open files' "/proc/$PID/limits" | awk '{print $4}')"
sudo -n journalctl -u "$UNIT" -n 8 --no-pager 2>/dev/null | tail -8

stage 500  1 60s
stage 2000 1 60s
stage 5000 1 300s

echo
echo "=== final host ==="; free -m; uptime
cleanup
echo "=== BENCH DONE $(date -Is) ==="
