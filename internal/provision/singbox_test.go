package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// 内嵌字节存在时:不联网,直接落盘内嵌的 sing-box(默认路径,根除自举悖论)。
func TestEnsureSingboxPrefersEmbedded(t *testing.T) {
	embedded := []byte("#!/bin/sh\necho embedded-singbox\n")
	dir := t.TempDir()
	// url 故意给个连不通的地址:内嵌优先就绝不该去碰它。
	p, err := EnsureSingbox(dir, "", embedded, "v1.13.14", "http://127.0.0.1:1/nope", "")
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(embedded) {
		t.Fatalf("content mismatch: want embedded bytes, got %q", got)
	}
}

// 同版本同字节:第二次复用落盘文件(命中缓存)。
func TestEnsureSingboxEmbeddedSameBytesCached(t *testing.T) {
	embedded := []byte("embedded-v1\n")
	dir := t.TempDir()
	if _, err := EnsureSingbox(dir, "", embedded, "v1.13.14", "", ""); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	p, err := EnsureSingbox(dir, "", embedded, "v1.13.14", "", "")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(embedded) {
		t.Fatalf("同字节应仍是原内容: %q", got)
	}
}

// 同版本【不同字节】(如重嵌换 build tag):content-hash 缓存键应失效旧缓存、刷新成新字节。
func TestEnsureSingboxSameVersionDifferentBytesRefreshes(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureSingbox(dir, "", []byte("OLD-utls-only\n"), "v1.13.14", "", ""); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	newBytes := []byte("NEW-with-quic\n")
	p, err := EnsureSingbox(dir, "", newBytes, "v1.13.14", "", "") // 同版本,不同字节
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(newBytes) {
		t.Fatalf("同版本不同字节应刷新成新内容,got %q", got)
	}
}

// override(本地指定路径)优先级最高,压过内嵌。
func TestEnsureSingboxOverrideBeatsEmbedded(t *testing.T) {
	f := t.TempDir() + "/mybin"
	os.WriteFile(f, []byte("x"), 0o755)
	p, err := EnsureSingbox(t.TempDir(), f, []byte("embedded"), "v1", "", "")
	if err != nil || p != f {
		t.Fatalf("override should win: p=%q err=%v", p, err)
	}
}

// 无内嵌(arch 不支持)时回落到下载 + SHA-256 校验。
func TestEnsureSingboxDownloadsAndVerifies(t *testing.T) {
	payload := []byte("#!/bin/sh\necho fake-singbox\n")
	sum := sha256.Sum256(payload)
	hexsum := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	p, err := EnsureSingbox(dir, "", nil, "", srv.URL, hexsum)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(payload) {
		t.Fatalf("content mismatch")
	}
	// second call uses cache (server can be down): close server, call again
	srv.Close()
	if _, err := EnsureSingbox(dir, "", nil, "", srv.URL, hexsum); err != nil {
		t.Fatalf("cached ensure failed: %v", err)
	}
}

func TestEnsureSingboxRejectsBadHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	if _, err := EnsureSingbox(t.TempDir(), "", nil, "", srv.URL, "deadbeef"); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
}

func TestEnsureSingboxOverride(t *testing.T) {
	f := t.TempDir() + "/mybin"
	os.WriteFile(f, []byte("x"), 0o755)
	p, err := EnsureSingbox(t.TempDir(), f, nil, "", "", "")
	if err != nil || p != f {
		t.Fatalf("override p=%q err=%v", p, err)
	}
}

func TestEnsureSingboxNoSource(t *testing.T) {
	if _, err := EnsureSingbox(t.TempDir(), "", nil, "", "", ""); err == nil {
		t.Fatal("expected error when neither embedded, override nor url given")
	}
}
