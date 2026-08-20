package supervisor

import "errors"

// appSource 现问系统「哪个端口属于哪个应用」。**注入点** —— 测试用假的。
//
// 临时文件:下个 task 会把这个接口与哨兵错误并进 apptraffic.go 并删掉本文件,
// 不要提前合并。
type appSource interface {
	// OwnersByPort 现问内核一次,返回 源端口 → 应用显示名。
	// 查不出应用的端口**不出现在 map 里**(调用方据此判 unknown)。
	OwnersByPort() (map[uint16]string, error)
}

var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")
