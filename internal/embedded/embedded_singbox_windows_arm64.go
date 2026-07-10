//go:build windows && arm64

package embedded

import _ "embed"

//go:embed assets/singbox_windows_arm64
var singbox []byte
