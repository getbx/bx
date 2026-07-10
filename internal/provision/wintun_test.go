package provision

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWintunWritesAndPins(t *testing.T) {
	dir := t.TempDir()
	p, err := EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "wintun.dll") {
		t.Fatalf("路径不对: %q", p)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "WINTUNv1" {
		t.Fatalf("内容不对: %q", b)
	}
}

func TestEnsureWintunSkipsWhenVersionMatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "wintun.dll"), []byte("SENTINEL"), 0o644)
	if _, err := EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "wintun.dll"))
	if string(b) != "SENTINEL" {
		t.Fatalf("版本一致不应重写, got %q", b)
	}
}

func TestEnsureWintunReExtractsOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	_, _ = EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1")
	if _, err := EnsureWintun(dir, []byte("WINTUNv2"), "0.14.2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "wintun.dll"))
	if string(b) != "WINTUNv2" {
		t.Fatalf("版本变更应重写, got %q", b)
	}
}

// 无内嵌(nil bytes):返回空路径 + nil,不写文件(靠系统已装 wintun.dll)。
func TestEnsureWintunNoEmbedNoop(t *testing.T) {
	dir := t.TempDir()
	p, err := EnsureWintun(dir, nil, "")
	if err != nil {
		t.Fatalf("nil bytes 不应报错: %v", err)
	}
	if p != "" {
		t.Fatalf("nil bytes 应返回空路径, got %q", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "wintun.dll")); err == nil {
		t.Fatal("nil bytes 不应写出 wintun.dll")
	}
}
