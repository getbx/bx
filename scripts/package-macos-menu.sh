#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_NAME="Bx"
BUNDLE_ID="com.getbx.bx.menu"
MENU_DIR="$ROOT/apps/macos/BxMenu"
DIST_DIR="${BX_DIST_DIR:-$ROOT/dist/macos}"
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

cd "$MENU_DIR"
swift build -c release --arch "$SWIFT_ARCH" -Xswiftc -target -Xswiftc "$SWIFT_ARCH-apple-macosx13.0"

rm -rf "$APP_DIR"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"
install -m 0755 "$MENU_BINARY" "$MACOS_DIR/BxMenu"

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
