# BxMenu

Small macOS menu bar companion for bx.

It is intentionally not a control panel. It shows whether bx is on, healthy, or needs attention, and exposes a few actions:

- Open Status
- View Logs
- Run Doctor
- Restart bx
- Turn Off

Build locally:

```bash
cd apps/macos/BxMenu
swift build -c release
.build/release/BxMenu &
```

Package as a menu bar app from the repository root:

```bash
cd /path/to/bx
scripts/package-macos-menu.sh
ditto dist/macos/Bx.app ~/Applications/Bx.app
open ~/Applications/Bx.app
```

Start at login:

```bash
scripts/install-macos-menu.sh install
```

Manual LaunchAgent install:

```bash
mkdir -p ~/Library/LaunchAgents
cp dist/macos/com.getbx.bx.menu.plist ~/Library/LaunchAgents/
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.getbx.bx.menu.plist
launchctl kickstart -k "gui/$(id -u)/com.getbx.bx.menu"
```

Remove the menu bar companion:

```bash
scripts/install-macos-menu.sh uninstall
```

The installer uses `~/Applications/Bx.app` by default. Override `BX_APP_DST` if you intentionally want another location.

BxMenu reads status through:

```bash
/usr/local/bin/bx status --json
```

Install bx first with `sudo bx setup <client-link>`.
