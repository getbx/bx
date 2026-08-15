#!/usr/bin/env bash
# 把已经打好的 release 目录做成 .dmg —— 那个「把图标拖进 Applications」的标准画面。
#
# 为什么要 dmg:tar.gz 解压出来是一个文件夹,小白拿到的是一堆文件,还得自己知道
# 「把 Bx.app 拖到 /Applications」。dmg 挂载后是一个窗口、两个图标、一条箭头,
# 这是 macOS 用户认得的那个动作,不需要任何说明。
#
# **它依赖 package-macos-release.sh 先跑过**(那里做签名与校验);本脚本只负责封装。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
VERSION="${BX_VERSION:-dev}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist.noindex/release}"
RELEASE_DIR="$DIST_ROOT/$RELEASE_NAME"
DMG="$DIST_ROOT/$RELEASE_NAME.dmg"

[ -d "$RELEASE_DIR/Bx.app" ] || { echo "先跑 scripts/package-macos-release.sh" >&2; exit 1; }

# **进 dmg 的东西刻意只有三样。** install.sh / uninstall.sh 不放进去:
# dmg 的全部意义是「拖一下就好」,而窗口里多两个脚本会让用户以为自己得跑点什么。
# 命令行安装那条路仍然在 tar.gz 里,给知道自己在做什么的人。
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
ditto "$RELEASE_DIR/Bx.app" "$STAGE/Bx.app"
ln -s /Applications "$STAGE/Applications"
cp "$RELEASE_DIR/README.txt" "$STAGE/README.txt"

rm -f "$DMG"
hdiutil create -volname "bx $VERSION" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null

# **签名必须活过封装。** ditto 会保留签名,但一次失手(用 cp -r 而不是 ditto、
# 或事后动了 bundle 里的文件)就会把它弄坏 —— 而坏签名给用户的是「已损坏,
# 应移到废纸篓」,那条路没有任何放行入口。所以这里再验一次。
MOUNT="$(mktemp -d)"
hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MOUNT" >/dev/null
if ! codesign --verify --deep --strict "$MOUNT/Bx.app" 2>/dev/null; then
	hdiutil detach "$MOUNT" >/dev/null || true
	rm -rf "$MOUNT"
	echo "dmg 里的 Bx.app 签名无效 —— 用户会看到「已损坏」,而那条路没有放行入口" >&2
	exit 1
fi
hdiutil detach "$MOUNT" >/dev/null
rm -rf "$MOUNT"

(cd "$DIST_ROOT" && shasum -a 256 "$RELEASE_NAME.dmg" >> SHA256SUMS)
echo "DMG: $DMG"
