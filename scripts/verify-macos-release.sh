#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
VERSION="${BX_VERSION:-dev}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist.noindex/release}"
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
# 成立:覆盖安装到一台已经装过 bx 的机器上时,安装会在用户确认后把保护重启回来。
# 这里钉住的必须是真话,否则 CI 会把一句假话钉死在发布物里(旧版正是如此:
# 它 grep 那三句无条件的承诺,谁去改正就撞红)。
grep -qF "全新安装不启动保护、不修改 DNS/路由" "$RELEASE_DIR/README.txt" || fail "README missing fresh-install network safety note"
grep -qF "安装会在你确认后重启保护" "$RELEASE_DIR/README.txt" || fail "README missing upgrade-restarts-protection disclosure"
grep -qF "Install bx" "$RELEASE_DIR/README.txt" || fail "README missing Install bx menu note"
grep -qF "sudo bx uninstall" "$RELEASE_DIR/README.txt" || fail "README missing uninstall pointer"
grep -qF "普通 macOS 用户身份运行" "$RELEASE_DIR/README.txt" || fail "README missing non-root install note"

grep -qF "app-install --app-source" "$RELEASE_DIR/install.sh" || fail "install.sh missing app-install invocation"

# **菜单栏的 bootstrap 必须在 install.sh 里(不提权那一侧),而且必须在 sudo 之后。**
#
# bootstrap 是少数必须身处目标 GUI session 的 launchctl 操作。root 直接做必然
# EIO(5),`asuser` 只是个 workaround —— 2026-08-11 真机升级证明它不可靠:
# app-install 成功 bootout 了正在跑的菜单栏,随后 asuser+bootstrap 失败,
# **升级把一个本来好好的菜单栏弄没了**,而安装照样报成功。
#
# 这类失败是隐形的(菜单不见了,而所有输出都说完成),所以不能靠「记得加」。
grep -qF 'launchctl bootstrap "gui/$(id -u)"' "$RELEASE_DIR/install.sh" \
  || fail "install.sh 没有在用户身份下 bootstrap 菜单栏 —— root 侧的 asuser 不可靠,升级会把菜单弄没"
# 顺序:必须在那条 sudo 之后,否则 plist 还没被写出来。
sudo_line=$(grep -n 'sudo .*app-install' "$RELEASE_DIR/install.sh" | head -1 | cut -d: -f1)
boot_line=$(grep -n 'launchctl bootstrap "gui/\$(id -u)"' "$RELEASE_DIR/install.sh" | head -1 | cut -d: -f1)
[[ -n "$sudo_line" && -n "$boot_line" && "$boot_line" -gt "$sudo_line" ]] \
  || fail "菜单 bootstrap 必须排在 app-install 之后(plist 由它写出)"
# install.sh 刻意不写死 --yes(用户就在终端前,该问就问),但必须把自己的参数透传,
# 否则非交互调用方无处表态:app-install 问不出来会非零退出,而 install.sh 又不给
# 任何加 --yes 的途径,升级就成了死路。
grep -qF 'app-install --app-source "$DIR/Bx.app" $ASSUME_YES' "$RELEASE_DIR/install.sh" || fail "install.sh must forward an opt-in --yes to app-install"
# `--` 不能省:模式以 `--` 开头,不加分隔符 grep 会把它当成自己的选项、
# 打一屏 usage 然后非零退出 —— 于是这条守卫**从来没有真正比对过内容**,
# 它只是每次都失败。verify 因此从来跑不完,而「跑不完」被当成了别的问题。
grep -qF -- '--yes|-y) ASSUME_YES="--yes"' "$RELEASE_DIR/install.sh" || fail "install.sh must accept --yes"
# 用户明确说「不」时 app-install 退出 2;脚本必须据此不再打印「完成」。
grep -qF 'if [ "$rc" -eq 2 ]' "$RELEASE_DIR/install.sh" || fail "install.sh must branch on the explicit-cancel exit code"
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
