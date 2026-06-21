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

cd "$MENU_DIR"
swift build -c release

rm -rf "$APP_DIR"
mkdir -p "$MACOS_DIR" "$RESOURCES_DIR"
install -m 0755 "$MENU_DIR/.build/release/BxMenu" "$MACOS_DIR/BxMenu"

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
  <string>0.1.0</string>
  <key>CFBundleVersion</key>
  <string>1</string>
  <key>LSMinimumSystemVersion</key>
  <string>13.0</string>
  <key>LSUIElement</key>
  <true/>
  <key>NSHighResolutionCapable</key>
  <true/>
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
  <string>/tmp/bx-menu.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/bx-menu.err.log</string>
</dict>
</plist>
PLIST

echo "Built: $APP_DIR"
echo "LaunchAgent: $LAUNCH_AGENT"
echo
echo "Install app:"
echo "  sudo ditto '$APP_DIR' '/Applications/$APP_NAME.app'"
echo
echo "Start at login:"
echo "  mkdir -p ~/Library/LaunchAgents"
echo "  cp '$LAUNCH_AGENT' ~/Library/LaunchAgents/"
echo "  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/$BUNDLE_ID.plist"
echo "  launchctl kickstart -k gui/$(id -u)/$BUNDLE_ID"
