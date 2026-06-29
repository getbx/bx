//go:build darwin && arm64

package embedded

import _ "embed"

//go:embed assets/singbox_darwin_arm64
var singbox []byte
