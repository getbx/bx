package mcp

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// **接线守卫。**
//
// 上面 mutationoutcome_test.go 那几条测的是判据;这一条测的是判据**真的接到了
// 出口上**。把 liveOps.Commit 改回 `if _, err := supervisor.CommitControl(...)`
// 之后,那几条照样全绿 —— 而那正是这个 bug 的原形:控制面一直知道答案
// (409 带着 State: "reverted"),是 MCP 这一层把它扔了。
//
// 判据打在真进程边界上:起一个真的 unix socket 控制面,让它照生产的样子回
// 409 + State,断言 agent 拿到的是 DEADMAN_REVERTED 而不是 TUNNEL_UNHEALTHY。

// fakeControlPlane 起一个应答 /v0/commit 与 /v0/rollback 的本地控制面。
func fakeControlPlane(t *testing.T, status int, body controlBody) string {
	t.Helper()
	// 不用 t.TempDir():它把测试名嵌进路径,而 unix socket 路径在 macOS 上有
	// ~104 字节上限,长测试名会让 bind 报 invalid argument。
	dir, err := os.MkdirTemp("", "bxmcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "core.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/commit", handler)
	mux.HandleFunc("/v0/rollback", handler)
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close(); ln.Close() })
	return sock
}

// controlBody 复刻 supervisor.controlResponse 的线上形状(那个类型不导出)。
type controlBody struct {
	Status string `json:"status,omitempty"`
	State  string `json:"state,omitempty"`
	Error  string `json:"error,omitempty"`
}

func TestLiveCommitReportsTheDeadmanRevertToTheAgent(t *testing.T) {
	sock := fakeControlPlane(t, http.StatusConflict, controlBody{
		Status: "error", Error: "nothing to commit", State: "reverted",
	})
	ops := &liveOps{coreSock: sock}

	var te ToolError
	if !errors.As(ops.Commit(), &te) {
		t.Fatal("Commit 没有产出结构化错误")
	}
	if te.Code != CodeDeadmanReverted {
		t.Fatalf("agent 拿到的是 %s —— 控制面明明说了 state=reverted,是这一层把它丢了", te.Code)
	}
}

func TestLiveRollbackReportsNothingArmedRatherThanAnUnhealthyTunnel(t *testing.T) {
	sock := fakeControlPlane(t, http.StatusConflict, controlBody{
		Status: "error", Error: "nothing to rollback", State: "idle",
	})
	ops := &liveOps{coreSock: sock}

	var te ToolError
	if !errors.As(ops.Rollback(), &te) {
		t.Fatal("Rollback 没有产出结构化错误")
	}
	if te.Code != CodeNothingToRollback {
		t.Fatalf("agent 拿到的是 %s,应当是 %s", te.Code, CodeNothingToRollback)
	}
}

// 成功路径不许因为这次改动变成失败 —— 200 + state=committed 是正常的确认。
func TestLiveCommitStaysSuccessfulOnTheHappyPath(t *testing.T) {
	sock := fakeControlPlane(t, http.StatusOK, controlBody{Status: "committed", State: "committed"})
	if err := (&liveOps{coreSock: sock}).Commit(); err != nil {
		t.Fatalf("正常确认被报成了失败:%v", err)
	}
}
