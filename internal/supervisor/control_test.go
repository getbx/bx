package supervisor

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
)

type fakeControlEngine struct {
	commitErr   error
	rollbackErr error
	state       confirm.State
}

func (f *fakeControlEngine) Commit() error        { return f.commitErr }
func (f *fakeControlEngine) Rollback() error      { return f.rollbackErr }
func (f *fakeControlEngine) State() confirm.State { return f.state }

func testMux(eng controlEngine) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} })
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

func TestRequireControlSocketPropagatesStartError(t *testing.T) {
	want := errors.New("bind failed")
	_, err := requireControlSocket(func() (io.Closer, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("want wrapped start error %v, got %v", want, err)
	}
}
