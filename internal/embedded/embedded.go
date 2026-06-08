// Package embedded 内嵌 brook 二进制与 china 分流列表快照,使 bx 成为零外部依赖的单文件。
// assets/ 由 .github/workflows/embed-brook.yml 跟随上游 txthinking/brook release 自动刷新。
package embedded

import (
	_ "embed"
	"strings"
)

//go:embed assets/brook_linux_amd64
var brook []byte

//go:embed assets/china_domain.txt
var chinaDomain []byte

//go:embed assets/china_cidr4.txt
var chinaCIDR []byte

//go:embed assets/BROOK_VERSION
var brookVersion string

// Brook 返回内嵌的 brook linux/amd64 二进制字节(只读,调用方不得修改返回的切片)。
func Brook() []byte { return brook }

// ChinaDomain 返回内嵌的 china 域名列表快照(只读,调用方不得修改返回的切片)。
func ChinaDomain() []byte { return chinaDomain }

// ChinaCIDR 返回内嵌的 china IP 段快照(只读,调用方不得修改返回的切片)。
func ChinaCIDR() []byte { return chinaCIDR }

// BrookVersion 返回内嵌 brook 的版本(上游 release tag)。
func BrookVersion() string { return strings.TrimSpace(brookVersion) }
