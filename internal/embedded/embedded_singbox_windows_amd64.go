//go:build windows && amd64

package embedded

import _ "embed"

//go:embed assets/singbox_windows_amd64
var singbox []byte
