# BxMenu

Small macOS menu bar companion for bx.

It is intentionally not a control panel. It shows whether bx is protected, off, not set up, or needs attention, and exposes a few actions:

- Open Status
- View Logs
- Run Doctor
- Set Up bx
- Start bx
- Restart bx
- Turn Off

It does not install, configure, start, or stop the bx network service by itself unless you choose one of the explicit menu actions.

When the menu shows `Setup Required`, choose `Set Up bx...`, paste your bx link, and approve the macOS administrator prompt. After setup succeeds, the menu asks whether to start bx now. If setup fails, use `Run Doctor` from the same menu.

If the menu shows `Update Required`, update the CLI used by the menu bar:

```bash
sudo install -m 0755 ./bx /usr/local/bin/bx
scripts/install-macos-menu.sh restart
```

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

Package a full macOS release:

```bash
scripts/package-macos-release.sh
scripts/verify-macos-release.sh
```

The release folder includes `bx`, `Bx.app`, `install.sh`, `uninstall.sh`, `README.txt`, and a top-level `SHA256SUMS`.

Start at login:

```bash
scripts/install-macos-menu.sh install
```

The installer:

- packages `Bx.app`
- installs it to `~/Applications/Bx.app`
- installs a user LaunchAgent at `~/Library/LaunchAgents/com.getbx.bx.menu.plist`
- writes menu logs under `~/Library/Logs/bx/`
- starts the menu bar app
- does not change bx DNS, routes, service state, or client config

Manage the installed app:

```bash
scripts/install-macos-menu.sh status
scripts/install-macos-menu.sh restart
scripts/install-macos-menu.sh uninstall
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
/usr/local/bin/bx doctor --json --skip-probe
/usr/local/bin/bx dns status
```

Run Doctor archives diagnostics under:

```text
~/Library/Logs/bx/diagnostics
```

The menu sets `BX_LOG_ARCHIVE_DIR` for that action, so diagnostics stay in the user's log folder instead of requiring system log write access.

The menu bar app itself writes raw logs under:

```text
~/Library/Logs/bx/menu.log
~/Library/Logs/bx/menu.err.log
```

Install bx first with `sudo bx setup <client-link>`.
When updating from a local build, also update the CLI used by the menu bar:

```bash
sudo install -m 0755 ./bx /usr/local/bin/bx
```
