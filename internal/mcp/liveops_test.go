package mcp

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"

	mcpstats "github.com/getbx/bx/internal/stats"
)

func TestStatusOverSocket(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/bx.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(mcpstats.Report{TunnelHealthy: true, LatencyMS: 42})
	})
	go http.Serve(ln, mux) //nolint:errcheck
	rep, err := statusOverSocket(sock)
	if err != nil {
		t.Fatalf("statusOverSocket: %v", err)
	}
	if !rep.TunnelHealthy || rep.LatencyMS != 42 {
		t.Fatalf("got %+v", rep)
	}
}

func TestMutatingRequiresRoot(t *testing.T) {
	// requireRoot 为纯函数:isRoot=false 且 mutating 时返回 PRIVILEGE_REQUIRED。
	if err := requireRoot(false); err == nil {
		t.Fatal("非 root 调改动类应报 PRIVILEGE_REQUIRED")
	} else {
		if te, ok := err.(ToolError); !ok || te.Code != CodePrivilegeRequired {
			t.Fatalf("应为 PRIVILEGE_REQUIRED,得到 %v", err)
		}
	}
	if err := requireRoot(true); err != nil {
		t.Fatalf("root 时不应报错,得到 %v", err)
	}
}
