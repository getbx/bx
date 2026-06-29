//go:build linux && (amd64 || arm64)

package embedded

import (
	"bytes"
	"testing"
)

// 守卫:linux amd64/arm64 必须真嵌进 sing-box,且是 ELF(防 CI 重嵌嵌空/嵌坏/嵌错类型)。
func TestSingboxEmbedded(t *testing.T) {
	b := Singbox()
	if len(b) == 0 {
		t.Fatal("singbox 资产为空(应内嵌真二进制)")
	}
	if !bytes.HasPrefix(b, []byte{0x7f, 'E', 'L', 'F'}) {
		t.Fatalf("singbox 资产非 ELF,前 4 字节=%x", b[:4])
	}
	if SingboxVersion() == "" {
		t.Error("SINGBOX_VERSION 为空")
	}
}
