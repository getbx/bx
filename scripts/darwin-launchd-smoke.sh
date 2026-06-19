#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/darwin-launchd-smoke.sh [options]
  sudo BX_LINK='bx://...' scripts/darwin-launchd-smoke.sh --execute [options]

Options:
  --execute          Run the smoke test. Without this, print the plan only.
  --bx PATH          bx binary path. Default: /tmp/bx-mac/bx, built automatically if missing.
  --link LINK        bx:// link. Default: $BX_LINK.
  --dns              Also test explicit macOS DNS takeover with bx dns on/off.
  --hold SECONDS     Seconds to keep bx up before shutdown. Default: 8.
  --log-dir DIR      Log directory. Default: ./.bx-test-logs/bx-launchd-smoke-YYYYmmdd-HHMMSS
  -h, --help         Show this help.

This script always writes a rollback script before starting bx. It does not
change system DNS unless --dns is passed.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

fix_log_permissions() {
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER:-}" != "root" && -d "${LOG_DIR:-}" ]]; then
    chown -R "$SUDO_USER":staff "$LOG_DIR" 2>/dev/null || true
  fi
}

BX="/tmp/bx-mac/bx"
BX_PROVIDED=0
LINK="${BX_LINK:-}"
EXECUTE=0
TEST_DNS=0
HOLD="8"
LOG_DIR=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --execute) EXECUTE=1; shift ;;
    --bx) BX="${2:-}"; BX_PROVIDED=1; shift 2 ;;
    --link) LINK="${2:-}"; shift 2 ;;
    --dns) TEST_DNS=1; shift ;;
    --hold) HOLD="${2:-}"; shift 2 ;;
    --log-dir) LOG_DIR="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

if [[ -z "$LOG_DIR" ]]; then
  LOG_DIR="$REPO_ROOT/.bx-test-logs/bx-launchd-smoke-$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p "$LOG_DIR"
chmod 700 "$LOG_DIR"

if [[ "$BX_PROVIDED" != "1" || ! -x "$BX" ]]; then
  mkdir -p "$(dirname "$BX")"
  go build -o "$BX" .
fi
[[ -x "$BX" ]] || die "bx binary not executable: $BX"

{
  echo "log_dir=$LOG_DIR"
  echo "bx=$BX"
  echo "execute=$EXECUTE"
  echo "dns=$TEST_DNS"
  echo "hold=$HOLD"
  sw_vers 2>/dev/null || true
  uname -a
  "$BX" --version || true
} >"$LOG_DIR/meta.txt" 2>&1

{
  echo '#!/usr/bin/env bash'
  echo 'set +e'
  echo 'date'
  if [[ "$TEST_DNS" == "1" ]]; then
    printf '%q dns off\n' "$BX"
  fi
  printf '%q down\n' "$BX"
  echo 'date'
} >"$LOG_DIR/rollback.sh"
chmod 700 "$LOG_DIR/rollback.sh"

{
  echo "# smoke plan"
  echo "$BX setup '<redacted bx link>' --force"
  echo "$BX up"
  echo "$BX status"
  echo "$BX doctor --json --skip-probe"
  echo "$BX logs -n 120"
  if [[ "$TEST_DNS" == "1" ]]; then
    echo "$BX dns status"
    echo "$BX dns on"
    echo "$BX dns status"
    echo "$BX dns off"
  fi
  echo "sleep $HOLD"
  echo "$BX down"
} | tee "$LOG_DIR/plan.txt"

if [[ "$EXECUTE" != "1" ]]; then
  echo
  echo "Dry-run complete. Logs: $LOG_DIR"
  echo "No service or network changes were made."
  echo "To execute: sudo BX_LINK='bx://...' $0 --execute"
  fix_log_permissions
  exit 0
fi

[[ "$(uname -s)" == "Darwin" ]] || die "launchd smoke test only supports macOS"
[[ "$(id -u)" == "0" ]] || die "--execute must run as root via sudo"
[[ -n "$LINK" ]] || die "--execute requires --link or BX_LINK"

cleanup() {
  "$LOG_DIR/rollback.sh" >>"$LOG_DIR/rollback.log" 2>&1 || true
  fix_log_permissions
}
trap cleanup EXIT

"$BX" setup "$LINK" --force >"$LOG_DIR/setup.log" 2>&1
"$BX" up >"$LOG_DIR/up.log" 2>&1
sleep "$HOLD"
"$BX" status >"$LOG_DIR/status.log" 2>&1 || true
"$BX" doctor --json --skip-probe >"$LOG_DIR/doctor.json" 2>&1 || true
"$BX" logs -n 120 >"$LOG_DIR/logs.txt" 2>&1 || true

if [[ "$TEST_DNS" == "1" ]]; then
  "$BX" dns status >"$LOG_DIR/dns-before.txt" 2>&1 || true
  "$BX" dns on >"$LOG_DIR/dns-on.txt" 2>&1
  "$BX" dns status >"$LOG_DIR/dns-after-on.txt" 2>&1 || true
  "$BX" dns off >"$LOG_DIR/dns-off.txt" 2>&1
fi

"$BX" down >"$LOG_DIR/down.log" 2>&1 || true
trap - EXIT
cleanup

echo "Smoke complete. Logs: $LOG_DIR"
