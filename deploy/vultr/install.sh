#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# install.sh - idempotent installer / upgrader / rollback for the IM2A gateway on the shared
# Vultr host (Debian 11, 959 MB RAM, 1 vCPU, co-tenant of the w2m fleet).
#
# It installs:
#   /opt/im2a/bin/im2a-gateway            Go binary (built from --build-dir, or --binary)
#   /opt/im2a/scripts/{selfcheck,verify}.sh
#   /opt/im2a/{README.md,DEPLOYMENT.md,ATTRIBUTION.md,NOTICE,LICENSE,THIRD_PARTY_LICENSES/}
#   /opt/im2a/scripts/attribution-check.sh  (MIT compliance evidence bundle)
#   /etc/im2a/env                         0600 root:root, secrets generated once, never in git
#   /etc/systemd/system/im2a-gateway.service
#   /etc/nginx/conf.d/im2a.conf           NEW file only; existing nginx configs untouched
#
# Safety properties:
#   * idempotent: re-running upgrades the binary and restarts the unit, keeps existing secrets;
#   * --dry-run prints every action without touching the host;
#   * every overwritten system/nginx file is backed up under /var/backups/im2a/install-<ts>/;
#   * a rejected `nginx -t` automatically restores the previous nginx config;
#   * --rollback undoes unit/nginx changes (keeps /opt/im2a data and /etc/im2a/env unless --purge).
#
# It NEVER touches xray, the w2m-* services, gsn-daemon, agent-universe-relay, the existing
# nginx server blocks (w2m/alone/subscribe) or the firewall.
set -euo pipefail

# ---------------------------------------------------------------------------------------------
# Defaults / arguments
# ---------------------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." 2>/dev/null && pwd || echo "$SCRIPT_DIR")"

PREFIX=/opt/im2a
BIN="$PREFIX/bin/im2a-gateway"
ENV_DIR=/etc/im2a
ENV_FILE="$ENV_DIR/env"
UNIT_PATH=/etc/systemd/system/im2a-gateway.service
OC_UNIT_PATH=/etc/systemd/system/openclaw-gateway.service
NGINX_CONF=/etc/nginx/conf.d/im2a.conf
BACKUP_ROOT=/var/backups/im2a

PORT=18080
PUBLIC_HOST="im2a.202.182.123.154.nip.io"
MIN_AVAIL_MB=80
BUILD_DIR=/root/im2a-work/build
SOURCE_ROOT=""
BINARY=""
ACTION=install
DRY_RUN=0
SKIP_NGINX=0
SKIP_ATTRIBUTION=0
ROTATE_SECRETS=0
WITH_OPENCLAW_UNIT=0
PURGE=0

CHANGED_NGINX=0
CHANGED_UNIT=0
BEFORE_NGINX=0
BEFORE_UNIT=0
TS="$(date +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="$BACKUP_ROOT/install-$TS"

usage() {
  cat <<'EOF'
Usage: bash install.sh [options]

Install/upgrade:
  --dry-run              Print the actions; change nothing (usable without root).
  --build-dir DIR        Go source tree to build ./services/gateway from.
                         Default: /root/im2a-work/build
  --binary FILE          Use a prebuilt binary instead of running `go build`.
  --source-root DIR      Repo/source tree that holds README.md, ATTRIBUTION.md, NOTICE,
                         LICENSE, THIRD_PARTY_LICENSES/, scripts/attribution-check.sh.
                         Default: two levels above this script, then --build-dir.
  --port PORT            Listen port (default 18080).
  --public-host HOST     Hostname used only in the final summary (nginx file is static).
  --skip-nginx           Do not install/reload the nginx vhost.
  --skip-attribution     Do not install the MIT attribution bundle (NOT compliant; test only).
  --rotate-secrets       Re-generate IM2A_ADMIN_TOKEN/GATEWAY_TOKEN/IM2A_JWT_SECRET.
  --with-openclaw-unit   Copy systemd/openclaw-gateway.service to /etc/systemd/system/
                         WITHOUT enabling it (measured: OOM on this host; see README).

Rollback:
  --rollback             Stop/disable im2a-gateway, restore the newest backup of the unit and
                         /etc/nginx/conf.d/im2a.conf, reload nginx. Keeps data and secrets.
  --purge                With --rollback: also delete /opt/im2a and /etc/im2a.

Examples:
  bash install.sh --dry-run
  bash install.sh --build-dir /root/im2a-work/build
  bash install.sh --binary /root/im2a-work/im2a-gateway
  bash install.sh --rollback
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)             DRY_RUN=1 ;;
    --build-dir)           BUILD_DIR="${2:?}"; shift ;;
    --binary)              BINARY="${2:?}"; shift ;;
    --source-root)         SOURCE_ROOT="${2:?}"; shift ;;
    --port)                PORT="${2:?}"; shift ;;
    --public-host)         PUBLIC_HOST="${2:?}"; shift ;;
    --skip-nginx)          SKIP_NGINX=1 ;;
    --skip-attribution)    SKIP_ATTRIBUTION=1 ;;
    --rotate-secrets)      ROTATE_SECRETS=1 ;;
    --with-openclaw-unit)  WITH_OPENCLAW_UNIT=1 ;;
    --rollback)            ACTION=rollback ;;
    --purge)               PURGE=1 ;;
    -h|--help)             usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [ -n "$SOURCE_ROOT" ]; then REPO_ROOT="$SOURCE_ROOT"; fi

# ---------------------------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------------------------
log()  { printf '[install] %s\n' "$*"; }
warn() { printf '[install] WARN: %s\n' "$*" >&2; }
die()  { printf '[install] ERROR: %s\n' "$*" >&2; exit 1; }

# run a mutating command (or print it under --dry-run)
run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '[dry-run] %s\n' "$*"
    return 0
  fi
  "$@"
}

gen_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

avail_mb() {
  free -m 2>/dev/null | awk '/^Mem|^内存/ {print $7}' | head -1
}

backup_path() {
  local p="$1"
  if [ -e "$p" ]; then
    if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] backup $p -> $BACKUP_DIR/"; return 0; fi
    mkdir -p "$BACKUP_DIR"
    cp -a "$p" "$BACKUP_DIR/$(basename "$p")"
    printf 'backup\t%s\n' "$p" >> "$BACKUP_DIR/manifest.tsv"
    log "backup: $p -> $BACKUP_DIR/"
  else
    if [ "$DRY_RUN" -eq 0 ]; then
      mkdir -p "$BACKUP_DIR"
      printf 'absent\t%s\n' "$p" >> "$BACKUP_DIR/manifest.tsv"
    fi
  fi
}

install_file() { # src dst mode
  local src="$1" dst="$2" mode="${3:-0644}"
  [ -f "$src" ] || die "missing source file: $src"
  if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] install -m $mode $src -> $dst"; return 0; fi
  install -m "$mode" "$src" "$dst"
}

pick_file() { # relative path -> absolute source path or empty
  local rel="$1"
  if [ -n "$REPO_ROOT" ] && [ -f "$REPO_ROOT/$rel" ]; then echo "$REPO_ROOT/$rel"; return 0; fi
  if [ -f "$BUILD_DIR/$rel" ]; then echo "$BUILD_DIR/$rel"; return 0; fi
  echo ""
}
pick_dir() {
  local rel="$1"
  if [ -n "$REPO_ROOT" ] && [ -d "$REPO_ROOT/$rel" ]; then echo "$REPO_ROOT/$rel"; return 0; fi
  if [ -d "$BUILD_DIR/$rel" ]; then echo "$BUILD_DIR/$rel"; return 0; fi
  echo ""
}

restore_nginx() {
  if [ "$BEFORE_NGINX" -eq 1 ] && [ -f "$BACKUP_DIR/im2a.conf" ]; then
    install -m 0644 "$BACKUP_DIR/im2a.conf" "$NGINX_CONF"
    log "nginx: restored previous $NGINX_CONF"
  else
    rm -f "$NGINX_CONF"
    log "nginx: removed $NGINX_CONF (did not exist before)"
  fi
  if command -v nginx >/dev/null 2>&1 && nginx -t >/dev/null 2>&1; then systemctl reload nginx 2>/dev/null || true; fi
  CHANGED_NGINX=0
}

latest_backup() {
  ls -1d "$BACKUP_ROOT"/install-* 2>/dev/null | sort | tail -1 || true
}

on_error() {
  local rc=$?
  trap - ERR
  if [ "$DRY_RUN" -eq 0 ]; then
    warn "install failed (rc=$rc)"
    if [ "$CHANGED_NGINX" -eq 1 ]; then
      restore_nginx || true
    fi
    warn "unit left as-is for inspection; roll back with: bash $0 --rollback"
  fi
  exit "$rc"
}
trap on_error ERR

# ---------------------------------------------------------------------------------------------
# Rollback
# ---------------------------------------------------------------------------------------------
do_rollback() {
  [ "$(id -u)" -eq 0 ] || die "--rollback requires root"
  log "rollback: stopping + disabling im2a-gateway"
  systemctl disable --now im2a-gateway 2>/dev/null || true

  local b
  b="$(latest_backup)"
  if [ -n "$b" ]; then
    log "rollback: using backup $b"
    if [ -f "$b/im2a-gateway.service" ]; then
      install -m 0644 "$b/im2a-gateway.service" "$UNIT_PATH"
      log "rollback: restored previous $UNIT_PATH"
    else
      rm -f "$UNIT_PATH"
      log "rollback: removed $UNIT_PATH (did not exist before)"
    fi
    if [ -f "$b/im2a.conf" ]; then
      install -m 0644 "$b/im2a.conf" "$NGINX_CONF"
      log "rollback: restored previous $NGINX_CONF"
    else
      rm -f "$NGINX_CONF"
      log "rollback: removed $NGINX_CONF (did not exist before)"
    fi
    if [ -f "$b/installed.tsv" ] && grep -q "openclaw-gateway.service" "$b/installed.tsv"; then
      systemctl disable --now openclaw-gateway 2>/dev/null || true
      rm -f "$OC_UNIT_PATH"
      log "rollback: removed optional $OC_UNIT_PATH"
    fi
  else
    warn "no backup directory found under $BACKUP_ROOT; removing installed unit/vhost anyway"
    rm -f "$UNIT_PATH" "$NGINX_CONF"
  fi

  systemctl daemon-reload || true
  if ! command -v nginx >/dev/null 2>&1; then
    log "rollback: nginx not installed on this host - skipped vhost reload"
  elif nginx -t >/dev/null 2>&1; then
    systemctl reload nginx 2>/dev/null || true
    log "rollback: nginx config valid, reloaded"
  else
    warn "rollback: nginx -t currently FAILS - inspect manually before reloading nginx"
  fi

  if [ "$PURGE" -eq 1 ]; then
    log "rollback: purging $PREFIX and $ENV_DIR (data + secrets)"
    rm -rf "$PREFIX" "$ENV_DIR"
  else
    log "rollback: kept $PREFIX (binary/data/logs) and $ENV_FILE (secrets)"
  fi
  log "rollback done. Nothing else on the host was touched."
  exit 0
}

if [ "$ACTION" = "rollback" ]; then
  do_rollback
fi

# ---------------------------------------------------------------------------------------------
# Preflight
# ---------------------------------------------------------------------------------------------
preflight() {
  if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then die "run as root (or use --dry-run)"; fi
  if [ "$DRY_RUN" -eq 0 ]; then
    for c in systemctl install curl ss free df; do
      command -v "$c" >/dev/null 2>&1 || die "missing required command: $c"
    done
    # nginx is only needed when the vhost is part of the install (e.g. a host without nginx
    # such as the clean Ubuntu bench host must pass --skip-nginx).
    if [ "$SKIP_NGINX" -eq 0 ]; then
      command -v nginx >/dev/null 2>&1 || die "missing required command: nginx (or pass --skip-nginx)"
    fi
  fi

  if [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || die "--binary file not found: $BINARY"
  else
    [ -f "$BUILD_DIR/services/gateway/main.go" ] || die \
      "no Go source at $BUILD_DIR/services/gateway (pass --build-dir or --binary)"
    command -v go >/dev/null 2>&1 || [ -x /usr/local/go/bin/go ] || die "go toolchain not found"
  fi

  # Never steal a port from an existing service.
  if command -v ss >/dev/null 2>&1 && ss -lnt 2>/dev/null | grep -qE ":${PORT}[[:space:]]"; then
    if systemctl is-active --quiet im2a-gateway 2>/dev/null; then
      log "port $PORT already held by im2a-gateway (upgrade path)"
    else
      die "port $PORT is already in use by another process; refusing to disturb existing services"
    fi
  fi

  local avail
  avail="$(avail_mb || echo 0)"
  case "$avail" in
    ''|*[!0-9]*) warn "could not read available memory" ;;
    *) [ "$avail" -ge 120 ] || warn "available memory only ${avail}MB (red line after deploy is ${MIN_AVAIL_MB}MB)" ;;
  esac

  local disk
  disk="$(df -Pm /opt 2>/dev/null | awk 'NR==2{print $4}')"
  case "$disk" in
    ''|*[!0-9]*) warn "could not read free disk for /opt" ;;
    *) [ "$disk" -ge 500 ] || die "less than 500MB free on /opt (${disk}MB)" ;;
  esac

  if [ "$SKIP_NGINX" -eq 0 ]; then
    nginx -t >/dev/null 2>&1 || die "nginx -t fails BEFORE install; fix nginx first (nothing changed)"
  fi
}

# ---------------------------------------------------------------------------------------------
# Steps
# ---------------------------------------------------------------------------------------------
step_dirs() {
  log "dirs: $PREFIX/{bin,data,logs,scripts}"
  run mkdir -p "$PREFIX/bin" "$PREFIX/data" "$PREFIX/logs" "$PREFIX/scripts"
  run chmod 0755 "$PREFIX" "$PREFIX/bin" "$PREFIX/data" "$PREFIX/logs" "$PREFIX/scripts"
}

ensure_env_key() { # key  is_secret  default_value
  local key="$1" secret="$2" def="${3:-}"
  if grep -qE "^${key}=" "$ENV_FILE" 2>/dev/null; then
    if [ "$ROTATE_SECRETS" -eq 1 ] && [ "$secret" -eq 1 ]; then
      local v; v="$(gen_secret)"
      if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] rotate $key in $ENV_FILE"; return 0; fi
      sed -i "s|^${key}=.*|${key}=${v}|" "$ENV_FILE"
      log "env: rotated $key"
    else
      log "env: $key present (kept)"
    fi
    return 0
  fi
  local v="$def"
  [ "$secret" -eq 1 ] && v="$(gen_secret)"
  if [ "$DRY_RUN" -eq 1 ]; then printf '[dry-run] append %s=<generated-or-default> to %s\n' "$key" "$ENV_FILE"; return 0; fi
  printf '%s=%s\n' "$key" "$v" >> "$ENV_FILE"
  log "env: appended $key"
}

step_env() {
  log "env: $ENV_FILE (0600 root:root; secrets generated once, never in git)"
  if [ ! -f "$ENV_FILE" ]; then
    run mkdir -p "$ENV_DIR"
    if [ "$DRY_RUN" -eq 0 ]; then
      umask 077
      {
        echo "# IM2A runtime environment - generated by deploy/vultr/install.sh on $TS"
        echo "# Mode 0600 root:root. NEVER commit this file. See deploy/vultr/env/im2a.env.example"
      } > "$ENV_FILE"
    fi
  fi
  ensure_env_key IM2A_ADMIN_TOKEN 1
  ensure_env_key GATEWAY_TOKEN 1
  ensure_env_key IM2A_JWT_SECRET 1
  ensure_env_key OPENCLAW_ENABLED 0 1
  ensure_env_key OPENCLAW_BIN 0 /usr/local/node24/bin/openclaw
  ensure_env_key OPENCLAW_HOME 0 /root
  ensure_env_key OPENCLAW_CHANNEL 0 openclaw-weixin
  ensure_env_key OPENCLAW_ACCOUNTS_DIR 0 /root/.openclaw/openclaw-weixin/accounts
  # external = CLI subprocesses only (no resident OpenClaw gateway). "managed" would let the
  # plugin try to start `openclaw gateway`, which measured OOM on this host - see README §6.
  ensure_env_key OPENCLAW_GATEWAY_MODE 0 external
  if [ "$DRY_RUN" -eq 0 ]; then
    chown root:root "$ENV_FILE"
    chmod 0600 "$ENV_FILE"
  fi
  log "env: permissions 0600 root:root"
}

step_binary() {
  log "binary: building/installing $BIN"
  if [ -n "$BINARY" ]; then
    if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] install -m 0755 $BINARY $BIN"; return 0; fi
    [ -f "$BIN" ] && cp -a "$BIN" "$PREFIX/bin/im2a-gateway.prev"
    install -m 0755 "$BINARY" "$BIN"
  else
    local gobin=/usr/local/go/bin/go
    command -v go >/dev/null 2>&1 && gobin="$(command -v go)"
    local commit=""
    if [ -d "$BUILD_DIR/.git" ]; then
      commit="$(cd "$BUILD_DIR" && git rev-parse --short HEAD 2>/dev/null || true)"
    fi
    [ -n "$commit" ] || commit="build-$TS"
    if [ "$DRY_RUN" -eq 1 ]; then
      log "[dry-run] (cd $BUILD_DIR && $gobin build -trimpath -ldflags '-s -w -X human-agent-im/internal/version.GitCommit=$commit' -o $BIN.new ./services/gateway)"
      return 0
    fi
    # The build tree /root/im2a-work/build is shared with the project's sync-build loop
    # (sync-build.ps1 takes the same flock). Waiting here avoids compiling while an upload is
    # being unpacked and keeps the two workflows from interleaving.
    if command -v flock >/dev/null 2>&1; then
      exec 9>/tmp/im2a-build.lock
      flock -w 180 9 || die "timed out waiting for /tmp/im2a-build.lock (another build is running)"
    fi
    ( cd "$BUILD_DIR" \
      && HOME=/root GOCACHE=/root/.cache/go-build GOPATH=/root/go GOFLAGS=-mod=mod \
         "$gobin" build -buildvcs=false -trimpath \
             -ldflags "-s -w -X human-agent-im/internal/version.GitCommit=$commit" \
             -o "$BIN.new" ./services/gateway )
    [ -x "$BIN.new" ] || die "go build produced no binary"
    if [ -f "$BIN" ]; then cp -a "$BIN" "$PREFIX/bin/im2a-gateway.prev"; fi
    mv -f "$BIN.new" "$BIN"
    {
      echo "built_at=$TS"
      echo "source=$BUILD_DIR"
      echo "git_commit=$commit"
      echo "go=$("$gobin" version 2>/dev/null | head -1)"
      echo "sha256=$(sha256sum "$BIN" | awk '{print $1}')"
    } > "$PREFIX/data/build-info.txt"
    log "binary: $(stat -c '%s bytes' "$BIN"), $(grep -c . "$PREFIX/data/build-info.txt") build facts recorded"
  fi
  # Self-test before the unit is allowed to start it.
  if [ "$DRY_RUN" -eq 0 ]; then
    if "$BIN" -selftest >/tmp/im2a-selftest.log 2>&1; then
      log "selftest: PASS ($(grep -c '\[PASS\]' /tmp/im2a-selftest.log) checks)"
    else
      sed -n '1,20p' /tmp/im2a-selftest.log >&2
      die "binary self-test failed; not installing the unit"
    fi
  fi
}

render_unit() { # src dst: replace the default -port 18080 if --port was overridden
  local src="$1" dst="$2"
  if [ "$PORT" = "18080" ]; then
    install_file "$src" "$dst" 0644
  else
    if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] install $src -> $dst (port=$PORT)"; return 0; fi
    sed "s|-port 18080|-port ${PORT}|g" "$src" > "$dst"
    chmod 0644 "$dst"
  fi
}

step_units() {
  log "systemd: installing $UNIT_PATH"
  BEFORE_UNIT=0
  [ -e "$UNIT_PATH" ] && BEFORE_UNIT=1
  backup_path "$UNIT_PATH"
  run mkdir -p "$PREFIX/scripts"
  install_file "$SCRIPT_DIR/scripts/selfcheck.sh" "$PREFIX/scripts/selfcheck.sh" 0755
  install_file "$SCRIPT_DIR/scripts/verify.sh" "$PREFIX/scripts/verify.sh" 0755
  render_unit "$SCRIPT_DIR/systemd/im2a-gateway.service" "$UNIT_PATH"
  CHANGED_UNIT=1
  if [ "$WITH_OPENCLAW_UNIT" -eq 1 ]; then
    warn "copying OPTIONAL openclaw-gateway.service (not enabled): measured OOM at 320M/384M on this host"
    backup_path "$OC_UNIT_PATH"
    install_file "$SCRIPT_DIR/systemd/openclaw-gateway.service" "$OC_UNIT_PATH" 0644
    if [ "$DRY_RUN" -eq 0 ]; then
      mkdir -p "$BACKUP_DIR"
      printf 'installed\t%s\n' "$OC_UNIT_PATH" >> "$BACKUP_DIR/installed.tsv"
    fi
  fi
  run systemctl daemon-reload
  run systemctl enable im2a-gateway.service
  log "systemd: (re)starting im2a-gateway"
  run systemctl restart im2a-gateway.service
}

wait_healthy() {
  [ "$DRY_RUN" -eq 1 ] && { log "[dry-run] wait for authenticated /healthz == 200"; return 0; }
  local token code i
  token="$(sed -n 's/^IM2A_ADMIN_TOKEN=//p' "$ENV_FILE" | tail -1)"
  for i in $(seq 1 60); do
    code="$(curl -sS -o /dev/null -w '%{http_code}' -m 3 \
      -H "Authorization: Bearer ${token}" "http://127.0.0.1:${PORT}/healthz" 2>/dev/null || true)"
    if [ "$code" = "200" ]; then
      log "health: authenticated /healthz=200 after ${i} attempt(s)"
      return 0
    fi
    sleep 2
  done
  systemctl status im2a-gateway --no-pager 2>&1 | head -20 >&2 || true
  journalctl -u im2a-gateway -n 30 --no-pager 2>&1 | tail -30 >&2 || true
  die "im2a-gateway did not become healthy on port ${PORT}"
}

step_nginx() {
  if [ "$SKIP_NGINX" -eq 1 ]; then log "nginx: skipped (--skip-nginx)"; return 0; fi
  log "nginx: installing NEW vhost $NGINX_CONF (existing configs untouched)"
  BEFORE_NGINX=0
  [ -e "$NGINX_CONF" ] && BEFORE_NGINX=1
  backup_path "$NGINX_CONF"
  if [ "$PORT" = "18080" ]; then
    install_file "$SCRIPT_DIR/nginx/im2a.conf" "$NGINX_CONF" 0644
  else
    if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] install nginx/im2a.conf -> $NGINX_CONF (backend :$PORT)"; else
      sed "s|127.0.0.1:18080|127.0.0.1:${PORT}|g" "$SCRIPT_DIR/nginx/im2a.conf" > "$NGINX_CONF"
      chmod 0644 "$NGINX_CONF"
    fi
  fi
  CHANGED_NGINX=1
  if [ "$DRY_RUN" -eq 1 ]; then log "[dry-run] nginx -t && systemctl reload nginx"; return 0; fi
  if ! nginx -t >/tmp/im2a-nginx-t.log 2>&1; then
    sed -n '1,20p' /tmp/im2a-nginx-t.log >&2
    restore_nginx
    die "nginx rejected the new vhost; previous config restored, nothing else changed"
  fi
  log "nginx: nginx -t OK"
  systemctl reload nginx
  log "nginx: reloaded (running workers keep existing configs intact)"
  CHANGED_NGINX=0

  # Verify through the public vhost locally (Host header, no DNS dependency). nginx reload is
  # asynchronous: old workers may still answer the first request with the previous routing
  # (e.g. the w2m default_server -> 401), so retry briefly instead of failing on the race.
  local root_code="" i
  for i in $(seq 1 10); do
    root_code="$(curl -sS -o /dev/null -w '%{http_code}' -m 5 -H "Host: ${PUBLIC_HOST}" "http://127.0.0.1/" 2>/dev/null || true)"
    [ "$root_code" = "200" ] && break
    sleep 1
  done
  if [ "$root_code" = "200" ]; then
    log "nginx: vhost check via Host header -> 200"
  else
    warn "nginx: vhost check via Host header returned ${root_code:-no-response} (service or vhost issue)"
  fi
}

step_attribution() {
  if [ "$SKIP_ATTRIBUTION" -eq 1 ]; then warn "attribution bundle skipped (--skip-attribution)"; return 0; fi
  log "attribution: installing MIT/third-party evidence bundle into $PREFIX"
  local f src
  for f in ATTRIBUTION.md NOTICE LICENSE README.md; do
    src="$(pick_file "$f")"
    if [ -n "$src" ]; then install_file "$src" "$PREFIX/$f" 0644
    else warn "attribution: source file not found: $f"; fi
  done
  # License texts: copy EVERY text in THIRD_PARTY_LICENSES/ (OpenClaw-MIT.txt and, when the
  # source tree has it, OpenClaw-Weixin-MIT.txt for the externally installed weixin channel
  # plugin). MIT requires the notice to travel with the distributed artifact.
  local tpdir
  tpdir="$(pick_dir THIRD_PARTY_LICENSES)"
  if [ -n "$tpdir" ]; then
    run mkdir -p "$PREFIX/THIRD_PARTY_LICENSES"
    local lic
    for lic in "$tpdir"/*.txt; do
      [ -f "$lic" ] || continue
      install_file "$lic" "$PREFIX/THIRD_PARTY_LICENSES/$(basename "$lic")" 0644
    done
  else
    warn "attribution: THIRD_PARTY_LICENSES/ not found in source tree"
  fi
  src="$(pick_file scripts/attribution-check.sh)"
  if [ -n "$src" ]; then install_file "$src" "$PREFIX/scripts/attribution-check.sh" 0755
  else warn "attribution: scripts/attribution-check.sh not found"; fi
  # NOTE: intentionally NO source-tree mirror under /opt/im2a. The updated
  # scripts/attribution-check.sh detects a deployment tree (no $ROOT/internal) and skips the
  # source-level checks with an INFO line; /opt/im2a stays "deployed artifacts only".
  # Deployment doc for operators (unit Documentation= points here).
  if [ -f "$SCRIPT_DIR/README.md" ]; then
    install_file "$SCRIPT_DIR/README.md" "$PREFIX/DEPLOYMENT.md" 0644
  fi
  if [ "$DRY_RUN" -eq 0 ] && [ -f "$PREFIX/scripts/attribution-check.sh" ]; then
    if bash "$PREFIX/scripts/attribution-check.sh" >/tmp/im2a-attr-check.log 2>&1; then
      log "attribution: file checks PASS"
    else
      warn "attribution: file checks FAILED (see /tmp/im2a-attr-check.log); runtime check still runs in verify.sh"
    fi
  fi
}

step_postcheck() {
  [ "$DRY_RUN" -eq 1 ] && { log "[dry-run] post-deploy memory check"; return 0; }
  local avail
  avail="$(avail_mb || echo 0)"
  if [ "${avail:-0}" -lt "$MIN_AVAIL_MB" ]; then
    warn "RED LINE BREACHED: available=${avail}MB < ${MIN_AVAIL_MB}MB."
    warn "  -> lower MemoryMax in $UNIT_PATH (currently 256M) or disable resident OpenClaw,"
    warn "  -> then: systemctl daemon-reload && systemctl restart im2a-gateway"
  else
    log "memory: available=${avail}MB >= ${MIN_AVAIL_MB}MB red line"
  fi
}

print_summary() {
  local token_len public_line
  token_len="$(sed -n 's/^IM2A_ADMIN_TOKEN=//p' "$ENV_FILE" 2>/dev/null | tail -1 | wc -c)"
  if [ "$SKIP_NGINX" -eq 1 ]; then
    public_line="public URL   : (nginx skipped; backend is loopback-only: http://127.0.0.1:${PORT})"
  else
    public_line="public URL   : http://${PUBLIC_HOST}/     (nginx -> 127.0.0.1:${PORT})"
  fi
  cat <<EOF

================================================================================
IM2A gateway deployed.
  unit         : systemctl status im2a-gateway --no-pager
  backend      : http://127.0.0.1:${PORT}   (firewall/ufw should block this from the internet)
  ${public_line}
  env file     : ${ENV_FILE} (0600 root:root; admin token ${token_len} chars incl. newline)
  build facts  : ${PREFIX}/data/build-info.txt
  evidence     : bash ${PREFIX}/scripts/verify.sh
  rollback     : bash $0 --rollback          (keep data)
                 bash $0 --rollback --purge  (also delete /opt/im2a + /etc/im2a)
  backup dir   : ${BACKUP_DIR}
  attribution  : OpenClaw MIT, Copyright (c) 2026 OpenClaw Foundation
                 /v1/about/attribution | /opt/im2a/ATTRIBUTION.md
================================================================================
EOF
}

# ---------------------------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------------------------
log "IM2A installer (dry-run=$DRY_RUN, port=$PORT, build-dir=$BUILD_DIR, source-root=$REPO_ROOT)"
preflight

if [ "$DRY_RUN" -eq 0 ]; then
  run mkdir -p "$BACKUP_DIR"
  printf 'started\t%s\n' "$TS" > "$BACKUP_DIR/manifest.tsv"
  log "backups: $BACKUP_DIR"
fi

step_dirs
step_env
step_binary
step_units
wait_healthy
step_nginx
step_attribution
step_postcheck
print_summary
log "done."
