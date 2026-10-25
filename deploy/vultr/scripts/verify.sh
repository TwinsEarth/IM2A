#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# verify.sh - one-shot, READ-ONLY acceptance evidence for the IM2A/OpenClaw deployment.
#
# Prints (and asserts) exactly the facts required by the deployment task:
#   service state, listener, authenticated /healthz, anonymous /v1/plugins,
#   anonymous /v1/about/attribution?format=text, public vhost, memory headroom (>=80 MB),
#   top-RSS processes, MIT attribution check (--runtime), openclaw CLI/weixin channel state.
#
# Nothing here mutates the host. Exit code 0 only if every hard assertion passes.
#
# Usage:
#   bash /opt/im2a/scripts/verify.sh            # from the deployed copy
#   bash deploy/vultr/scripts/verify.sh         # from the repo (same checks)
# Overrides: IM2A_ENV_FILE, IM2A_PORT, IM2A_PUBLIC_HOST, IM2A_MIN_AVAILABLE_MB
set -uo pipefail

ENV_FILE="${IM2A_ENV_FILE:-/etc/im2a/env}"
PORT="${IM2A_PORT:-18080}"
PUBLIC_HOST="${IM2A_PUBLIC_HOST:-im2a.202.182.123.154.nip.io}"
MIN_AVAIL_MB="${IM2A_MIN_AVAILABLE_MB:-80}"
BASE="http://127.0.0.1:${PORT}"
FAIL=0

ok()   { printf '  [PASS] %s\n' "$1"; }
bad()  { printf '  [FAIL] %s\n' "$1"; FAIL=$((FAIL + 1)); }
info() { printf '  [INFO] %s\n' "$1"; }
head2() { printf '\n===== %s =====\n' "$1"; }

TOKEN=""
if [ -r "$ENV_FILE" ]; then
  TOKEN="$(sed -n 's/^IM2A_ADMIN_TOKEN=//p' "$ENV_FILE" 2>/dev/null | tail -1)"
fi

# ---------------------------------------------------------------------------------------------
head2 "1. systemctl status im2a-gateway --no-pager | head -15"
systemctl status im2a-gateway --no-pager 2>&1 | head -15
active="$(systemctl is-active im2a-gateway 2>/dev/null || true)"
[ "$active" = "active" ] && ok "im2a-gateway is active" || bad "im2a-gateway is '$active'"

head2 "2. ss -lntp | grep 18080"
ss -lntp 2>/dev/null | grep -E ":${PORT}[[:space:]]" || bad "nothing listening on ${PORT}"
ss -lntp 2>/dev/null | grep -qE ":${PORT}[[:space:]]" && ok "listener present on :${PORT}"

head2 "3. curl -sS -m 5 -i http://127.0.0.1:${PORT}/healthz  (Authorization: Bearer \$IM2A_ADMIN_TOKEN)"
if [ -z "$TOKEN" ]; then
  bad "IM2A_ADMIN_TOKEN not found in $ENV_FILE (dev mode: unauthenticated probe)"
  curl -sS -m 5 -i "${BASE}/healthz" 2>&1 | head -20
else
  curl -sS -m 5 -i -H "Authorization: Bearer ${TOKEN}" "${BASE}/healthz" 2>&1 | head -20
  code="$(curl -sS -o /dev/null -w '%{http_code}' -m 5 -H "Authorization: Bearer ${TOKEN}" "${BASE}/healthz" 2>/dev/null || true)"
  [ "$code" = "200" ] && ok "authenticated /healthz -> 200" || bad "authenticated /healthz -> ${code:-no-response}"
  code_noauth="$(curl -sS -o /dev/null -w '%{http_code}' -m 5 "${BASE}/healthz" 2>/dev/null || true)"
  [ "$code_noauth" = "401" ] && ok "unauthenticated /healthz -> 401 (token actually enforced)" \
    || info "unauthenticated /healthz -> ${code_noauth:-no-response}"
fi

head2 "4. curl -sS -m 5 http://127.0.0.1:${PORT}/v1/plugins  (anonymous, public readable)"
plugins="$(curl -sS -m 5 "${BASE}/v1/plugins" 2>/dev/null || true)"
printf '%s\n' "$plugins" | head -40
if printf '%s' "$plugins" | grep -q '"openclaw"'; then
  ok "/v1/plugins lists the openclaw plugin"
else
  bad "/v1/plugins does not list openclaw"
fi

head2 "5. curl -sS -m 5 'http://127.0.0.1:${PORT}/v1/about/attribution?format=text'  (anonymous)"
attr_text="$(curl -sS -m 5 "${BASE}/v1/about/attribution?format=text" 2>/dev/null || true)"
printf '%s\n' "$attr_text" | head -40
printf '%s' "$attr_text" | grep -qi 'MIT' && ok "attribution text declares MIT" || bad "attribution text missing MIT"
printf '%s' "$attr_text" | grep -q 'OpenClaw Foundation' && ok "attribution text keeps upstream copyright line" \
  || bad "attribution text missing 'OpenClaw Foundation'"

head2 "6. public vhost http://${PUBLIC_HOST}/  (nginx -> 127.0.0.1:${PORT})"
if command -v curl >/dev/null 2>&1; then
  curl -sS -m 8 -o /dev/null -w '  root: HTTP %{http_code} in %{time_total}s\n' "http://${PUBLIC_HOST}/" 2>&1 || true
  curl -sS -m 8 -o /dev/null -w '  /v1/plugins: HTTP %{http_code}\n' "http://${PUBLIC_HOST}/v1/plugins" 2>&1 || true
  curl -sS -m 8 -o /dev/null -w '  /v1/about/attribution: HTTP %{http_code}\n' "http://${PUBLIC_HOST}/v1/about/attribution" 2>&1 || true
  pub="$(curl -sS -m 8 "http://${PUBLIC_HOST}/v1/about/attribution" 2>/dev/null || true)"
  printf '%s' "$pub" | grep -q 'openclaw' && ok "public /v1/about/attribution is anonymous and lists openclaw" \
    || bad "public /v1/about/attribution failed or missing openclaw"
else
  info "curl not available"
fi

head2 "7. free -m  (RED LINE: available >= ${MIN_AVAIL_MB} MB)"
free -m
avail="$(free -m | awk '/^Mem|内存/ {print $7}' | head -1)"
case "$avail" in
  ''|*[!0-9]*) info "could not parse available memory" ;;
  *)
    if [ "$avail" -ge "$MIN_AVAIL_MB" ]; then
      ok "available=${avail}MB >= ${MIN_AVAIL_MB}MB red line"
    else
      bad "available=${avail}MB < ${MIN_AVAIL_MB}MB red line (lower MemoryMax or disable resident OpenClaw)"
    fi
    ;;
esac

head2 "8. ps -eo pid,rss,comm,args --sort=-rss | head -12"
ps -eo pid,rss,comm,args --sort=-rss 2>/dev/null | head -12
echo "--- im2a-gateway / openclaw RSS ---"
ps -eo pid,rss,comm,args 2>/dev/null | grep -E 'im2a-gateway|openclaw' | grep -v grep || echo "  (no im2a-gateway/openclaw process)"
sysmem="$(systemctl show im2a-gateway -p MemoryCurrent --value 2>/dev/null || echo '')"
[ -n "$sysmem" ] && echo "  systemd cgroup MemoryCurrent: ${sysmem} bytes"

head2 "9. OpenClaw runtime: version / on-demand CLI / weixin channel"
OC_BIN=""
if [ -r "$ENV_FILE" ]; then
  OC_BIN="$(sed -n 's/^OPENCLAW_BIN=//p' "$ENV_FILE" 2>/dev/null | tail -1)"
fi
[ -n "$OC_BIN" ] || OC_BIN="openclaw"
if command -v "$OC_BIN" >/dev/null 2>&1 || [ -x "$OC_BIN" ]; then
  "$OC_BIN" --version 2>&1 | head -2
  "$OC_BIN" gateway status --json 2>&1 | head -12
  "$OC_BIN" channels list --all 2>&1 | grep -i -A2 'weixin\|wechat' || info "weixin channel not listed"
  echo "--- weixin channel capabilities (acceptance probe; does NOT attempt QR login) ---"
  "$OC_BIN" channels capabilities --channel openclaw-weixin 2>&1 | head -20
else
  info "openclaw binary not found ($OC_BIN) - plugin should report degraded, IM2A still serves"
fi
if [ -f /etc/systemd/system/openclaw-gateway.service ]; then
  echo "--- openclaw-gateway.service (optional resident unit) ---"
  systemctl is-enabled openclaw-gateway 2>/dev/null || true
  systemctl is-active openclaw-gateway 2>/dev/null || true
else
  info "no resident openclaw-gateway.service installed (expected: on-demand mode on this host)"
fi

head2 "10. bash /opt/im2a/scripts/attribution-check.sh --runtime"
if [ -f /opt/im2a/scripts/attribution-check.sh ]; then
  bash /opt/im2a/scripts/attribution-check.sh --runtime 2>&1 | sed 's/\x1b\[[0-9;]*m//g'
  rc=${PIPESTATUS[0]}
  [ "$rc" -eq 0 ] && ok "attribution check (files + runtime) passed" || bad "attribution check failed (rc=$rc)"
else
  bad "missing /opt/im2a/scripts/attribution-check.sh"
fi

head2 "RESULT"
if [ "$FAIL" -eq 0 ]; then
  echo "VERIFY PASS: all hard checks passed."
  exit 0
fi
echo "VERIFY FAIL: ${FAIL} hard check(s) failed (see [FAIL] lines above)."
exit 1
