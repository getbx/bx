package supervisor

import (
	"errors"

	"github.com/getbx/bx/internal/appattr"
)

// appSource 现问系统「哪个端口属于哪个应用」。**注入点** —— 测试用假的。
//
// 临时文件:下个 task 会把这个接口与哨兵错误并进 apptraffic.go 并删掉本文件,
// 不要提前合并。
type appSource interface {
	// OwnersByPort 现问内核一次,返回 (端口,协议) → 应用显示名。
	// 键必须是 appattr.PortKey 而不是裸 uint16 —— TCP 与 UDP 端口空间相互独立,
	// 同一个数字可能同时被两个协议占用,合并成一个键会让后写入的那个协议静默
	// 覆盖先写入的归因。
	// 查不出应用的端口**不出现在 map 里**(调用方据此判 unknown)。
	OwnersByPort() (map[appattr.PortKey]string, error)
}

var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")
