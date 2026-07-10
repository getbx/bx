//go:build windows && arm64

package embedded

import _ "embed"

//go:embed assets/brook_windows_arm64
var brook []byte
