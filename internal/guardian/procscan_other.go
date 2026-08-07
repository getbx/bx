//go:build !darwin

package guardian

import "errors"

// errCoreScanUnsupported 让非 darwin 平台保持既有的无条件 fail-closed:
// 这套标记机制在实践中只有 macOS 用得到(Guardian 只有 darwin 实体),
// 在别处放宽没有收益,只有风险。
var errCoreScanUnsupported = errors.New("core process scan is only implemented on darwin")

func scanRunningCores() ([]Process, error) { return nil, errCoreScanUnsupported }
