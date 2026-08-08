#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
VERSION="${BX_VERSION:-dev}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist/release}"
RELEASE_DIR="$DIST_ROOT/$RELEASE_NAME"
ARCHIVE="$DIST_ROOT/$RELEASE_NAME.tar.gz"
SUMS="$DIST_ROOT/SHA256SUMS"
RESOURCES="$RELEASE_DIR/Bx.app/Contents/Resources"

fail() {
  echo "verify failed: $*" >&2
  exit 1
}

[[ ! -e "$RELEASE_DIR/bx" ]] || fail "top-level bx must not be present in the package"
[[ -d "$RELEASE_DIR/Bx.app" ]] || fail "missing Bx.app"
[[ -x "$RELEASE_DIR/install.sh" ]] || fail "missing executable install.sh"
[[ -x "$RELEASE_DIR/uninstall.sh" ]] || fail "missing executable uninstall.sh"
[[ -f "$RELEASE_DIR/README.txt" ]] || fail "missing README.txt"
[[ -f "$ARCHIVE" ]] || fail "missing archive $ARCHIVE"
[[ -f "$SUMS" ]] || fail "missing SHA256SUMS"
[[ -x "$RESOURCES/bx-cli" ]] || fail "missing executable bx-cli"
[[ -x "$RESOURCES/bx-bridge" ]] || fail "missing executable bx-bridge"
[[ -f "$RESOURCES/release.json" ]] || fail "missing release.json"

case "$ARCH" in
  arm64) MACH_ARCH="arm64" ;;
  amd64) MACH_ARCH="x86_64" ;;
  *) fail "unsupported architecture $ARCH" ;;
esac
file "$RESOURCES/bx-cli" | grep -q "Mach-O.*$MACH_ARCH" || fail "bx-cli is not $MACH_ARCH"
file "$RESOURCES/bx-bridge" | grep -q "Mach-O.*$MACH_ARCH" || fail "bx-bridge is not $MACH_ARCH"
file "$RELEASE_DIR/Bx.app/Contents/MacOS/BxMenu" | grep -q "Mach-O.*$MACH_ARCH" || fail "BxMenu is not $MACH_ARCH"

plutil -lint "$RELEASE_DIR/Bx.app/Contents/Info.plist" >/dev/null
plutil -extract NSAppleEventsUsageDescription raw "$RELEASE_DIR/Bx.app/Contents/Info.plist" >/dev/null || fail "Info.plist missing Apple Events usage description"
tar -tzf "$ARCHIVE" >/dev/null

python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$RESOURCES/release.json" || fail "release.json is not valid JSON"
python3 -c '
import json, sys
data = json.load(open(sys.argv[1]))
if data.get("version") != sys.argv[2]:
    sys.exit("release.json version mismatch: %r != %r" % (data.get("version"), sys.argv[2]))
' "$RESOURCES/release.json" "$VERSION" || fail "release.json version does not match BX_VERSION"

RELEASE_CLI_SHA=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["assets"]["bx-cli"])' "$RESOURCES/release.json")
RELEASE_BRIDGE_SHA=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["assets"]["bx-bridge"])' "$RESOURCES/release.json")
ACTUAL_CLI_SHA=$(shasum -a 256 "$RESOURCES/bx-cli" | awk '{print $1}')
ACTUAL_BRIDGE_SHA=$(shasum -a 256 "$RESOURCES/bx-bridge" | awk '{print $1}')
[[ "$RELEASE_CLI_SHA" == "$ACTUAL_CLI_SHA" ]] || fail "release.json bx-cli digest mismatch"
[[ "$RELEASE_BRIDGE_SHA" == "$ACTUAL_BRIDGE_SHA" ]] || fail "release.json bx-bridge digest mismatch"

grep -qF "install.sh 不会执行 bx setup" "$RELEASE_DIR/README.txt" || fail "README missing no-setup note"
# 升级路径改成「一次做完」之后,「不会执行 bx up、不修改 DNS/路由」只对全新安装
# 成立:覆盖安装到一台保护正在运行的机器上时,安装会在用户确认后把保护重启回来。
# 这里钉住的必须是真话,否则 CI 会把一句假话钉死在发布物里(旧版正是如此:
# 它 grep 那三句无条件的承诺,谁去改正就撞红)。
grep -qF "全新安装不启动保护、不修改 DNS/路由" "$RELEASE_DIR/README.txt" || fail "README missing fresh-install network safety note"
grep -qF "安装会在你确认后重启保护" "$RELEASE_DIR/README.txt" || fail "README missing upgrade-restarts-protection disclosure"
grep -qF "Install bx" "$RELEASE_DIR/README.txt" || fail "README missing Install bx menu note"
grep -qF "sudo bx uninstall" "$RELEASE_DIR/README.txt" || fail "README missing uninstall pointer"
grep -qF "普通 macOS 用户身份运行" "$RELEASE_DIR/README.txt" || fail "README missing non-root install note"

grep -qF "app-install --app-source" "$RELEASE_DIR/install.sh" || fail "install.sh missing app-install invocation"
# install.sh 刻意不写死 --yes(用户就在终端前,该问就问),但必须把自己的参数透传,
# 否则非交互调用方无处表态:app-install 问不出来会非零退出,而 install.sh 又不给
# 任何加 --yes 的途径,升级就成了死路。
grep -qF 'app-install --app-source "$DIR/Bx.app" "$@"' "$RELEASE_DIR/install.sh" || fail "install.sh must forward its own args to app-install"
! grep -qE 'app-install .*--yes' "$RELEASE_DIR/install.sh" || fail "install.sh must not hardcode --yes"
grep -qF "全新安装不会启动保护" "$RELEASE_DIR/install.sh" || fail "install.sh missing fresh-install protection note"
grep -qF "再停止保护、换好文件、重启保护服务" "$RELEASE_DIR/install.sh" || fail "install.sh missing upgrade outage disclosure"
! grep -qF "bx setup" "$RELEASE_DIR/install.sh" || fail "install.sh must not invoke bx setup"
! grep -qF "bx up " "$RELEASE_DIR/install.sh" || fail "install.sh must not invoke bx up"
! grep -qF "networksetup" "$RELEASE_DIR/install.sh" || fail "install.sh must not touch networksetup"
grep -qF "架构不匹配" "$RELEASE_DIR/install.sh" || fail "install.sh missing architecture preflight"
grep -qF '[ "$(id -u)" -ne 0 ]' "$RELEASE_DIR/install.sh" || fail "install.sh missing non-root install guard"
grep -qF '[ -x "$DIR/Bx.app/Contents/Resources/bx-cli" ]' "$RELEASE_DIR/install.sh" || fail "install.sh missing bx-cli preflight"

grep -qF "bx uninstall" "$RELEASE_DIR/uninstall.sh" || fail "uninstall.sh missing bx uninstall pointer"

(
  cd "$DIST_ROOT"
  shasum -a 256 -c SHA256SUMS >/dev/null
)

echo "macOS release verified: $RELEASE_DIR"
