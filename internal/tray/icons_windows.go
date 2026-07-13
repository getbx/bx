//go:build windows

package tray

import _ "embed"

//go:embed icons/protected.ico
var iconProtected []byte

//go:embed icons/off.ico
var iconOff []byte

//go:embed icons/attention.ico
var iconAttention []byte

// iconFor 按托盘态选图标字节:protected→绿、attention→红,其余(off/notSetup/notInstalled)→灰。
func iconFor(s TrayState) []byte {
	switch s {
	case StateProtected:
		return iconProtected
	case StateAttention:
		return iconAttention
	default:
		return iconOff
	}
}
