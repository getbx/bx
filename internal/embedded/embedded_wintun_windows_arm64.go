//go:build windows && arm64

package embedded

import _ "embed"

//go:embed assets/wintun_windows_arm64.dll
var wintun []byte
