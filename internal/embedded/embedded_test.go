package embedded

import (
	"runtime"
	"testing"
)

func TestAssetsPresent(t *testing.T) {
	// brook 只在 linux/darwin(amd64/arm64)内嵌;windows/其他平台为 nil、走下载兜底。
	if runtime.GOOS != "windows" && len(Brook()) == 0 {
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
