//go:build windows && amd64

package embedded

import _ "embed"

//go:embed assets/wintun_windows_amd64.dll
var wintun []byte
