# Safe Reconnect Design

## Goal

Make a user-initiated bx reconnect fail closed on macOS: it must not restore
system DNS, remove TUN routes, stop the bx service, or expose application
traffic to the physical network while a replacement transport is connecting.

## Scope

This design covers a running bx daemon and a user request to reconnect the
transport. It does not claim protection against a forced daemon kill, kernel
panic, power loss, or binary replacement during an update. Those cases require
a separate persistent macOS kill-switch design.

## Chosen Model

bx already has the right primitive in `transportSwapper.swapTo(link)`:

1. Start a fresh transport instance while the current transport remains in
   place.
2. Wait for the fresh instance to pass its health check.
3. Atomically point new dialer connections at the fresh SOCKS endpoint.
4. Stop the old transport only after the swap.

The reconnect operation calls that primitive with the currently active link. It
does not tear down the daemon, TUN device, route hijack, DNS listener, or
system DNS configuration.

If the old transport is unhealthy, existing TUN/routes still direct new flows
to bx. The dialer's `Healthy` check rejects those flows until a healthy
replacement has been swapped in. The observable result is a brief connection
failure, never a direct fallback.

If a replacement cannot become healthy, bx keeps the existing transport
instead of stopping it. The request returns an error and no system network
state changes.

## Interface

- Add `sudo bx reconnect` as the explicit safe transport-reconnect command.
- Change `sudo bx restart` to use the same safe reconnect path for
  compatibility with existing user expectation.
- Change the macOS menu action from `bx down && bx up` to `bx reconnect`.
  Rename it to `Reconnect` because protection itself remains running.
- Expose a local control endpoint dedicated to reconnecting the current link.
  It must return only after the replacement is healthy and atomically active,
  or return an error without changing the active transport.

## Authorization

The reconnect endpoint changes runtime behavior, so it remains a privileged
local operation. On macOS, implement Unix-domain peer credentials with
`LOCAL_PEERCRED` / `GetsockoptXucred`; authorize root and the configured bx
owner. On platforms where peer credentials cannot be obtained, reject the
request rather than widening socket permissions.

The control socket may remain readable for status. Its mutating reconnect route
must continue to require authenticated peer identity.

## State and User Experience

- During a successful reconnect, the menu stays in a yellow `Reconnecting
  safely` state until the fresh transport is healthy and swapped.
- On success it returns to green and shows the new latency.
- On failure it stays yellow or returns to its prior healthy state when the
  old transport remained usable; the menu presents the error and points to
  logs.
- The action must not call `bx down`, `bx up`, `launchctl bootout`, or
  `launchctl kickstart`.

## Validation

- Unit tests prove reconnect starts and health-checks a new transport before
  changing the dialer's active transport, and retains the old transport on
  failure.
- Control tests prove unauthenticated mutation is rejected and an authorized
  reconnect invokes the safe swap path.
- macOS tests cover `LOCAL_PEERCRED` authorization through a real Unix socket.
- A macOS smoke test records routes and system DNS before, during, and after a
  reconnect; it verifies the split-default routes and DNS ownership remain
  present, while a concurrent egress attempt is either proxied or fails and
  never succeeds through the physical default route.
- The menu-app test verifies its reconnect action invokes `bx reconnect`, not
  a shell sequence containing `down` or `up`.
