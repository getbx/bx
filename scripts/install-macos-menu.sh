#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_NAME="Bx"
BUNDLE_ID="com.getbx.bx.menu"
APP_SRC="${BX_APP_SRC:-$ROOT/dist/macos/$APP_NAME.app}"
APP_DST="${BX_APP_DST:-$HOME/Applications/$APP_NAME.app}"
AGENT_SRC="${BX_AGENT_SRC:-$ROOT/dist/macos/$BUNDLE_ID.plist}"
AGENT_DIR="$HOME/Library/LaunchAgents"
AGENT_DST="$AGENT_DIR/$BUNDLE_ID.plist"
DOMAIN="gui/$(id -u)"

usage() {
  cat <<USAGE
Usage: scripts/install-macos-menu.sh [install|uninstall|restart|status]

Installs the bx macOS menu bar companion. This does not change bx network,
DNS, route, or service configuration.
USAGE
}

ensure_macos() {
  if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "This installer only supports macOS." >&2
    exit 1
  fi
}

build_package() {
  "$ROOT/scripts/package-macos-menu.sh" >/dev/null
}

bootout_agent() {
  launchctl bootout "$DOMAIN" "$AGENT_DST" >/dev/null 2>&1 || true
}

install_menu() {
  ensure_macos
  build_package
  echo "Installing bx menu bar app..."
  mkdir -p "$(dirname "$APP_DST")"
  ditto "$APP_SRC" "$APP_DST"
  mkdir -p "$AGENT_DIR"
  write_launch_agent "$AGENT_DST" "$APP_DST"
  bootout_agent
  launchctl bootstrap "$DOMAIN" "$AGENT_DST"
  launchctl kickstart -k "$DOMAIN/$BUNDLE_ID"
  echo "bx menu bar app is installed and running."
}

uninstall_menu() {
  ensure_macos
  echo "Removing bx menu bar app..."
  bootout_agent
  rm -f "$AGENT_DST"
  rm -rf "$APP_DST"
  echo "bx menu bar app removed."
}

restart_menu() {
  ensure_macos
  if [[ ! -f "$AGENT_DST" ]]; then
    echo "LaunchAgent is not installed. Run: scripts/install-macos-menu.sh install" >&2
    exit 1
  fi
  bootout_agent
  launchctl bootstrap "$DOMAIN" "$AGENT_DST"
  launchctl kickstart -k "$DOMAIN/$BUNDLE_ID"
  echo "bx menu bar app restarted."
}

status_menu() {
  ensure_macos
  if [[ -d "$APP_DST" ]]; then
    echo "app: installed at $APP_DST"
  else
    echo "app: not installed"
  fi
  if [[ -f "$AGENT_DST" ]]; then
    echo "login item: installed at $AGENT_DST"
  else
    echo "login item: not installed"
  fi
  launchctl print "$DOMAIN/$BUNDLE_ID" >/dev/null 2>&1 && echo "state: running" || echo "state: not running"
}

write_launch_agent() {
  local dst="$1"
  local app="$2"
  cat > "$dst" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$BUNDLE_ID</string>
  <key>ProgramArguments</key>
  <array>
    <string>$app/Contents/MacOS/BxMenu</string>
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
}

main() {
  local action="${1:-install}"
  case "$action" in
    install) install_menu ;;
    uninstall) uninstall_menu ;;
    restart) restart_menu ;;
    status) status_menu ;;
    -h|--help|help) usage ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
