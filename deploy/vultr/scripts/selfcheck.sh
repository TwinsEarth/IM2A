#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# selfcheck.sh - startup self-check for im2a-gateway.service (ExecStartPost).
#
# Waits for the gateway listener and requires an AUTHENTICATED /healthz == 200, proving both
# "the process still runs" and "the admin token from /etc/im2a/env actually works". Never
# prints the token. Exits non-zero on timeout, which makes the systemd unit fail and
# Restart=always retry (see deploy/vultr/systemd/im2a-gateway.service).
#
# Env overrides (all optional):
#   IM2A_ENV_FILE  default /etc/im2a/env
#   IM2A_PORT      default 18080
#   IM2A_SELFCHECK_TIMEOUT  default 120 (seconds)
set -uo pipefail

ENV_FILE="${IM2A_ENV_FILE:-/etc/im2a/env}"
PORT="${IM2A_PORT:-18080}"
TIMEOUT="${IM2A_SELFCHECK_TIMEOUT:-120}"
BASE="http://127.0.0.1:${PORT}"

TOKEN=""
if [ -r "$ENV_FILE" ]; then
  TOKEN="$(sed -n 's/^IM2A_ADMIN_TOKEN=//p' "$ENV_FILE" 2>/dev/null | tail -1)"
fi
if [ -z "$TOKEN" ]; then
  echo "[selfcheck] WARN: no IM2A_ADMIN_TOKEN in $ENV_FILE; /healthz is unauthenticated (dev mode)" >&2
fi

started=$SECONDS
while [ $((SECONDS - started)) -lt "$TIMEOUT" ]; do
  if ss -lnt 2>/dev/null | grep -qE ":${PORT}[[:space:]]"; then
    if [ -n "$TOKEN" ]; then
      code="$(curl -sS -o /dev/null -w '%{http_code}' -m 3 \
        -H "Authorization: Bearer ${TOKEN}" "${BASE}/healthz" 2>/dev/null || true)"
    else
      code="$(curl -sS -o /dev/null -w '%{http_code}' -m 3 "${BASE}/healthz" 2>/dev/null || true)"
    fi
    if [ "$code" = "200" ]; then
      echo "[selfcheck] OK: listening on 127.0.0.1:${PORT}, authenticated /healthz=200 (${SECONDS}s)"
      exit 0
    fi
    echo "[selfcheck] listener up but /healthz=$code; retrying..." >&2
  fi
  sleep 2
done

echo "[selfcheck] FAIL: gateway not healthy on ${BASE}/healthz within ${TIMEOUT}s" >&2
echo "[selfcheck] hint: journalctl -u im2a-gateway -n 50 --no-pager" >&2
exit 1
