#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/darwin-unified-install-check.sh [options]
  sudo scripts/darwin-unified-install-check.sh --execute --yes [options]

Options:
  --execute   Actually run `bx app-install` against a freshly staged Bx.app and
              assert the unified install landed correctly. Requires root
              (sudo) AND --yes. Without --execute, only stages the package,
              verifies it, and prints what a real run would write — zero
              system paths are touched.
  --yes       Explicit confirmation required together with --execute.
  -h, --help  Show this help.

This script never runs `bx up`/`bx down`/`bx setup`, never calls
networksetup, and never modifies DNS or routes. It only ever exercises
`bx app-install --app-source <staged Bx.app>` (and, with --execute, asserts
the result). Rollback after --execute is `sudo bx uninstall`.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

EXECUTE=0
CONFIRM_YES=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --execute) EXECUTE=1; shift ;;
    --yes) CONFIRM_YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ "$(uname -s)" == "Darwin" ]] || die "this check only supports macOS"

if [[ "$EXECUTE" == "1" ]]; then
  [[ "$(id -u)" == "0" ]] || die "--execute must run as root via sudo"
  [[ "$CONFIRM_YES" == "1" ]] || die "--execute requires --yes as an explicit second confirmation"
fi

MACHINE="$(uname -m)"
case "$MACHINE" in
  arm64) ARCH="arm64" ;;
  x86_64) ARCH="amd64" ;;
  *) die "unsupported architecture: $MACHINE" ;;
esac
VERSION="dev"

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/bx-unified-install-check.XXXXXX")" || die "could not create staging directory"
cleanup() {
  rm -rf "$STAGE"
}
trap cleanup EXIT

LOG_DIR="$STAGE/logs"
mkdir -p "$LOG_DIR"

echo "Staging package: BX_ARCH=$ARCH BX_VERSION=$VERSION BX_RELEASE_DIR=$STAGE"
BX_ARCH="$ARCH" BX_VERSION="$VERSION" BX_RELEASE_DIR="$STAGE" \
  "$REPO_ROOT/scripts/package-macos-release.sh" >"$LOG_DIR/package.log" 2>&1 ||
  { cat "$LOG_DIR/package.log" >&2; die "package-macos-release.sh failed; see above"; }

echo "Verifying staged package..."
BX_ARCH="$ARCH" BX_VERSION="$VERSION" BX_RELEASE_DIR="$STAGE" \
  "$REPO_ROOT/scripts/verify-macos-release.sh" >"$LOG_DIR/verify.log" 2>&1 ||
  { cat "$LOG_DIR/verify.log" >&2; die "verify-macos-release.sh failed; see above"; }
echo "Staged package verified."

RELEASE_DIR="$STAGE/bx-macos-$ARCH"
APP="$RELEASE_DIR/Bx.app"
[[ -d "$APP" ]] || die "staged Bx.app missing at $APP"

RUNTIME_ROOT="/Library/Application Support/bx/runtime"
RUNTIME_VERSION_DIR="$RUNTIME_ROOT/$VERSION"
RUNTIME_CURRENT="$RUNTIME_ROOT/current"
BRIDGE_PATH="/usr/local/bin/bx"
APP_PATH="/Applications/Bx.app"
GUARD_PLIST="/Library/LaunchDaemons/com.getbx.bx.guard.plist"
GUARD_LABEL="com.getbx.bx.guard"

detect_console_user() {
  /usr/bin/stat -f '%Su' /dev/console 2>/dev/null || true
}

detect_console_home() {
  local user="$1"
  [[ -n "$user" ]] || return 0
  dscl . -read "/Users/$user" NFSHomeDirectory 2>/dev/null | awk '{print $2}'
}

CONSOLE_USER="$(detect_console_user)"
CONSOLE_HOME="$(detect_console_home "$CONSOLE_USER")"
if [[ -z "$CONSOLE_HOME" ]]; then
  CONSOLE_HOME="~<console-user>"
fi
AGENT_PLIST="$CONSOLE_HOME/Library/LaunchAgents/com.getbx.bx.menu.plist"

print_plan() {
  echo
  echo "== 将会写入(app-install 的真实落地路径) =="
  echo "  $APP_PATH"
  echo "  $RUNTIME_VERSION_DIR/bx"
  echo "  $RUNTIME_VERSION_DIR/release.json"
  echo "  $RUNTIME_CURRENT -> $VERSION"
  echo "  $BRIDGE_PATH (bridge, 覆盖为 bx-bridge 的字节)"
  echo "  $GUARD_PLIST (写入但不 enable/加载)"
  echo "  $AGENT_PLIST"
  echo
  echo "== 不会做(Guardian 未在运行时) =="
  echo "  不执行 bx up"
  echo "  不 bootstrap/加载 Guardian(guard LaunchDaemon 只写文件,不启动保护)"
  echo "  不修改 DNS/路由"
  echo
  echo "== 会做(Guardian 正在运行时,即覆盖安装/升级) =="
  echo "  停止保护 → 换文件 → bootout 并重新 bootstrap Guardian → 恢复升级前的保护状态"
  echo "  期间网络回到直连几秒;本脚本以 --yes 直通 app-install 的确认"
}

if [[ "$EXECUTE" != "1" ]]; then
  print_plan
  echo
  echo "Dry-run complete. Staged package: $RELEASE_DIR"
  echo "No system paths were touched."
  echo "To execute: sudo $0 --execute --yes"
  exit 0
fi

echo
echo "Capturing pre-existing Guardian load state (before app-install)..."
GUARD_LOADED_BEFORE=0
if launchctl print "system/$GUARD_LABEL" >"$LOG_DIR/guard-before.txt" 2>&1; then
  GUARD_LOADED_BEFORE=1
  echo "note: Guardian ($GUARD_LABEL) was already loaded before this run; skipping the not-loaded assertion below."
fi

echo
# --yes:本脚本自己已经要求过 --execute --yes 两道确认,且 stdout 被 tee 接管、
# 常在自动化里跑;app-install 在无法确认时会(正确地)取消,故这里显式表态。
echo "Running: $APP/Contents/Resources/bx-cli app-install --yes --app-source $APP"
if ! "$APP/Contents/Resources/bx-cli" app-install --yes --app-source "$APP" 2>&1 | tee "$LOG_DIR/app-install.log"; then
  die "bx app-install failed; see $LOG_DIR/app-install.log"
fi

FAILED=0
report() {
  local ok="$1"
  local msg="$2"
  if [[ "$ok" == "0" ]]; then
    echo "PASS: $msg"
  else
    echo "FAIL: $msg"
    FAILED=1
  fi
}

echo
echo "== 断言 =="

if "$BRIDGE_PATH" --version >"$LOG_DIR/bridge-version.txt" 2>&1; then
  report 0 "$BRIDGE_PATH --version succeeds (via bridge)"
else
  cat "$LOG_DIR/bridge-version.txt" >&2 || true
  report 1 "$BRIDGE_PATH --version succeeds (via bridge)"
fi

CURRENT_TARGET="$(readlink "$RUNTIME_CURRENT" 2>/dev/null || true)"
if [[ "$CURRENT_TARGET" == "$VERSION" ]]; then
  report 0 "readlink '$RUNTIME_CURRENT' == $VERSION (got: $CURRENT_TARGET)"
else
  report 1 "readlink '$RUNTIME_CURRENT' == $VERSION (got: $CURRENT_TARGET)"
fi

RUNTIME_STAT="$(stat -f '%Su %Sp' "$RUNTIME_VERSION_DIR" 2>/dev/null || true)"
RUNTIME_OWNER="${RUNTIME_STAT%% *}"
RUNTIME_MODE="${RUNTIME_STAT##* }"
if [[ "$RUNTIME_OWNER" == "root" && -n "$RUNTIME_MODE" && "${RUNTIME_MODE:5:1}" != "w" && "${RUNTIME_MODE:8:1}" != "w" ]]; then
  report 0 "runtime dir '$RUNTIME_VERSION_DIR' owned by root, not group/other-writable (stat: $RUNTIME_STAT)"
else
  report 1 "runtime dir '$RUNTIME_VERSION_DIR' owned by root, not group/other-writable (stat: $RUNTIME_STAT)"
fi

if [[ -f "$GUARD_PLIST" ]] && grep -q "runtime/current/bx" "$GUARD_PLIST"; then
  report 0 "guard plist '$GUARD_PLIST' ProgramArguments references runtime/current/bx"
else
  report 1 "guard plist '$GUARD_PLIST' ProgramArguments references runtime/current/bx"
fi

if [[ -f "$AGENT_PLIST" ]]; then
  report 0 "console user LaunchAgent plist exists: $AGENT_PLIST"
else
  report 1 "console user LaunchAgent plist exists: $AGENT_PLIST"
fi

if [[ "$GUARD_LOADED_BEFORE" == "1" ]]; then
  echo "SKIP: launchctl print system/$GUARD_LABEL not loaded (Guardian was already loaded before this run)"
else
  if launchctl print "system/$GUARD_LABEL" >"$LOG_DIR/guard-after.txt" 2>&1; then
    report 1 "launchctl print system/$GUARD_LABEL is NOT loaded (app-install must not start services)"
  else
    report 0 "launchctl print system/$GUARD_LABEL is NOT loaded (app-install must not start services)"
  fi
fi

echo
if [[ "$FAILED" != "0" ]]; then
  echo "One or more assertions FAILED. Rollback with: sudo bx uninstall"
  exit 1
fi

echo "All assertions PASSED."
echo "Rollback guidance: sudo bx uninstall"
