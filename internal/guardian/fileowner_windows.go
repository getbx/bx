//go:build windows

package guardian

import "os"

func fileOwnerUID(os.FileInfo) (uint32, bool) { return 0, false }
