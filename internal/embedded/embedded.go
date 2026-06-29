// Package embedded 内嵌 brook 二进制与 china 分流列表快照,使 bx 成为零外部依赖的单文件。
// assets/ 由 .github/workflows/embed-brook.yml 跟随上游 txthinking/brook release 自动刷新。
package embedded

import (
	_ "embed"
	"strings"
)

// brook 由系统+架构专属文件按 GOOS/GOARCH 内嵌。
// 目前支持 linux/darwin 的 amd64 与 arm64;其他平台无对应 brook,编译期即报 undefined: brook。

//go:embed assets/china_domain.txt
var chinaDomain []byte

//go:embed assets/china_cidr4.txt
var chinaCIDR []byte

//go:embed assets/BROOK_VERSION
var brookVersion string

//go:embed assets/SINGBOX_VERSION
var singboxVersion string

// singbox 由 GOOS/GOARCH 专属文件按需内嵌:linux amd64/arm64 嵌真二进制(自建静态最小构建,
// 见 embedded_singbox_<arch>.go),其余平台为 nil(reality 回落到下载/override,见 provision.EnsureSingbox)。

// Brook 返回内嵌的、与当前架构匹配的 brook 二进制字节(只读,调用方不得修改返回的切片)。
func Brook() []byte { return brook }

// Singbox 返回内嵌的、与当前架构匹配的 sing-box 二进制字节(REALITY 传输用)。
// linux amd64/arm64 非空;其他平台为 nil(调用方据此回落下载)。只读,不得修改返回的切片。
func Singbox() []byte { return singbox }

// SingboxVersion 返回内嵌 sing-box 的版本(上游 release tag)。
func SingboxVersion() string { return strings.TrimSpace(singboxVersion) }

// ChinaDomain 返回内嵌的 china 域名列表快照(只读,调用方不得修改返回的切片)。
func ChinaDomain() []byte { return chinaDomain }

// ChinaCIDR 返回内嵌的 china IP 段快照(只读,调用方不得修改返回的切片)。
func ChinaCIDR() []byte { return chinaCIDR }

// BrookVersion 返回内嵌 brook 的版本(上游 release tag)。
func BrookVersion() string { return strings.TrimSpace(brookVersion) }
