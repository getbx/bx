package guardian

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/policysync"
	"github.com/getbx/bx/internal/syncstore"
)

const pushLink = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"

type pushEnv struct {
	pusher    *policyPusher
	cfgPath   string
	storeDir  string
	puts      int
	mu        sync.Mutex
	protected bool
	writes    int
}

func newPushEnv(t *testing.T) *pushEnv {
	t.Helper()
	env := &pushEnv{protected: true, storeDir: t.TempDir()}
	store := syncstore.Handler(env.storeDir)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			env.mu.Lock()
			env.puts++
			env.mu.Unlock()
		}
		store.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	env.cfgPath = filepath.Join(dir, "config.yaml")
	env.writeConfig(t, "rules:\n  - direct: ['*.apple.com']\n")
	env.pusher = &policyPusher{
		configPath: env.cfgPath,
		statePath:  filepath.Join(dir, "policy-sync.json"),
		protected:  func() bool { return env.protected },
		// Stands in for "through the tunnel's socks port to 127.0.0.1:<StorePort>": every dial
		// lands on the test store, whatever address the pusher asks for.
		dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
		},
		now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	}
	return env
}

func (e *pushEnv) writeConfig(t *testing.T, rules string) {
	t.Helper()
	if err := os.WriteFile(e.cfgPath, []byte("server: "+pushLink+"\n"+rules), 0o600); err != nil {
		t.Fatal(err)
	}
	// Move the modification time explicitly: on a filesystem with 1s timestamps two writes in the
	// same second would look unchanged to the pusher's stat check.
	e.writes++
	stamp := time.Unix(1_700_000_000+int64(e.writes)*10, 0)
	if err := os.Chtimes(e.cfgPath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func (e *pushEnv) stored(t *testing.T) policysync.Policy {
	t.Helper()
	keys, _ := policysync.Derive(pushLink)
	raw, err := os.ReadFile(filepath.Join(e.storeDir, keys.BlobID))
	if err != nil {
		t.Fatalf("nothing stored: %v", err)
	}
	p, err := policysync.Open(keys, raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPusherStoresTheRulesOnceAndAgainOnlyWhenTheyChange(t *testing.T) {
	env := newPushEnv(t)
	ctx := context.Background()
	if err := env.pusher.pushIfChanged(ctx); err != nil {
		t.Fatal(err)
	}
	if p := env.stored(t); len(p.Direct) != 1 || p.Direct[0] != "*.apple.com" || p.Version == 0 {
		t.Fatalf("stored %+v", p)
	}
	_ = env.pusher.pushIfChanged(ctx)
	if env.puts != 1 {
		t.Fatalf("unchanged rules were pushed again (%d PUTs): a 30-minute check must cost no traffic", env.puts)
	}
	env.writeConfig(t, "global: true\nrules:\n  - direct: ['*.apple.com']\n    proxy: ['x.com']\n")
	if err := env.pusher.pushIfChanged(ctx); err != nil {
		t.Fatal(err)
	}
	if p := env.stored(t); env.puts != 2 || !p.Global || len(p.Proxy) != 1 {
		t.Fatalf("changed rules: puts=%d stored %+v", env.puts, p)
	}
}

// Only through the tunnel: while protection is off there is no tunnel to go through, and going
// around it would put a request to the VPS outside the tunnel.
func TestPusherSendsNothingWhileProtectionIsOff(t *testing.T) {
	env := newPushEnv(t)
	env.protected = false
	if err := env.pusher.pushIfChanged(context.Background()); err != nil {
		t.Fatal(err)
	}
	if env.puts != 0 {
		t.Fatal("pushed while protection was off")
	}
}

// A failed push must be retried: nothing is recorded as synced until the store said so.
func TestPusherRetriesAfterAFailedPush(t *testing.T) {
	env := newPushEnv(t)
	real := env.pusher.dial
	env.pusher.dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("store unreachable") }
	if err := env.pusher.pushIfChanged(context.Background()); err == nil || !strings.Contains(err.Error(), "enable-sync") {
		t.Fatalf("unreachable store: err = %v, want a pointer to bx server enable-sync", err)
	}
	env.pusher.dial = real
	env.pusher.now = func() time.Time { return time.Unix(1_800_000_000+120, 0) } // past the first backoff
	if err := env.pusher.pushIfChanged(context.Background()); err != nil || env.puts != 1 {
		t.Fatalf("retry: err=%v puts=%d", err, env.puts)
	}
}

// A server without a sync store is the normal case for everyone who never set up the phone. Retrying
// every minute forever meant a connection through the tunnel and a failure line in the Guardian log
// every minute (~1,440 lines a day). Back off (1m, 2m, 4m … up to 1h), dial nothing while waiting,
// log a failure only when it first appears or changes, and try at once again when protection comes
// back on.
func TestAFailingStoreIsRetriedWithBackoffAndLoggedOnce(t *testing.T) {
	env := newPushEnv(t)
	clock := int64(1_800_000_000)
	env.pusher.now = func() time.Time { return time.Unix(clock, 0) }
	dials := 0
	env.pusher.dial = func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("connection refused")
	}
	ctx := context.Background()
	if err := env.pusher.pushIfChanged(ctx); err == nil {
		t.Fatal("the first failure must be reported (logged)")
	}
	first := dials
	clock += 30 // inside the 1-minute backoff
	if err := env.pusher.pushIfChanged(ctx); err != nil || dials != first {
		t.Fatalf("dialed during backoff (dials %d→%d, err %v)", first, dials, err)
	}
	clock += 31 // backoff over: retry, same failure → not logged again
	if err := env.pusher.pushIfChanged(ctx); err != nil || dials == first {
		t.Fatalf("retry after backoff: dials=%d err=%v (a repeat of the same failure is not logged again)", dials, err)
	}
	if got := env.pusher.retryAt.Sub(time.Unix(clock, 0)); got != 2*time.Minute {
		t.Fatalf("second backoff = %v, want 2m", got)
	}
	for i := 0; i < 12; i++ {
		clock = env.pusher.retryAt.Unix()
		_ = env.pusher.pushIfChanged(ctx)
	}
	if got := env.pusher.retryAt.Sub(time.Unix(clock, 0)); got != time.Hour {
		t.Fatalf("backoff cap = %v, want 1h", got)
	}
	// Protection coming back on retries right away.
	env.pusher.poked()
	before := dials
	_ = env.pusher.pushIfChanged(ctx)
	if dials == before {
		t.Fatal("a poke did not retry")
	}
}

// Links that cannot key a sync (brook) are skipped quietly: nothing to push, not an error loop.
func TestPusherSkipsServersThatCannotSync(t *testing.T) {
	env := newPushEnv(t)
	_ = os.WriteFile(env.cfgPath, []byte("server: brook://server?server=203.0.113.9%3A9999&password=x\n"), 0o600)
	if err := env.pusher.pushIfChanged(context.Background()); err != nil || env.puts != 0 {
		t.Fatalf("brook server: err=%v puts=%d", err, env.puts)
	}
}

// The daemon wires no poke channel, so the pusher notices protection coming back on by itself:
// the backoff from a failure while the tunnel was down must not delay the first push by an hour.
func TestProtectionComingBackOnClearsTheBackoff(t *testing.T) {
	env := newPushEnv(t)
	env.pusher.retryAt = time.Unix(1_800_000_000, 0).Add(time.Hour)
	env.protected = false
	_ = env.pusher.pushIfChanged(context.Background())
	env.protected = true
	if err := env.pusher.pushIfChanged(context.Background()); err != nil || env.puts != 1 {
		t.Fatalf("after protection came back on: err=%v puts=%d (still backing off)", err, env.puts)
	}
}
