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

BxMenu reads status through:

```bash
/usr/local/bin/bx status --json
```

Install bx first with `sudo bx setup <client-link>`.
