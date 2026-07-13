# macOS Status Indicator Design

## Goal

Make bx's current protection state visible from the macOS menu bar without
turning the menu icon into a dashboard or forcing the user to open a menu.

## Visual Model

The existing monochrome shield remains the bx identity. A single static colored
dot sits immediately to its right. The dot is a status signal, not a second
icon, badge count, animation, or throughput indicator.

| Dot | Meaning | User-facing tooltip example |
| --- | --- | --- |
| Green | Protection is healthy: the tunnel is healthy and bx has reached its protected runtime state. | `bx: Protected, 287 ms` |
| Yellow | bx needs attention but is still handling the situation safely, such as reconnecting, an unreadable status response, an extra tunnel warning, or elevated latency. | `bx: Reconnecting safely` |
| Red | Protection is unavailable or failed to start. bx must remain fail-closed rather than falling back to the real network path. | `bx: Protection unavailable` |
| Gray | bx is deliberately off, has not been configured, or is not installed. | `bx: Off` |

The dot does not blink. A transient network fluctuation must not immediately
become red: yellow is the recovery and attention state. Red is reserved for a
state that needs user action.

## State Mapping

- `connected` maps to green.
- `warning` maps to yellow.
- `updateNeeded` maps to yellow because the installed CLI needs attention but
  the menu app can still explain the next step.
- `off`, `setupNeeded`, and `missing` map to gray.
- A future explicit failed-start or fail-closed state maps to red. Until bx
  exposes this distinct runtime state in status JSON, the menu must not invent a
  red condition from ordinary warning text.

## Implementation

- Keep the shield as a template SF Symbol so it follows the macOS menu bar
  appearance.
- Change the status item to variable width and render a small attributed dot as
  its title next to the shield image.
- Set the dot color from the state mapping. The tooltip and menu header remain
  the accessible textual source of the same state.
- Do not add timers, animations, notifications, network probes, or privileged
  operations solely for the indicator. It continues to use the existing
  read-only five-second state refresh.

## Validation

- Unit-test the mapping from each `BxState` category to its dot semantic.
- Build and package the menu app, then inspect it on macOS in light and dark
  menu bar appearances.
- Verify that the shield remains visible, the dot does not resize the menu bar
  during refresh, and the tooltip still states the status in words.
