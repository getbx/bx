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
echo "This installs Bx.app into /Applications and sets bx up (one administrator prompt)."
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

Install (the .dmg is the easy way):
  1. Open bx-macos-ARCH.dmg, drag Bx.app onto Applications and double-click it.
     It walks you through the rest — no terminal needed.
  2. Or: drag the Bx.app in this folder to /Applications, open it and click "Install bx...".
  3. Or: run ./install.sh (the same thing, from a terminal).

The first time you open it macOS will say the developer cannot be verified — bx is not
signed with an Apple Developer ID. To allow it: System Settings -> Privacy & Security ->
scroll down to the bx entry -> Open Anyway.
(That is NOT the same as "is damaged and should be moved to the Trash". If you see that
one, the package was tampered with — do not allow it, download it again.)

What installing does:
  Puts Bx.app in /Applications, installs the bx-cli inside it as the system bx command,
  and sets up the Guardian protection service and the login item. It does not touch your
  connection settings. A fresh install does not turn protection on.
  If bx is already installed on this machine (the Guardian service is loaded, whether or
  not protection is on — i.e. an upgrade), you are asked first; then protection stops, the
  files are swapped, the service restarts and protection returns to how you had it. If it
  was on, the network drops for a few seconds in between.

After installing:
  Open the bx icon in the menu bar and choose Set Up bx... to continue.

Uninstall:
  sudo bx uninstall
  (see ./uninstall.sh)

Notes:
TXT

chmod +x "$RELEASE_DIR/install.sh" "$RELEASE_DIR/uninstall.sh"

# **给整个 bundle 签名 —— 没有证书时也要签。**
#
# Go 的链接器会给它产出的二进制自动打一个 ad-hoc 签名,但那只签了**二进制**,
# 没签 bundle:装配好的 Bx.app 里 Contents/Resources 下还塞了 bx-cli / bx-bridge /
# release.json,它们不在任何签名的覆盖范围里。后果实测(2026-08-13,macOS 26.5.2):
#
#   codesign --verify  →  code has no resources but signature indicates they must be present
#
# **那不是「不受信任」,是「无效」** —— 而这两者对用户是天差地别的两个弹窗:
#   签名无效  →  「已损坏,应移到废纸篓」,**没有任何放行入口**;
#   签名有效但不受信任 → 「无法验证开发者」,系统设置 → 隐私与安全性 里有 Open Anyway。
#
# ad-hoc 签整个 bundle 不需要任何证书,而它正好把前者变成后者。有 Developer ID 时
# 把 BX_CODESIGN_IDENTITY 设成那个身份即可(之后还要 notarytool 公证,那是另一步)。
SIGN_IDENTITY="${BX_CODESIGN_IDENTITY:--}"
echo "Signing Bx.app (identity: $SIGN_IDENTITY)..."
codesign --force --deep --sign "$SIGN_IDENTITY" "$RELEASE_DIR/Bx.app"
# **签完必须验。** 一个签失败却继续打包的脚本,产出的正是上面那个「已损坏」。
codesign --verify --deep --strict "$RELEASE_DIR/Bx.app" || {
  echo "签名验证失败 —— 这个包会让用户看到「已损坏,应移到废纸篓」,而那条路没有放行入口" >&2
  exit 1
}

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
