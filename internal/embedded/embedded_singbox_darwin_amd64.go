//go:build darwin && amd64

package embedded

import _ "embed"

//go:embed assets/singbox_darwin_amd64
var singbox []byte
