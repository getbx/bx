//go:build windows

package tray

import _ "embed"

//go:embed icons/protected.ico
var iconProtected []byte

//go:embed icons/warning.ico
var iconWarning []byte

//go:embed icons/failed.ico
var iconFailed []byte

//go:embed icons/off.ico
var iconOff []byte

// iconFor 按托盘态选图标字节:protected→绿、warning→琥珀、attention→红(failed),其余→灰(off)。
func iconFor(s TrayState) []byte {
	switch s {
	case StateProtected:
		return iconProtected
	case StateWarning:
		return iconWarning
	case StateAttention:
		return iconFailed
	default:
		return iconOff
	}
}
