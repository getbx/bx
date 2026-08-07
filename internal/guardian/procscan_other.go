//go:build !darwin

package guardian

import "errors"

// errCoreScanUnsupported 让非 darwin 平台一律 fail-closed:
// 这套标记机制在实践中只有 macOS 用得到(Guardian 只有 darwin 实体,
// `daemon.go` 里 `requireDaemonPlatform()` 在构造 ExecCoreRunner 之前就挡住了
// 别的平台),在别处放宽没有收益,只有风险。
//
// **移植警告**:这个桩不再是「不改行为」。fork 前的三条路——launching 标记、
// 无记录——现在都要向系统求证,而本桩恒返回错误 = 恒 uncertain,于是
// **`Start` 在无记录时也会拒绝,即 `bx up` 永远起不来 Core**。
// 谁要把 Guardian 移植到新平台,必须先实现 scanRunningCores,不能只放开
// requireDaemonPlatform。
var errCoreScanUnsupported = errors.New("core process scan is only implemented on darwin")

func scanRunningCores() ([]Process, error) { return nil, errCoreScanUnsupported }
