//go:build linux && amd64

package embedded

import _ "embed"

//go:embed assets/singbox_linux_amd64
var singbox []byte
