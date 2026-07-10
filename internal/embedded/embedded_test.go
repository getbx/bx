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
