//go:build linux && arm64

package embedded

import _ "embed"

//go:embed assets/singbox_linux_arm64
var singbox []byte
