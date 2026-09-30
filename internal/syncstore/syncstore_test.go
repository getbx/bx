package syncstore

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const id = "0123456789abcdef0123456789abcdef"

func do(t *testing.T, h http.Handler, method, path string, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out, _ := io.ReadAll(rec.Body)
	return rec.Code, out
}

func TestStoreKeepsOneOpaqueBlobPerID(t *testing.T) {
	dir := t.TempDir()
	h := Handler(dir)
	if code, _ := do(t, h, http.MethodGet, "/v1/blob/"+id, nil); code != http.StatusNotFound {
		t.Fatalf("empty store GET = %d, want 404 (the phone says 'not synced yet')", code)
	}
	blob := []byte("bxps1\x00opaque-ciphertext")
	if code, _ := do(t, h, http.MethodPut, "/v1/blob/"+id, blob); code != http.StatusNoContent {
		t.Fatalf("PUT = %d", code)
	}
	code, got := do(t, h, http.MethodGet, "/v1/blob/"+id, nil)
	if code != http.StatusOK || !bytes.Equal(got, blob) {
		t.Fatalf("GET = %d %q", code, got)
	}
	info, err := os.Stat(filepath.Join(dir, id))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("blob file mode = %v, err %v; want 0600", info.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("temp file left behind: %s (writes must be atomic)", e.Name())
		}
	}
}

// 路径就是文件名:id 只许 32 位小写十六进制,否则 ../ 之类会写到目录外面去。
func TestStoreRefusesAnythingButAHexID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sync")
	h := Handler(dir)
	for _, bad := range []string{"../../etc/passwd", "../escape", "ABCDEF0123456789abcdef0123456789", "short", id + "0", id + "/x"} {
		if code, _ := do(t, h, http.MethodPut, "/v1/blob/"+bad, []byte("x")); code < 300 {
			t.Errorf("PUT %q = %d, want it refused", bad, code)
		}
	}
	// Whatever the router answers (400, 404, or a 307 to a cleaned path), nothing is written.
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			t.Errorf("a refused PUT wrote %s", path)
		}
		return nil
	})
	if code, _ := do(t, h, http.MethodDelete, "/v1/blob/"+id, nil); code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want 405", code)
	}
}

func TestStoreCapsTheBlobSize(t *testing.T) {
	h := Handler(t.TempDir())
	if code, _ := do(t, h, http.MethodPut, "/v1/blob/"+id, bytes.Repeat([]byte("a"), MaxBlob+1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized PUT = %d, want 413", code)
	}
	if code, _ := do(t, h, http.MethodPut, "/v1/blob/"+id, nil); code != http.StatusBadRequest {
		t.Fatalf("empty PUT = %d, want 400", code)
	}
}
