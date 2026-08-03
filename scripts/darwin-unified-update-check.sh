#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/darwin-unified-update-check.sh [options]
  sudo scripts/darwin-unified-update-check.sh --execute --yes [options]

Options:
  --execute   Actually run `bx update --package-file ... --json` against a
              running unified-layout install and assert the update landed
              (positive path) and that a corrupt package auto-rolls-back
              (negative path). Requires root (sudo), --yes, an already
              unified-layout install (see darwin-unified-install-check.sh),
              and `bx status --json` currently reporting protection_state
              "protected". Without --execute, only stages a "dev2" package,
              verifies it, builds (in a temp dir) the corrupt "dev3" rollback
              package, and prints what a real run would do — zero system
              paths are touched.
  --yes       Explicit confirmation required together with --execute.
  -h, --help  Show this help.

This script never runs `bx up`/`bx down`/`bx setup`, never calls
launchctl/networksetup, and never modifies DNS or routes. It only ever
invokes `bx update`, `bx status`, and `bx --version` (all via the installed
bridge at /usr/local/bin/bx). Positive update target: BX_VERSION=dev2.
Rollback rehearsal target: BX_VERSION=dev3, a copy of the dev2 package with
Contents/Resources/bx-cli replaced by garbage bytes (release.json's bx-cli
digest recomputed to match, so the package passes digest verification but
the new Core binary cannot start — Guardian's health check fails and it
auto-rolls back to dev2).
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

VERSION_FROM="dev"
VERSION2="dev2"
VERSION3="dev3"

STAGE="$(mktemp -d "${TMPDIR:-/tmp}/bx-unified-update-check.XXXXXX")" || die "could not create staging directory"
cleanup() {
  rm -rf "$STAGE"
}
trap cleanup EXIT

LOG_DIR="$STAGE/logs"
mkdir -p "$LOG_DIR"

RUNTIME_ROOT="/Library/Application Support/bx/runtime"
RUNTIME_CURRENT="$RUNTIME_ROOT/current"
BRIDGE_PATH="/usr/local/bin/bx"
RECEIPT_PATH="/var/lib/bx/update/receipt.json"
TRANSACTION_PATH="/var/lib/bx/update/transaction.json"

# ---------------------------------------------------------------------------
# Stage 1: build + verify the "dev2" full macOS release package (positive
# update target).
# ---------------------------------------------------------------------------
echo "Staging dev2 package: BX_ARCH=$ARCH BX_VERSION=$VERSION2 BX_RELEASE_DIR=$STAGE"
BX_ARCH="$ARCH" BX_VERSION="$VERSION2" BX_RELEASE_DIR="$STAGE" \
  "$REPO_ROOT/scripts/package-macos-release.sh" >"$LOG_DIR/package-dev2.log" 2>&1 ||
  { cat "$LOG_DIR/package-dev2.log" >&2; die "package-macos-release.sh (dev2) failed; see above"; }

echo "Verifying staged dev2 package..."
BX_ARCH="$ARCH" BX_VERSION="$VERSION2" BX_RELEASE_DIR="$STAGE" \
  "$REPO_ROOT/scripts/verify-macos-release.sh" >"$LOG_DIR/verify-dev2.log" 2>&1 ||
  { cat "$LOG_DIR/verify-dev2.log" >&2; die "verify-macos-release.sh (dev2) failed; see above"; }
echo "Staged dev2 package verified."

DEV2_RELEASE_DIR="$STAGE/bx-macos-$ARCH"
DEV2_TARBALL="$STAGE/bx-macos-$ARCH.tar.gz"
[[ -f "$DEV2_TARBALL" ]] || die "dev2 tarball missing at $DEV2_TARBALL"

# ---------------------------------------------------------------------------
# Stage 2: build the corrupt "dev3" rollback-rehearsal package from a copy of
# the dev2 tree. Contents/Resources/bx-cli is replaced with garbage bytes;
# release.json's bx-cli digest is recomputed so the package still passes
# asset-digest verification (the corruption is "doesn't run", not "digest
# mismatch"), and release.json's version is bumped to dev3 so
# pkg.Release.Version (which the CLI uses as ToVersion) matches.
# ---------------------------------------------------------------------------
echo "Building corrupt dev3 rollback-rehearsal package..."
DEV3_ROOT="$STAGE/dev3-build"
mkdir -p "$DEV3_ROOT"
ditto "$DEV2_RELEASE_DIR" "$DEV3_ROOT/bx-macos-$ARCH"

DEV3_RESOURCES="$DEV3_ROOT/bx-macos-$ARCH/Bx.app/Contents/Resources"
[[ -f "$DEV3_RESOURCES/release.json" ]] || die "dev3 build missing release.json at $DEV3_RESOURCES"

head -c 1024 /dev/urandom >"$DEV3_RESOURCES/bx-cli"
chmod 0755 "$DEV3_RESOURCES/bx-cli"
CORRUPT_CLI_SHA="$(shasum -a 256 "$DEV3_RESOURCES/bx-cli" | awk '{print $1}')"

python3 - "$DEV3_RESOURCES/release.json" "$VERSION3" "$CORRUPT_CLI_SHA" <<'PYEOF'
import json
import sys

path, version, cli_sha = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path) as f:
    data = json.load(f)
data["version"] = version
data["assets"]["bx-cli"] = cli_sha
with open(path, "w") as f:
    json.dump(data, f)
PYEOF

DEV3_TARBALL="$STAGE/bx-macos-$ARCH-dev3.tar.gz"
(
  cd "$DEV3_ROOT"
  tar -czf "$DEV3_TARBALL" "bx-macos-$ARCH"
)

# Sanity-check the corrupt package purely by inspection (no execution, no
# system paths touched): tar is well-formed, release.json is valid JSON with
# version==dev3, and the recomputed digest matches the on-disk garbage bytes.
tar -tzf "$DEV3_TARBALL" >/dev/null || die "dev3 tarball is not a valid tar.gz"
python3 -c '
import json, sys
data = json.load(open(sys.argv[1]))
assert data["version"] == sys.argv[2], "dev3 release.json version mismatch"
assert data["assets"]["bx-cli"] == sys.argv[3], "dev3 release.json bx-cli digest mismatch"
' "$DEV3_RESOURCES/release.json" "$VERSION3" "$CORRUPT_CLI_SHA" ||
  die "dev3 corrupt package sanity check failed"
echo "Corrupt dev3 package built and sanity-checked: $DEV3_TARBALL"

print_plan() {
  echo
  echo "== 演练步骤(--execute 会做的事,按顺序) =="
  echo "  1. 前置检查:统一布局已生效(readlink '$RUNTIME_CURRENT' 有效)"
  echo "     且 bx status --json 的 protection_state == protected"
  echo "  2. 正向更新演练:"
  echo "       sudo $BRIDGE_PATH update --package-file $DEV2_TARBALL --json"
  echo "     断言:exit 0 / JSON 含 \"phase\":\"committed\" /"
  echo "       readlink '$RUNTIME_CURRENT' == $VERSION2 /"
  echo "       $BRIDGE_PATH --version 含 $VERSION2 /"
  echo "       bx status --json 回到 protected /"
  echo "       $RECEIPT_PATH outcome==committed / $TRANSACTION_PATH 不存在"
  echo "  3. 回滚演练(坏包 dev3,digest 合法但 bx-cli 是垃圾字节,起不来):"
  echo "       sudo $BRIDGE_PATH update --package-file $DEV3_TARBALL --json"
  echo "     断言:JSON 含 \"rolled_back\":true /"
  echo "       readlink '$RUNTIME_CURRENT' 仍为 $VERSION2 /"
  echo "       bx status --json 回到 protected /"
  echo "       $RECEIPT_PATH outcome==rolled_back"
  echo "  4. 全程只调用 bx update / bx status / bx --version"
  echo
  echo "== 不会做 =="
  echo "  不执行 bx up / bx down / bx setup"
  echo "  不调用 launchctl / networksetup"
  echo "  不修改 DNS/路由"
  echo "  不会把 dev2/dev3 以外的任何东西写进 /Applications 或 /Library"
}

if [[ "$EXECUTE" != "1" ]]; then
  print_plan
  echo
  echo "Dry-run complete. Staged dev2 package: $DEV2_RELEASE_DIR"
  echo "Staged dev3 (corrupt) package: $DEV3_TARBALL"
  echo "No system paths were touched."
  echo "To execute: sudo $0 --execute --yes"
  exit 0
fi

# ---------------------------------------------------------------------------
# --execute below this point.
# ---------------------------------------------------------------------------

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

json_field() {
  # json_field <json-text-or-file> <field> [--file]
  python3 -c '
import json, sys
raw = sys.argv[1]
field = sys.argv[2]
try:
    data = json.loads(raw)
except json.JSONDecodeError:
    data = json.load(open(raw))
v = data.get(field, "")
print(v if isinstance(v, str) else json.dumps(v))
' "$1" "$2" 2>/dev/null || true
}

echo
echo "Preflight: checking unified layout + current protection state..."

CURRENT_BEFORE="$(readlink "$RUNTIME_CURRENT" 2>/dev/null || true)"
[[ -n "$CURRENT_BEFORE" ]] || die "unified layout not active: readlink '$RUNTIME_CURRENT' failed (run darwin-unified-install-check.sh --execute first)"
echo "Current runtime version before rehearsal: $CURRENT_BEFORE"

STATUS_BEFORE_JSON="$("$BRIDGE_PATH" status --json 2>"$LOG_DIR/status-before.err" || true)"
PROTECTION_BEFORE="$(json_field "$STATUS_BEFORE_JSON" protection_state)"
[[ "$PROTECTION_BEFORE" == "protected" ]] ||
  die "bx status --json protection_state must be 'protected' before rehearsal (got: '$PROTECTION_BEFORE'); see $LOG_DIR/status-before.err"
echo "Preflight OK: protection_state=protected, current=$CURRENT_BEFORE"

echo
echo "== 1/2 正向更新演练:update -> $VERSION2 =="
echo "Running: $BRIDGE_PATH update --package-file $DEV2_TARBALL --json"
UPDATE2_JSON=""
UPDATE2_EXIT=0
UPDATE2_JSON="$("$BRIDGE_PATH" update --package-file "$DEV2_TARBALL" --json 2>"$LOG_DIR/update-dev2.err" | tee "$LOG_DIR/update-dev2.json")" || UPDATE2_EXIT=$?
cat "$LOG_DIR/update-dev2.err" >&2 || true

echo
echo "== 断言(正向更新) =="
report "$([[ "$UPDATE2_EXIT" == "0" ]] && echo 0 || echo 1)" "bx update --package-file (dev2) exits 0 (got: $UPDATE2_EXIT)"

if echo "$UPDATE2_JSON" | grep -q '"phase":"committed"'; then
  report 0 'update JSON contains "phase":"committed"'
else
  report 1 'update JSON contains "phase":"committed" (got: '"$UPDATE2_JSON"')'
fi

CURRENT_AFTER2="$(readlink "$RUNTIME_CURRENT" 2>/dev/null || true)"
if [[ "$CURRENT_AFTER2" == "$VERSION2" ]]; then
  report 0 "readlink '$RUNTIME_CURRENT' == $VERSION2 (got: $CURRENT_AFTER2)"
else
  report 1 "readlink '$RUNTIME_CURRENT' == $VERSION2 (got: $CURRENT_AFTER2)"
fi

BRIDGE_VERSION_OUT="$("$BRIDGE_PATH" --version 2>&1 || true)"
if echo "$BRIDGE_VERSION_OUT" | grep -q "$VERSION2"; then
  report 0 "$BRIDGE_PATH --version output contains $VERSION2 (got: $BRIDGE_VERSION_OUT)"
else
  report 1 "$BRIDGE_PATH --version output contains $VERSION2 (got: $BRIDGE_VERSION_OUT)"
fi

STATUS_AFTER2_JSON="$("$BRIDGE_PATH" status --json 2>"$LOG_DIR/status-after-dev2.err" || true)"
PROTECTION_AFTER2="$(json_field "$STATUS_AFTER2_JSON" protection_state)"
if [[ "$PROTECTION_AFTER2" == "protected" ]]; then
  report 0 "bx status --json protection_state back to protected after update (got: $PROTECTION_AFTER2)"
else
  report 1 "bx status --json protection_state back to protected after update (got: $PROTECTION_AFTER2)"
fi

RECEIPT_OUTCOME2="$(json_field "$RECEIPT_PATH" outcome)"
if [[ "$RECEIPT_OUTCOME2" == "committed" ]]; then
  report 0 "$RECEIPT_PATH outcome==committed (got: $RECEIPT_OUTCOME2)"
else
  report 1 "$RECEIPT_PATH outcome==committed (got: $RECEIPT_OUTCOME2)"
fi

if [[ ! -e "$TRANSACTION_PATH" ]]; then
  report 0 "$TRANSACTION_PATH does not exist after commit"
else
  report 1 "$TRANSACTION_PATH does not exist after commit (it still exists)"
fi

echo
echo "== 2/2 回滚演练:update -> $VERSION3(坏包,预期自动回滚到 $VERSION2) =="
echo "Running: $BRIDGE_PATH update --package-file $DEV3_TARBALL --json"
UPDATE3_JSON=""
UPDATE3_EXIT=0
UPDATE3_JSON="$("$BRIDGE_PATH" update --package-file "$DEV3_TARBALL" --json 2>"$LOG_DIR/update-dev3.err" | tee "$LOG_DIR/update-dev3.json")" || UPDATE3_EXIT=$?
cat "$LOG_DIR/update-dev3.err" >&2 || true

echo
echo "== 断言(回滚演练) =="
report "$([[ "$UPDATE3_EXIT" != "0" ]] && echo 0 || echo 1)" "bx update --package-file (dev3, corrupt) exits non-zero (got: $UPDATE3_EXIT)"

if echo "$UPDATE3_JSON" | grep -q '"rolled_back":true'; then
  report 0 'update JSON contains "rolled_back":true'
else
  report 1 'update JSON contains "rolled_back":true (got: '"$UPDATE3_JSON"')'
fi

CURRENT_AFTER3="$(readlink "$RUNTIME_CURRENT" 2>/dev/null || true)"
if [[ "$CURRENT_AFTER3" == "$VERSION2" ]]; then
  report 0 "readlink '$RUNTIME_CURRENT' still == $VERSION2 after rollback (got: $CURRENT_AFTER3)"
else
  report 1 "readlink '$RUNTIME_CURRENT' still == $VERSION2 after rollback (got: $CURRENT_AFTER3)"
fi

STATUS_AFTER3_JSON="$("$BRIDGE_PATH" status --json 2>"$LOG_DIR/status-after-dev3.err" || true)"
PROTECTION_AFTER3="$(json_field "$STATUS_AFTER3_JSON" protection_state)"
if [[ "$PROTECTION_AFTER3" == "protected" ]]; then
  report 0 "bx status --json protection_state back to protected after rollback (got: $PROTECTION_AFTER3)"
else
  report 1 "bx status --json protection_state back to protected after rollback (got: $PROTECTION_AFTER3)"
fi

RECEIPT_OUTCOME3="$(json_field "$RECEIPT_PATH" outcome)"
if [[ "$RECEIPT_OUTCOME3" == "rolled_back" ]]; then
  report 0 "$RECEIPT_PATH outcome==rolled_back (got: $RECEIPT_OUTCOME3)"
else
  report 1 "$RECEIPT_PATH outcome==rolled_back (got: $RECEIPT_OUTCOME3)"
fi

if [[ ! -e "$TRANSACTION_PATH" ]]; then
  report 0 "$TRANSACTION_PATH does not exist after rollback"
else
  report 1 "$TRANSACTION_PATH does not exist after rollback (it still exists)"
fi

echo
echo "如需还原 dev:重跑本脚本用 dev 包正向更新(BX_VERSION=dev 打包后"
echo "  sudo $BRIDGE_PATH update --package-file <dev 包路径> --json)。"
echo "当前保护会话未受影响,全程只调用了 bx update/bx status/bx --version。"

if [[ "$FAILED" != "0" ]]; then
  echo
  echo "One or more assertions FAILED."
  exit 1
fi

echo
echo "All assertions PASSED."
