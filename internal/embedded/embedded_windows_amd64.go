//go:build windows && amd64

package embedded

import _ "embed"

//go:embed assets/brook_windows_amd64
var brook []byte
