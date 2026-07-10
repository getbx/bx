package embedded

import (
	"runtime"
	"testing"
)

func TestAssetsPresent(t *testing.T) {
	// brook 在 linux/darwin/windows 的 amd64/arm64 均已内嵌;其余 GOOS/GOARCH 组合
	// 为 nil、走 provision.EnsureBrook 的下载/override 兜底(embedded_other.go)。
	brookEmbedded := (runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows") &&
		(runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
	if brookEmbedded && len(Brook()) == 0 {
		t.Error("brook 资产为空")
	}
	if len(ChinaDomain()) == 0 {
		t.Error("china_domain 资产为空")
	}
	if len(ChinaCIDR()) == 0 {
		t.Error("china_cidr 资产为空")
	}
	if BrookVersion() == "" {
		t.Error("BROOK_VERSION 为空")
	}
}

func TestWintunEmbeddedOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" && len(Wintun()) == 0 {
		t.Fatal("windows 构建应内嵌 wintun.dll")
	}
	if runtime.GOOS != "windows" && len(Wintun()) != 0 {
		t.Fatal("非 windows 不应有内嵌 wintun")
	}
}

func TestBrookEmbeddedOnWindows(t *testing.T) {
	// windows amd64/arm64 内嵌官方 brook exe(brook 传输免下载),同 linux/darwin 覆盖。
	if runtime.GOOS == "windows" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		b := Brook()
		if len(b) == 0 {
			t.Fatal("windows amd64/arm64 构建应内嵌 brook")
		}
		if len(b) < 2 || b[0] != 'M' || b[1] != 'Z' {
			t.Fatalf("brook 资产非 windows PE,前 2 字节=%x", b[:min(2, len(b))])
		}
	}
}

func TestSingboxEmbeddedOnWindows(t *testing.T) {
	// windows amd64/arm64 自建静态 sing-box 已内嵌(with_utls,with_quic),reality/hysteria2 免下载。
	if runtime.GOOS == "windows" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		b := Singbox()
		if len(b) == 0 {
			t.Fatal("windows amd64/arm64 构建应内嵌 sing-box")
		}
		if len(b) < 2 || b[0] != 'M' || b[1] != 'Z' {
			t.Fatalf("singbox 资产非 windows PE,前 2 字节=%x", b[:min(2, len(b))])
		}
		if SingboxVersion() == "" {
			t.Error("SINGBOX_VERSION 为空")
		}
	}
}
