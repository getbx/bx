# BxMenu

Small macOS menu bar app for bx.

It is intentionally not a control panel. On macOS it is the default user surface: it shows whether bx is protected, off, not set up, or needs attention, and exposes only the actions a user normally needs. `Open Status` opens a small native status panel and does not require Terminal access.

Hovering the menu bar icon shows the current bx state.
When protected, `UDP Relay` shows `On`, `Direct`, or `Blocked`.
The shield has a static status dot: green means protected, yellow means bx is
safely recovering or needs attention, red means a future explicit protection
failure, and gray means bx is off or not configured.

- Install bx
- Open Status
- View Logs
- Run Doctor
- Set Up bx
- Start Protection
- Reconnect
- Turn Off bx
- Update bx
- Quit bx

It does not install, configure, start, reconnect, or turn off protection by itself unless you choose one of the explicit menu actions.
Starting protection always asks for confirmation before bx takes over system traffic.
Turning protection off also asks for confirmation and restores managed DNS settings.
`Turn Off bx` stops protection through `bx down` and restores bx-managed DNS, but leaves the menu bar app running. `Quit bx…` does the same and then also closes the menu bar app. `Quit Menu` closes only the menu bar UI; it deliberately leaves protection running.
Use `Turn Off bx` when you want to stop protection but keep the menu bar app open; use `Quit bx…` when you also want to close the app.
When bx needs attention, the primary action is to reconnect. bx keeps TUN, routes, and managed DNS in place while it verifies a replacement transport, so a failed reconnect does not create a direct-traffic window.

When the menu shows `Setup Required`, choose `Set Up bx...`, paste your bx link, and approve the macOS administrator prompt. After setup succeeds, the menu asks whether to start protection now. If setup fails, use `Run Doctor` from the same menu.
If setup, start, reconnect, or turn off fails, the failure dialog offers `Run Doctor` directly so diagnostics can be archived without hunting through the menu again.

`Open Status` stays inside the bx app. The first action that needs Terminal, currently `Run Doctor`, may ask for permission to control Terminal. bx uses this only for the action you selected. Approve the macOS prompt to continue.

At launch and then every 24 hours, the menu checks (read-only) for a signed bx
release in the background and shows `Update bx…` when one is available. On a
unified install (`Bx.app` in `/Applications`, the normal case for release
packages) choosing it currently reports that in-place update isn't supported
yet — download the new `bx-macos-<arch>.tar.gz` and reinstall (`./install.sh`
or the menu's `Install bx…`) instead; that reinstall is idempotent and keeps
`/etc/bx/config.yaml`. A one-click online update through Guardian is planned
for a later phase. Update attempt output is written to:

```text
~/Library/Logs/bx/menu-update.log
```

If the menu shows `Update Required`, the installed CLI is too old to safely
coordinate a full App update. Install the current macOS bx package again:

```bash
./install.sh
```

If the menu shows `Not Installed`, install the macOS bx package again so `/usr/local/bin/bx` and `Bx.app` are installed together.

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

The release folder includes `Bx.app` (with `bx-cli`/`bx-bridge`/`release.json` embedded under `Contents/Resources`), `install.sh`, `uninstall.sh`, `README.txt`, and a top-level `SHA256SUMS`. There is no top-level `bx` binary.

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
- does not start or turn off protection, change DNS, routes, or client config

Manage the installed app:

```bash
scripts/install-macos-menu.sh status
scripts/install-macos-menu.sh restart
scripts/install-macos-menu.sh uninstall
```

Remove the menu bar app:

```bash
scripts/install-macos-menu.sh uninstall
```

The installer uses `~/Applications/Bx.app` by default. Override `BX_APP_DST` if you intentionally want another location.

This dev-only path installs the menu app by itself. The production install (`Bx.app` in `/Applications`, run via `Install bx…` in the menu or the release package's `install.sh`) writes its own LaunchAgent pointing at `/Applications/Bx.app` and self-heals it on launch — you never need to hand-install a plist for that path.

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

The menu runs Doctor in Terminal and may ask for the macOS administrator password so it can read the system client config and service logs. It sets `BX_LOG_ARCHIVE_DIR` for that action, so diagnostics stay in the user's log folder and are opened in Finder after collection.

The menu bar app itself writes raw logs under:

```text
~/Library/Logs/bx/menu.log
~/Library/Logs/bx/menu.err.log
```

`View Logs` opens this folder in Finder even when bx is off, not set up, or the CLI needs repair.

For normal macOS use, install the full release package, then set up and start protection from the menu bar. The CLI path remains available for automation and recovery:

```bash
sudo bx setup '<client-link>'
sudo bx up
```

`/usr/local/bin/bx` is a bridge installed by the unified installer: it resolves and execs the matching CLI under `/Library/Application Support/bx/runtime/current`, so `bx --version` always matches the installed `Bx.app` version. Don't overwrite it by hand with a locally built `./bx` — that bypasses the bridge and desyncs the CLI from the App version. To pick up a local build, package a full release (`scripts/package-macos-release.sh`) and reinstall via `install.sh`.
