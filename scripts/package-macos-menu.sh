#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_NAME="Bx"
BUNDLE_ID="com.getbx.bx.menu"
MENU_DIR="$ROOT/apps/macos/BxMenu"
DIST_DIR="${BX_DIST_DIR:-$ROOT/dist.noindex/macos}"
APP_DIR="$DIST_DIR/$APP_NAME.app"
CONTENTS_DIR="$APP_DIR/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"
LAUNCH_AGENT="$DIST_DIR/$BUNDLE_ID.plist"
LOG_DIR="${BX_LOG_DIR:-$HOME/Library/Logs/bx}"
VERSION="${BX_VERSION:-dev}"
ARCH="${BX_ARCH:-$(uname -m)}"

case "$ARCH" in
  arm64) SWIFT_ARCH="arm64" ;;
  amd64|x86_64) SWIFT_ARCH="x86_64" ;;
  *)
    echo "Unsupported BX_ARCH=$ARCH; use arm64 or amd64." >&2
    exit 2
    ;;
esac
MENU_BINARY="$MENU_DIR/.build/$SWIFT_ARCH-apple-macosx/release/BxMenu"

"$ROOT/scripts/test-macos-menu.sh"

mkdir -p "$DIST_DIR"
# **不要在这里 touch .metadata_never_index。**
#
# 那个文件只在**卷根目录**生效;放在一个普通目录里 Spotlight 照样索引 ——
# 这台开发机上实测过:文件在,而 dist/macos-arm64/Bx.app 仍然被 mdfind 找得到。
# 后果不只是噪声:在 Spotlight 里搜 "bx" 会出来五个 Bx.app,而点开构建产物里
# 那个,跑的是一份陈旧的菜单二进制,对着当前的 Guardian。
#
# 真正生效的机制是**目录名以 .noindex 结尾**(Xcode 的 DerivedData 就是这么做的),
# 所以全部构建产物都落在 dist.noindex/ 下面。

cd "$MENU_DIR"
swift build -c release --arch "$SWIFT_ARCH" -Xswiftc -target -Xswiftc "$SWIFT_ARCH-apple-macosx13.0"

rm -rf "$APP_DIR"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"
install -m 0755 "$MENU_BINARY" "$MACOS_DIR/BxMenu"

# **图标。** 在此之前 Bx.app 一个图标都没有 —— Resources 里没有 .icns,
# Info.plist 里连 CFBundleIconFile 都没有,于是 Finder / Launchpad / Spotlight
# 全都画那个灰色占位方块。一个既没签名又没图标的 app,在普通用户眼里与恶意软件
# 没有区别,而这条恰恰是「小白也能装」那条路上最先被看见的一步。
#
# 源图是设计包的 iconset(apps/macos/BxMenu/Resources/Bx.iconset,来自
# bx_B_integrated_production_v3/macos/Bx.iconset):b+x 那个产品标,十档尺寸各自单独
# 出图 —— 设计包 README 明说别自己从 1024 缩(16/32 会糊,而 16 正是 Spotlight 那一格)。
# 2026-09-28 之前这里从 winres/icon1024.png(Windows 时代的绿盾 + b)缩,于是泄漏检测页
# 与 server ui 早换了新标而 Dock、更新弹窗、Finder 里还是旧的。菜单栏那个盾牌是保护状态,
# 不是产品标,不动(MenuIcon.swift)。转换只用 macOS 自带的 iconutil,不引入任何依赖。
ICONSET="$ROOT/apps/macos/BxMenu/Resources/Bx.iconset"
if [ ! -d "$ICONSET" ]; then
  echo "缺 $ICONSET —— 设计包的 iconset 没有 vendored 进仓库" >&2
  exit 1
fi
iconutil -c icns "$ICONSET" -o "$RESOURCES_DIR/AppIcon.icns"

cat > "$CONTENTS_DIR/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleDisplayName</key>
  <string>bx</string>
  <key>CFBundleExecutable</key>
  <string>BxMenu</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>CFBundleIdentifier</key>
  <string>$BUNDLE_ID</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>bx</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>$VERSION</string>
  <key>CFBundleVersion</key>
  <string>$VERSION</string>
  <key>LSMinimumSystemVersion</key>
  <string>13.0</string>
  <key>LSUIElement</key>
  <true/>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>NSAppleEventsUsageDescription</key>
  <string>bx opens Terminal only when you choose Run Doctor from the menu.</string>
</dict>
</plist>
PLIST

cat > "$LAUNCH_AGENT" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$BUNDLE_ID</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Applications/$APP_NAME.app/Contents/MacOS/BxMenu</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>$LOG_DIR/menu.log</string>
  <key>StandardErrorPath</key>
  <string>$LOG_DIR/menu.err.log</string>
</dict>
</plist>
PLIST

echo "Built: $APP_DIR"
echo "LaunchAgent: $LAUNCH_AGENT"
echo
echo "Install app:"
echo "  mkdir -p ~/Applications"
echo "  ditto '$APP_DIR' ~/Applications/$APP_NAME.app"
echo
echo "Start at login:"
echo "  scripts/install-macos-menu.sh install"
