//go:build darwin && arm64

package embedded

import _ "embed"

//go:embed assets/brook_darwin_arm64
var brook []byte
