package supervisor

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
)

type fakeControlEngine struct {
	commitErr   error
	rollbackErr error
	armErr      error
	state       confirm.State
	armed       bool
	applied     bool
}

func (f *fakeControlEngine) Commit() error        { return f.commitErr }
func (f *fakeControlEngine) Rollback() error      { return f.rollbackErr }
func (f *fakeControlEngine) State() confirm.State { return f.state }
func (f *fakeControlEngine) Arm(apply, undo func() error) error {
	if f.armErr != nil {
		return f.armErr
	}
	if apply != nil {
		_ = apply()
		f.applied = true
	}
	f.armed = true
	return nil
}

func testMux(eng controlEngine) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, nopMutator{}, nil, nil, 0)
}

type fakeMutator struct {
	gotLink   string
	setErr    error
	setCalled bool
	rehCalled bool
}

func (f *fakeMutator) SetTransport(link string) (func() error, func() error, error) {
	f.setCalled = true
	f.gotLink = link
	if f.setErr != nil {
		return nil, nil, f.setErr
	}
	return func() error { return nil }, func() error { return nil }, nil
}
func (f *fakeMutator) Rehijack() (func() error, func() error, error) {
	f.rehCalled = true
	return func() error { return nil }, func() error { return nil }, nil
}

func testMuxMut(eng controlEngine, mut mutator) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, mut, nil, nil, 0)
}

func testMuxKick(eng controlEngine, kick func() error) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, nopMutator{}, kick, nil, 0)
}

func testMuxReload(eng controlEngine, reload func() error) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, nopMutator{}, nil, reload, 0)
}

func mustPost(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestControlStatus(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateArmed}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v0/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status code=%d", resp.StatusCode)
	}
	var rep stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.Server != "test-node" {
		t.Fatalf("got %+v", rep)
	}
	if rep.MutationState != "armed" {
		t.Fatalf("mutation_state=%q want armed", rep.MutationState)
	}
}

func TestControlCommitOK(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateCommitted}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/commit")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("commit ok 应 200,得 %d", resp.StatusCode)
	}
}

func TestControlCommitNotArmed(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{commitErr: confirm.ErrNotArmed}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/commit")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("nothing to commit 应 409,得 %d", resp.StatusCode)
	}
}

func TestControlRollbackOK(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateReverted}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/rollback")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("rollback ok 应 200,得 %d", resp.StatusCode)
	}
}

func TestControlRollbackError(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{rollbackErr: errors.New("回滚也失败")}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/rollback")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("rollback 出错应 500,得 %d", resp.StatusCode)
	}
}

func TestControlStatusRejectsPost(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{}))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status POST 应 405,得 %d", resp.StatusCode)
	}
}

func TestControlKickInvokesKick(t *testing.T) {
	called := make(chan struct{}, 1)
	kick := func() error { called <- struct{}{}; return nil }
	srv := httptest.NewServer(testMuxKick(&fakeControlEngine{}, kick))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/kick")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("kick 应 200,得 %d", resp.StatusCode)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("kick 未被调用")
	}
}

func TestControlKickNotImplementedWhenNil(t *testing.T) {
	srv := httptest.NewServer(testMuxKick(&fakeControlEngine{}, nil))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/kick")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("kick=nil 应 501,得 %d", resp.StatusCode)
	}
}

func TestControlKickRejectsGet(t *testing.T) {
	srv := httptest.NewServer(testMuxKick(&fakeControlEngine{}, func() error { return nil }))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v0/kick")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("kick GET 应 405,得 %d", resp.StatusCode)
	}
}

func TestControlReloadInvokesReload(t *testing.T) {
	called := false
	srv := httptest.NewServer(testMuxReload(&fakeControlEngine{}, func() error { called = true; return nil }))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reload")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload 应 200,得 %d", resp.StatusCode)
	}
	if !called {
		t.Fatal("reload 未被调用")
	}
}

func TestControlReloadPropagatesError(t *testing.T) {
	srv := httptest.NewServer(testMuxReload(&fakeControlEngine{}, func() error { return errors.New("解析失败") }))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reload")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("reload 出错应 500,得 %d", resp.StatusCode)
	}
}

func TestControlReloadNotImplementedWhenNil(t *testing.T) {
	srv := httptest.NewServer(testMuxReload(&fakeControlEngine{}, nil))
	defer srv.Close()
	resp := mustPost(t, srv.URL+"/v0/reload")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("reload=nil 应 501,得 %d", resp.StatusCode)
	}
}

func TestRequireControlSocketPropagatesStartError(t *testing.T) {
	want := errors.New("bind failed")
	_, err := requireControlSocket(func() (io.Closer, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("want wrapped start error %v, got %v", want, err)
	}
}

func TestControlSetTransportArmed(t *testing.T) {
	mut := &fakeMutator{}
	eng := &fakeControlEngine{}
	srv := httptest.NewServer(testMuxMut(eng, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json",
		strings.NewReader(`{"link":"vless://x@h:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("armed 应 200,得 %d", resp.StatusCode)
	}
	if !mut.setCalled || mut.gotLink != "vless://x@h:443" || !eng.armed {
		t.Fatalf("应调 mut.SetTransport(link) 且 engine.Arm;mut=%+v armed=%v", mut, eng.armed)
	}
}

func TestControlSetTransportEmptyLink(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{}, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json", strings.NewReader(`{"link":""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 link 应 400,得 %d", resp.StatusCode)
	}
	if mut.setCalled {
		t.Fatal("空 link 不应调 mut")
	}
}

func TestControlSetTransportAlreadyArmed(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{state: confirm.StateArmed}, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json", strings.NewReader(`{"link":"vless://x@h:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("已 armed 应 409,得 %d", resp.StatusCode)
	}
	if mut.setCalled {
		t.Fatal("已 armed 不应调用 mutator")
	}
}

func TestControlRehijackArmed(t *testing.T) {
	mut := &fakeMutator{}
	eng := &fakeControlEngine{}
	srv := httptest.NewServer(testMuxMut(eng, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/rehijack", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !mut.rehCalled || !eng.armed {
		t.Fatalf("rehijack 应 200 + mut.Rehijack + Arm;code=%d mut=%+v armed=%v", resp.StatusCode, mut, eng.armed)
	}
}
