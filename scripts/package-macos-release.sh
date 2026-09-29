#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
VERSION="${BX_VERSION:-dev}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist.noindex/release}"
RELEASE_DIR="$DIST_ROOT/$RELEASE_NAME"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS release packaging requires macOS." >&2
  exit 1
fi

case "$ARCH" in
  arm64|amd64) ;;
  *)
    echo "Unsupported BX_ARCH=$ARCH; use arm64 or amd64." >&2
    exit 2
    ;;
esac

rm -rf "$RELEASE_DIR"
# **上一次的产物必须先删掉。**
#
# 只删 RELEASE_DIR 是不够的:tar.gz / dmg / SHA256SUMS 落在它的**上一层**,
# 于是某一步失败或被跳过时,校验器会找到上一次成功留下的那一份、照样放行。
# 变异验证当场证实了这一点 —— 去掉生成 dmg 那一步,verify 仍然全绿。
# 陈旧产物能盖住这次的缺失,是发布流程里最不该有的一种绿灯。
rm -f "$DIST_ROOT/$RELEASE_NAME.tar.gz" "$DIST_ROOT/$RELEASE_NAME.dmg" "$DIST_ROOT/SHA256SUMS"
mkdir -p "$RELEASE_DIR"
# **不要在这里 touch .metadata_never_index。**
#
# 那个文件只在**卷根目录**生效;放在一个普通目录里 Spotlight 照样索引 ——
# 这台开发机上实测过:文件在,而 dist/macos-arm64/Bx.app 仍然被 mdfind 找得到。
# 后果不只是噪声:在 Spotlight 里搜 "bx" 会出来五个 Bx.app,而点开构建产物里
# 那个,跑的是一份陈旧的菜单二进制,对着当前的 Guardian。
#
# 真正生效的机制是**目录名以 .noindex 结尾**(Xcode 的 DerivedData 就是这么做的),
# 所以全部构建产物都落在 dist.noindex/ 下面。

echo "Building bx for darwin/$ARCH..."
GOOS=darwin GOARCH="$ARCH" go build -trimpath -ldflags "-X github.com/getbx/bx/internal/version.Version=$VERSION" -o "$RELEASE_DIR/bx" "$ROOT"

echo "Packaging menu bar app..."
BX_ARCH="$ARCH" BX_VERSION="$VERSION" BX_DIST_DIR="$ROOT/dist.noindex/macos-$ARCH" "$ROOT/scripts/package-macos-menu.sh" >/dev/null
ditto "$ROOT/dist.noindex/macos-$ARCH/Bx.app" "$RELEASE_DIR/Bx.app"

echo "Embedding release assets into Bx.app..."
RESOURCES="$RELEASE_DIR/Bx.app/Contents/Resources"
install -m 0755 "$RELEASE_DIR/bx" "$RESOURCES/bx-cli"
GOOS=darwin GOARCH="$ARCH" go build -trimpath -ldflags "-X github.com/getbx/bx/internal/version.Version=$VERSION" \
  -o "$RESOURCES/bx-bridge" "$ROOT/cmd/bx-bridge"

# **签名:有 Developer ID 就按公证的要求签,没有就 ad-hoc;两条路都要签。**
#
# 为什么没有证书也要签(2026-08-13 真机):Go 的链接器只给二进制打 ad-hoc 签名,
# 不签 bundle,而装配好的 Bx.app 在 Contents/Resources 下还塞了 bx-cli / bx-bridge /
# release.json —— 它们不在任何签名的覆盖范围里,于是
#   codesign --verify  →  code has no resources but signature indicates they must be present
# **那不是「不受信任」,是「无效」**,而这两者对用户是天差地别的两个弹窗:
#   签名无效  →  「已损坏,应移到废纸篓」,**没有任何放行入口**;
#   签名有效但不受信任 → 「无法验证开发者」,系统设置 → 隐私与安全性 里有 Open Anyway。
#
# 有 Developer ID(2026-09-28 起,the publisher's LLC)时公证要求**每一个 Mach-O** 都
# 带 hardened runtime 与可信时间戳,而 Resources 下的两个可执行文件不算 nested code,
# `--deep` 不会替它们签 —— 所以三样各签一遍,顺序由内到外:先两个二进制,再算它们
# 的摘要写进 release.json(签名改变字节,先算摘要再签会让 app-install 的校验对不上),
# 最后签整个 bundle(封住 Resources)。
SIGN_IDENTITY="${BX_CODESIGN_IDENTITY:--}"
SIGN_FLAGS=(--force --sign "$SIGN_IDENTITY")
if [[ "$SIGN_IDENTITY" != "-" ]]; then
  SIGN_FLAGS+=(--options runtime --timestamp)
fi
echo "Signing bx-cli and bx-bridge (identity: $SIGN_IDENTITY)..."
codesign "${SIGN_FLAGS[@]}" "$RESOURCES/bx-cli"
codesign "${SIGN_FLAGS[@]}" "$RESOURCES/bx-bridge"
CLI_SHA=$(shasum -a 256 "$RESOURCES/bx-cli" | awk '{print $1}')
BRIDGE_SHA=$(shasum -a 256 "$RESOURCES/bx-bridge" | awk '{print $1}')
cat > "$RESOURCES/release.json" <<EOF
{
  "schema_version": 1,
  "version": "$VERSION",
  "platform": "darwin/$ARCH",
  "assets": {
    "bx-cli": "$CLI_SHA",
    "bx-bridge": "$BRIDGE_SHA"
  }
}
EOF
rm "$RELEASE_DIR/bx"   # 顶层裸 bx 不再进包(经 App 内 bx-cli 由 app-install 安装)

cat > "$RELEASE_DIR/install.sh" <<'SCRIPT'
#!/bin/bash
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
[ "$(uname -s)" = "Darwin" ] || { echo "macOS only" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || { echo "do not run this as root (it asks for sudo once, when it installs)" >&2; exit 1; }
MACHINE="$(uname -m)"
case "__BX_RELEASE_ARCH__:$MACHINE" in
  arm64:arm64|amd64:x86_64) ;;
  *) echo "wrong architecture: this package is __BX_RELEASE_ARCH__, this machine is $MACHINE" >&2; exit 1 ;;
esac
[ -x "$DIR/Bx.app/Contents/Resources/bx-cli" ] || { echo "incomplete package: bx-cli is missing" >&2; exit 1; }
# 只认 --yes/-y:既给非交互调用方一个表态的途径,又不把任意参数拼进一条 sudo
# 命令行(也顺手让 ./install.sh --help 不再走到下面那句「完成」)。
ASSUME_YES=""
for arg in "$@"; do
  case "$arg" in
    --yes|-y) ASSUME_YES="--yes" ;;
    *) echo "usage: ./install.sh [--yes]" >&2; exit 2 ;;
  esac
done
echo "This installs Bx.app into /Applications and configures it (one administrator prompt)."
echo "Your connection settings are left alone. A fresh install does not turn protection on."
echo "If bx is already installed here, you are asked first, and then protection is stopped,"
echo "the files are swapped, the service restarts and protection returns to how you had it"
# 不加 --yes:命令行安装时用户就在终端前,该问就问(会断网的操作必须当面确认)。
# 非交互场景(无终端的 SSH/CI)由用户显式 ./install.sh --yes 表态,经 "$@" 透传;
# 不表态时 app-install 会以非零退出报错,set -e 就会在打印「完成」之前中止。
# ASSUME_YES 不加引号:它要么是空(不传参),要么是单个 --yes,不会被词分割。
rc=0
sudo "$DIR/Bx.app/Contents/Resources/bx-cli" app-install --app-source "$DIR/Bx.app" $ASSUME_YES || rc=$?
if [ "$rc" -eq 2 ]; then
  echo "Cancelled: nothing was changed."
  exit 2
elif [ "$rc" -ne 0 ]; then
  exit "$rc"
fi
# **菜单栏的 LaunchAgent 由这里 bootstrap,不由上面那条 sudo。**
#
# bootstrap 是 launchctl 里少数**必须身处目标 GUI session** 的操作(bootout 与
# kickstart 不是)。root 进程没有目标用户 GUI 域的 audit session token,直接
# bootstrap 必然 EIO(5);`launchctl asuser <uid>` 是个 workaround,而 2026-08-11
# 真机升级证明它**不可靠** —— 那次 app-install 成功 bootout 了正在跑的菜单栏,
# 随后 asuser+bootstrap 报 `Bootstrap failed: 5: Input/output error`,于是**升级把
# 一个本来好好的菜单栏弄没了**:launchd 里没有 job、进程也没了,而 plist 与程序
# 都完好。
#
# 而这个脚本本来就以普通用户身份跑(第 5 行明确拒绝 root),它**就在**那个 GUI
# session 里 —— 这一步天生属于它,不属于那条 sudo。
#
# 失败只警告不中止:无 GUI session 的场景(SSH、CI)bootstrap 一定失败,而那时
# 「文件都装好了」仍然是真的,不该把整个安装判成失败。
if [ -f "$HOME/Library/LaunchAgents/com.getbx.bx.menu.plist" ]; then
  if ! launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.getbx.bx.menu.plist" 2>/dev/null; then
    # 已经加载时 bootstrap 会失败,那是正常的 —— 用 kickstart 兜一下(不带 -k:
    # 跑着就是 no-op,不闪烁)。两条都失败才提示用户。
    launchctl kickstart "gui/$(id -u)/com.getbx.bx.menu" 2>/dev/null || {
      echo "! The menu bar app did not start by itself. Run this by hand (no sudo):"
      echo "    launchctl bootstrap gui/$(id -u) $HOME/Library/LaunchAgents/com.getbx.bx.menu.plist"
    }
  fi
fi
echo "Done. Open the bx icon in the menu bar to finish setting up."
SCRIPT

perl -0pi -e "s/__BX_RELEASE_ARCH__/$ARCH/g" "$RELEASE_DIR/install.sh"

cat > "$RELEASE_DIR/uninstall.sh" <<'SCRIPT'
#!/bin/bash
set -euo pipefail
echo "This package no longer ships a separate uninstaller."
echo "Run:"
echo "  sudo bx uninstall"
echo
echo "That disables and removes the Guardian protection service, Bx.app and the bx CLI,"
echo "and the launchd login item. It keeps /etc/bx (your settings) and /var/lib/bx (runtime data)."
SCRIPT

cat > "$RELEASE_DIR/README.txt" <<TXT
bx macOS $ARCH release ($VERSION)

IF macOS REFUSES TO OPEN bx
---------------------------
Official bx releases are signed with an Apple Developer ID and notarized by Apple,
so a normal download opens without any warning. If macOS refuses anyway, the
message tells you what went wrong:

  "bx cannot be opened because the developer cannot be verified"
  "Apple could not verify bx is free of malware"
      -> This copy is not an official release build, or its notarization could not
         be checked. Download it again from the official releases page.
         (Built it yourself? Then this is expected: System Settings ->
         Privacy & Security -> scroll down to the bx entry -> Open Anyway.)

  "bx is damaged and can't be opened. You should move it to the Trash"
      -> Do NOT allow this one, and do not run any xattr command you find online:
         that message means the package was modified after it was built.
         Download it again from the official releases page.

Install (the .dmg is the easy way)
----------------------------------
  1. Open bx-macos-ARCH.dmg, drag Bx.app onto Applications and double-click it.
     It walks you through the rest - no terminal needed.
  2. Or: drag the Bx.app in this folder to /Applications, open it and click "Install bx...".
  3. Or: run ./install.sh (the same thing, from a terminal).

What installing does
--------------------
  Puts Bx.app in /Applications, installs the bx-cli inside it as the system bx command,
  and sets up the Guardian protection service and the login item. It does not touch your
  connection settings.

After installing
----------------
  Open the bx icon in the menu bar and choose Set Up bx... to continue.

Uninstall
---------
  sudo bx uninstall
  (see ./uninstall.sh)

Notes
-----
  Run install.sh as your normal macOS user (it asks for administrator rights once, via
  sudo, when it needs them).
  Installing over a machine that already has bx (the Guardian service is loaded, whether
  or not protection is on) asks you first. Where there is no terminal to ask in
  (non-interactive SSH, CI) install.sh stops with an error rather than pretending it
  worked - if you mean to upgrade, run ./install.sh --yes.
  install.sh never runs bx setup: not one character of your connection settings changes.
  A fresh install does not turn protection on and does not touch DNS or routing. When you
  install over a machine that already has bx, protection is restarted after you confirm -
  DNS and routing are taken over again as a result, which is what resuming protection means.
  An older bx client (installed before this release) will fail to unpack this package with
  bx update --package and say so cleanly; that is expected (pre-1.0). Install it again the
  way this README describes instead.
  Problem reports: when bx fails, it sends a redacted report to the bx maintainer, through
  the tunnel only (never while protection is off). Server addresses, links and bypass ranges
  never enter a report. Copies stay in /var/lib/bx/reports (bx reports lists them); turn it
  off with reports: off in /etc/bx/config.yaml.
TXT

chmod +x "$RELEASE_DIR/install.sh" "$RELEASE_DIR/uninstall.sh"

echo "Signing Bx.app (identity: $SIGN_IDENTITY)..."
# 不带 --deep:Resources 下那两个二进制上面已经各自签过,这里签的是 bundle 本身
# (主可执行文件 BxMenu + 资源封条)。entitlements 见文件内注释。
codesign "${SIGN_FLAGS[@]}" --entitlements "$ROOT/apps/macos/BxMenu/BxMenu.entitlements" "$RELEASE_DIR/Bx.app"
# **签完必须验。** 一个签失败却继续打包的脚本,产出的正是上面那个「已损坏」。
codesign --verify --deep --strict "$RELEASE_DIR/Bx.app" || {
  echo "签名验证失败 —— 这个包会让用户看到「已损坏,应移到废纸篓」,而那条路没有放行入口" >&2
  exit 1
}

# **公证并把票据钉进 Bx.app,再进 tar.gz 与 dmg。**
#
# 三个变量齐了就公证(scripts/macos-notarize.sh),缺了就一个字不做 —— 本机
# 无证书的开发打包照旧能跑。但 Developer ID 签了却没公证的包**不许发出去**:
# verify-macos-release.sh 在身份是 Developer ID 时要求票据在、spctl 放行,少一步就红。
#
# 顺序是刻意的:先公证 app、把票据钉进 bundle,之后打的 tar.gz 与 dmg 里装的都是
# 带票据的那一份;dmg 自己再公证一次并钉票据(package-macos-dmg.sh)。用户拖出
# app 之后离线也能过 Gatekeeper,靠的就是 bundle 里那张票据。
"$ROOT/scripts/macos-notarize.sh" "$RELEASE_DIR/Bx.app"

(
  cd "$DIST_ROOT"
  rm -f "$RELEASE_NAME.tar.gz"
  tar -czf "$RELEASE_NAME.tar.gz" "$RELEASE_NAME"
  shasum -a 256 "$RELEASE_NAME.tar.gz" > SHA256SUMS
)

# **dmg 必须在这里生成。**
#
# README.txt 第一行就写着「安装(推荐用 .dmg)」,而在此之前**没有任何地方调用过
# package-macos-dmg.sh** —— 那个脚本写好了、放在那儿、一次都没被跑过。于是每一个
# 拿到这个包的人都被指向一个不存在的文件。
#
# 接在这里而不是让人另外敲一条:一条「推荐路径」如果需要额外一步才存在,
# 那它就不是推荐路径。
echo "Building .dmg..."
BX_ARCH="$ARCH" BX_VERSION="$VERSION" BX_RELEASE_DIR="$DIST_ROOT" "$ROOT/scripts/package-macos-dmg.sh"

(
  cd "$DIST_ROOT"
  # 校验和覆盖 dmg 与 tar.gz 两样 —— 只覆盖一半的清单比没有清单更糟:
  # 它看起来像是全都核对过了。
  shasum -a 256 "$RELEASE_NAME.tar.gz" "$RELEASE_NAME.dmg" > SHA256SUMS
)

echo "Built: $RELEASE_DIR"
echo "Archive: $DIST_ROOT/$RELEASE_NAME.tar.gz"
echo "Disk image: $DIST_ROOT/$RELEASE_NAME.dmg"
echo "Checksums: $DIST_ROOT/SHA256SUMS"
