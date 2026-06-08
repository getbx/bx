package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPGetOKAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte("LIST"))
	}))
	defer srv.Close()
	b, err := httpGet(context.Background(), srv.Client(), srv.URL+"/ok")
	if err != nil || string(b) != "LIST" {
		t.Fatalf("200 应返回 body, got %q err=%v", b, err)
	}
	if _, err := httpGet(context.Background(), srv.Client(), srv.URL+"/bad"); err == nil {
		t.Fatal("非 200 应报错")
	}
}

func TestAtomicWriteFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := atomicWriteFile(p, []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(p, []byte("B")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "B" {
		t.Fatalf("应覆盖为 B, got %q", b)
	}
}

func TestRefreshLoopRunsWhenHealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n int32
	refreshLoop(ctx, time.Millisecond, func() bool { return true }, func() error {
		if atomic.AddInt32(&n, 1) >= 3 {
			cancel()
		}
		return nil
	})
	if atomic.LoadInt32(&n) < 3 {
		t.Fatalf("健康时应反复刷新, got %d", n)
	}
}

func TestRefreshLoopSkipsWhenUnhealthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var n int32
	refreshLoop(ctx, time.Millisecond, func() bool { return false }, func() error {
		atomic.AddInt32(&n, 1)
		return nil
	})
	if atomic.LoadInt32(&n) != 0 {
		t.Fatalf("不健康不应刷新, got %d", n)
	}
}
