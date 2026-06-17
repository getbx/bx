//go:build darwin && amd64

package embedded

import _ "embed"

//go:embed assets/brook_darwin_amd64
var brook []byte
