//go:build amd64

package embedded

import _ "embed"

//go:embed assets/brook_linux_amd64
var brook []byte
