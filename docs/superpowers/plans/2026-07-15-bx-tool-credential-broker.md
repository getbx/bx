# bx Tool Credential Broker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional personal macOS Tool Keys companion that keeps API tokens out of agent context and only injects each token into requests for its exact bound HTTPS origin.

**Architecture:** Keep all credential state and outbound HTTP work in a separate root-owned `bx keyd` launchd service under `internal/toolkeys`. Existing `bx mcp` and BxMenu communicate with keyd through a versioned Unix-socket LocalAPI; bx supervisor, TUN, DNS, routing, dialer, and tunnel packages never import Tool Keys. The default agent-assisted flow lets the agent propose origin and authentication metadata while the user only pastes the token into BxMenu.

**Tech Stack:** Go 1.26, `net/http` over Unix sockets, `golang.org/x/net/idna`, `golang.org/x/sys/unix`, MCP Go SDK v1.6.1, Swift 5.9/AppKit, launchd.

## Global Constraints

- V1 is personal macOS + BxMenu + MCP only; Windows/Linux UI and storage are out of scope.
- Tool Keys is disabled by default and uses a separate process, launchd label, Unix socket, data directory, and logs.
- `internal/toolkeys` must not import `internal/supervisor`, `internal/tun`, `internal/dns`, `internal/route`, `internal/dialer`, or `internal/tunnel`; those packages must not import `internal/toolkeys`.
- A credential binds to one canonical HTTPS origin: lowercase/IDNA hostname, default port 443 normalized, no userinfo/path/query/fragment, no wildcard, IP literal, localhost, or private-CA exception.
- Agent-facing APIs never accept, return, display, copy, export, log, place in argv, or place in environment variables any token value.
- V1 does not infer provider scopes and does not store method/path permission tables; sanitized upstream 401/403 responses pass back to the agent.
- Requests accept only relative origin paths and cannot override Host; redirects are never followed.
- Request and response bodies are JSON or UTF-8 text and at most 8 MiB in each direction; multipart, binary, SSE, and unbounded streaming are rejected.
- Logs contain metadata only and omit query strings, bodies, authentication headers, cookies, and token values.
- keyd failure, upgrade failure, or corrupted state must not affect bx core network protection or existing MCP tools.
- Follow the repository TDD rule: failing test, confirm red, minimal implementation, confirm green, then commit.

---

## Planned File Structure

```text
internal/toolkeys/
  types.go                 credential, auth, request, response, error types
  origin.go                exact HTTPS origin canonicalization
  store.go                 root-only atomic credential/pending persistence
  audit.go                 metadata-only bounded audit log
  redact.go                exact-token and sensitive-JSON response redaction
  broker.go                origin-bound outbound HTTP execution
  peercred_darwin.go       LOCAL_PEERCRED UID extraction
  peercred_other.go        fail-closed non-Darwin stub
  localapi.go              keyd HTTP-over-Unix server
  client.go                MCP/CLI LocalAPI client
  daemon.go                Unix listener and keyd lifecycle
  launchd_darwin.go        separate launchd install/enable/disable/status
  launchd_other.go         unsupported-platform errors
  *_test.go                package-focused tests
internal/mcp/
  toolkeys_ops.go          optional Tool Keys operation port and live adapter
  tools_toolkeys.go        three MCP tools
  tools_toolkeys_test.go   tool contracts and secret-free results
internal/cli/
  toolkeys.go              toolkeys/keyd commands; token completion uses stdin
  toolkeys_test.go         command and source-contract tests
apps/macos/BxMenu/Sources/BxMenu/ToolKeys/
  ToolKeysModels.swift     pure display/pending models
  ToolKeysClient.swift     invokes bx CLI; writes tokens only to child stdin
  CredentialPrompt.swift  NSSecureTextField user-presence prompt
  ToolKeysPanel.swift      list/pause/replace/delete UI
apps/macos/BxMenu/Tests/
  ToolKeysPresentationTests.swift
```

### Task 1: Credential model and canonical origin

**Files:**
- Create: `internal/toolkeys/types.go`
- Create: `internal/toolkeys/origin.go`
- Create: `internal/toolkeys/origin_test.go`

**Interfaces:**
- Produces: `type AuthHint struct { Type AuthType; Name string }` with only `bearer`, `header`, and `query`.
- Produces: `func CanonicalOrigin(raw string) (string, error)`.
- Produces: `type Credential`, `CredentialMeta`, `PendingRequest`, `APIRequest`, `APIResponse`, and typed `Code`/`Error` values used by all later tasks.

- [ ] **Step 1: Write failing origin and auth tests**

Create table tests with these exact cases:

```go
func TestCanonicalOrigin(t *testing.T) {
    tests := []struct {
        in   string
        want string
        ok   bool
    }{
        {"https://API.Example.com", "https://api.example.com", true},
        {"https://api.example.com:443", "https://api.example.com", true},
        {"https://api.example.com:8443", "https://api.example.com:8443", true},
        {"https://b\u00fccher.example", "https://xn--bcher-kva.example", true},
        {"http://api.example.com", "", false},
        {"https://api.example.com/v1", "", false},
        {"https://user@api.example.com", "", false},
        {"https://127.0.0.1", "", false},
        {"https://localhost", "", false},
        {"https://*.example.com", "", false},
    }
    for _, tt := range tests {
        got, err := CanonicalOrigin(tt.in)
        if tt.ok && (err != nil || got != tt.want) {
            t.Fatalf("CanonicalOrigin(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
        }
        if !tt.ok && err == nil {
            t.Fatalf("CanonicalOrigin(%q) succeeded: %q", tt.in, got)
        }
    }
}

func TestAuthHintValidate(t *testing.T) {
    good := []AuthHint{
        {Type: AuthBearer},
        {Type: AuthHeader, Name: "X-API-Key"},
        {Type: AuthQuery, Name: "api_key"},
    }
    for _, hint := range good {
        if err := hint.Validate(); err != nil { t.Fatalf("%+v: %v", hint, err) }
    }
    bad := []AuthHint{
        {Type: "raw", Name: "Authorization: Bearer {secret}"},
        {Type: AuthHeader, Name: "Host"},
        {Type: AuthHeader, Name: "X-Key\r\nInjected"},
        {Type: AuthQuery, Name: ""},
    }
    for _, hint := range bad {
        if err := hint.Validate(); err == nil { t.Fatalf("%+v accepted", hint) }
    }
}
```

- [ ] **Step 2: Run tests and confirm red**

Run: `go test ./internal/toolkeys -run 'TestCanonicalOrigin|TestAuthHintValidate'`

Expected: FAIL because `internal/toolkeys` and the referenced symbols do not exist.

- [ ] **Step 3: Implement the minimal types and canonicalizer**

Use `url.Parse`, reject every non-empty component forbidden by the global constraints, reject `net.ParseIP(host) != nil`, convert with `idna.Lookup.ToASCII`, and normalize `:443`. Define header-name validation with `httpguts.ValidHeaderFieldName`; reject `Host`, `Authorization`, `Proxy-Authorization`, `Cookie`, and `Set-Cookie` case-insensitively.

The shared request shape must be:

```go
type AuthType string

const (
    AuthBearer AuthType = "bearer"
    AuthHeader AuthType = "header"
    AuthQuery  AuthType = "query"
)

type AuthHint struct {
    Type AuthType `json:"type"`
    Name string   `json:"name,omitempty"`
}

type Credential struct {
    ID         string    `json:"id"`
    Label      string    `json:"label"`
    Origin     string    `json:"origin"`
    Secret     string    `json:"-"`
    AuthHint   AuthHint  `json:"auth_hint"`
    Enabled    bool      `json:"enabled"`
    CreatedAt  time.Time `json:"created_at"`
    RotatedAt  time.Time `json:"rotated_at,omitempty"`
    LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

type CredentialMeta struct {
    ID         string    `json:"id"`
    Label      string    `json:"label"`
    Origin     string    `json:"origin"`
    AuthHint   AuthHint  `json:"auth_hint"`
    Enabled    bool      `json:"enabled"`
    CreatedAt  time.Time `json:"created_at"`
    RotatedAt  time.Time `json:"rotated_at,omitempty"`
    LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

type PendingRequest struct {
    ID        string    `json:"id"`
    Origin    string    `json:"origin"`
    AuthHint  AuthHint  `json:"auth_hint"`
    Reason    string    `json:"reason"`
    DocsURL   string    `json:"docs_url,omitempty"`
    CreatedAt time.Time `json:"created_at"`
    ExpiresAt time.Time `json:"expires_at"`
}

type APIRequest struct {
    CredentialID string            `json:"credential_id"`
    Method       string            `json:"method"`
    Path         string            `json:"path"`
    Query        url.Values        `json:"query,omitempty"`
    Headers      map[string]string `json:"headers,omitempty"`
    JSONBody     json.RawMessage   `json:"json_body,omitempty"`
    TextBody     *string           `json:"text_body,omitempty"`
    AuthHint     *AuthHint         `json:"auth_hint,omitempty"`
}

type APIResponse struct {
    Status      int               `json:"status"`
    Headers     map[string]string `json:"headers,omitempty"`
    ContentType string            `json:"content_type,omitempty"`
    JSONBody    json.RawMessage   `json:"json_body,omitempty"`
    TextBody    *string           `json:"text_body,omitempty"`
}

type PendingRequestInput struct {
    Origin   string   `json:"origin"`
    AuthHint AuthHint `json:"auth_hint"`
    Reason   string   `json:"reason"`
    DocsURL  string   `json:"docs_url,omitempty"`
}
```

Define finite codes `CREDENTIAL_REQUIRED`, `USER_ACTION_REQUIRED`, `BROKER_UNAVAILABLE`, `ORIGIN_INVALID`, `REQUEST_INVALID`, `CREDENTIAL_DISABLED`, `REDIRECT_NOT_FOLLOWED`, `BODY_TOO_LARGE`, and `UPSTREAM_FAILED`.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/toolkeys && git diff --check`

Expected: PASS.

Commit:

```bash
git add internal/toolkeys/types.go internal/toolkeys/origin.go internal/toolkeys/origin_test.go
git commit -m "feat(toolkeys): define origin-bound credentials"
```

### Task 2: Root-only atomic store, pending requests, and audit

**Files:**
- Create: `internal/toolkeys/store.go`
- Create: `internal/toolkeys/store_test.go`
- Create: `internal/toolkeys/audit.go`
- Create: `internal/toolkeys/audit_test.go`

**Interfaces:**
- Consumes: Task 1 `Credential`, `CredentialMeta`, `PendingRequest`, `AuthHint`, and codes.
- Produces: `OpenStore(path string) (*Store, error)`, `Put`, `Resolve`, `List`, `SetEnabled`, `ReplaceSecret`, `Delete`, `CreatePending`, `CompletePending`, and `ListPending`.
- Produces: `OpenAudit(path string, retention time.Duration) (*Audit, error)` and `Record(AuditEntry) error`.

- [ ] **Step 1: Write failing store tests**

Cover these behaviors with `t.TempDir()`:

```go
func TestStoreNeverExposesSecretInMeta(t *testing.T) {
    s, err := OpenStore(filepath.Join(t.TempDir(), "credentials.json"))
    if err != nil { t.Fatal(err) }
    c := Credential{ID: "cred-1", Label: "Example", Origin: "https://api.example.com", Secret: "bx-secret-123", AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}
    if err := s.Put(c); err != nil { t.Fatal(err) }
    metas := s.List()
    b, _ := json.Marshal(metas)
    if bytes.Contains(b, []byte(c.Secret)) { t.Fatalf("meta leaked secret: %s", b) }
    got, err := s.Resolve(c.ID)
    if err != nil || got.Secret != c.Secret { t.Fatalf("Resolve = %+v, %v", got, err) }
}

func TestStoreFileModeAndAtomicRotation(t *testing.T) {
    path := filepath.Join(t.TempDir(), "credentials.json")
    s, err := OpenStore(path)
    if err != nil { t.Fatal(err) }
    if err := s.Put(Credential{ID: "c", Origin: "https://api.example.com", Secret: "old", Enabled: true}); err != nil { t.Fatal(err) }
    if err := s.ReplaceSecret("c", "new"); err != nil { t.Fatal(err) }
    info, err := os.Stat(path)
    if err != nil { t.Fatal(err) }
    if info.Mode().Perm() != 0o600 { t.Fatalf("mode = %o", info.Mode().Perm()) }
    got, _ := s.Resolve("c")
    if got.Secret != "new" { t.Fatalf("secret = %q", got.Secret) }
}

func TestPendingExpiresAndCompletionConsumesIt(t *testing.T) {
    now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
    s, err := OpenStore(filepath.Join(t.TempDir(), "credentials.json"))
    if err != nil { t.Fatal(err) }
    s.now = func() time.Time { return now }
    pending, err := s.CreatePending("https://api.example.com", AuthHint{Type: AuthBearer}, "create task", "")
    if err != nil { t.Fatal(err) }
    cred, err := s.CompletePending(pending.ID, "secret")
    if err != nil { t.Fatal(err) }
    if cred.Origin != pending.Origin { t.Fatalf("origin changed: %+v", cred) }
    if _, err := s.CompletePending(pending.ID, "again"); err == nil { t.Fatal("pending reused") }
}
```

Also test corrupted JSON is preserved byte-for-byte and `OpenStore` returns an error rather than replacing it; deleting a credential removes its secret; and a pending request older than 10 minutes is omitted and cannot be completed.

- [ ] **Step 2: Confirm red**

Run: `go test ./internal/toolkeys -run 'TestStore|TestPending|TestAudit'`

Expected: FAIL because store and audit implementations are missing.

- [ ] **Step 3: Implement atomic persistence**

Use one mutex-protected in-memory state loaded from JSON. Every mutation writes a sibling temp file with mode `0600`, calls `Sync`, closes, and renames over the target. Create the parent directory with `0700`. Never expose the disk record type from the package; `List` must construct new `CredentialMeta` values.

Use `crypto/rand` plus `base64.RawURLEncoding` for 24-byte credential and pending IDs. `CompletePending` must perform secret insert and pending removal in one persisted state transition.

Audit entries contain only:

```go
type AuditEntry struct {
    Time         time.Time `json:"time"`
    CredentialID string    `json:"credential_id"`
    Label        string    `json:"label"`
    Origin       string    `json:"origin"`
    Method       string    `json:"method"`
    Path         string    `json:"path"`
    Status       int       `json:"status"`
    DurationMS   int64     `json:"duration_ms"`
    Surface      string    `json:"surface"`
}
```

Write JSON Lines with mode `0600`; reject entries whose path contains `?`; prune records older than 30 days during append and explicit clear.

- [ ] **Step 4: Verify secret isolation and commit**

Run:

```bash
go test ./internal/toolkeys -run 'TestStore|TestPending|TestAudit'
go test ./internal/toolkeys
git diff --check
```

Expected: PASS.

Commit:

```bash
git add internal/toolkeys/store.go internal/toolkeys/store_test.go internal/toolkeys/audit.go internal/toolkeys/audit_test.go
git commit -m "feat(toolkeys): persist credentials without readable metadata leaks"
```

### Task 3: Origin-bound HTTP broker and response redaction

**Files:**
- Create: `internal/toolkeys/redact.go`
- Create: `internal/toolkeys/redact_test.go`
- Create: `internal/toolkeys/broker.go`
- Create: `internal/toolkeys/broker_test.go`

**Interfaces:**
- Consumes: `Store.Resolve`, `Audit.Record`, `APIRequest`, `APIResponse`, `AuthHint`.
- Produces: `NewBroker(store *Store, audit *Audit, client *http.Client) *Broker`.
- Produces: `func (b *Broker) Do(ctx context.Context, req APIRequest, surface string) (APIResponse, error)`.

- [ ] **Step 1: Write failing redaction and transport tests**

Use an `httptest.NewTLSServer` behind a test-only transport that dials its listener while preserving the canonical origin `https://api.example.test`; do not weaken production origin validation or store an IP-literal origin.

Required tests:

```go
func TestBrokerInjectsSecretAndStripsCallerAuth(t *testing.T) {
    const secret = "tool-secret-value"
    var gotAuth, gotCookie string
    ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotAuth, gotCookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
        w.Header().Set("Set-Cookie", "session=secret")
        _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "access_token": "new-secret"})
    }))
    defer ts.Close()
    broker := testBroker(t, "https://api.example.test", secret, testHTTPClientForTLSServer(ts))
    out, err := broker.Do(context.Background(), APIRequest{
        CredentialID: "cred",
        Method: http.MethodPost,
        Path: "/v1/run",
        Headers: map[string]string{"Authorization": "Bearer attacker", "Cookie": "a=b"},
        JSONBody: json.RawMessage(`{"input":"hello"}`),
    }, "mcp")
    if err != nil { t.Fatal(err) }
    if gotAuth != "Bearer "+secret || gotCookie != "" { t.Fatalf("auth=%q cookie=%q", gotAuth, gotCookie) }
    body := string(out.JSONBody)
    if strings.Contains(body, secret) || strings.Contains(body, "new-secret") { t.Fatalf("leaked body: %s", body) }
    if _, ok := out.Headers["Set-Cookie"]; ok { t.Fatalf("Set-Cookie leaked: %+v", out.Headers) }
}

func testBroker(t *testing.T, origin, secret string, client *http.Client) *Broker {
    t.Helper()
    dir := t.TempDir()
    store, err := OpenStore(filepath.Join(dir, "credentials.json"))
    if err != nil { t.Fatal(err) }
    if err := store.Put(Credential{ID: "cred", Label: "Example", Origin: origin, Secret: secret, AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}); err != nil { t.Fatal(err) }
    audit, err := OpenAudit(filepath.Join(dir, "audit.jsonl"), 30*24*time.Hour)
    if err != nil { t.Fatal(err) }
    return NewBroker(store, audit, client)
}

func testHTTPClientForTLSServer(ts *httptest.Server) *http.Client {
    dialer := &net.Dialer{Timeout: time.Second}
    return &http.Client{Transport: &http.Transport{
        DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
            return dialer.DialContext(ctx, "tcp", ts.Listener.Addr().String())
        },
        TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test server certificate only
    }}
}
```

Add tests that reject an absolute path, `//other.example/path`, CR/LF, Host override, non-UTF-8 body, and body over 8 MiB. Add 301/302/307/308 tests whose redirect target increments a counter; require the counter remain zero. Add bearer/header/query injection tests and require audit paths exclude query strings.

- [ ] **Step 2: Confirm red**

Run: `go test ./internal/toolkeys -run 'TestBroker|TestRedact'`

Expected: FAIL because broker and redaction symbols are missing.

- [ ] **Step 3: Implement the constrained broker**

Clone the supplied HTTP client and set:

```go
client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
    return http.ErrUseLastResponse
}
```

Build the destination only with `url.JoinPath(credential.Origin, req.Path)` after strict relative-path validation. Force `outgoing.Host = ""`, strip credential-like and hop-by-hop headers, then inject exactly one credential from the selected bounded `AuthHint`. Use `io.LimitReader(resp.Body, 8<<20+1)` and return `BODY_TOO_LARGE` on the extra byte.

For JSON responses, recursively replace values whose normalized field name is one of `token`, `api_key`, `secret`, `password`, `private_key`, `client_secret`, `access_token`, or `refresh_token` with `<redacted>`. For JSON and UTF-8 text, replace every exact stored credential value with `<redacted>`. Strip response `Authorization`, `Proxy-Authorization`, `Set-Cookie`, and `Cookie`.

- [ ] **Step 4: Verify and commit**

Run:

```bash
go test ./internal/toolkeys -run 'TestBroker|TestRedact'
go test ./internal/toolkeys
git diff --check
```

Expected: PASS and redirect target count remains zero.

Commit:

```bash
git add internal/toolkeys/redact.go internal/toolkeys/redact_test.go internal/toolkeys/broker.go internal/toolkeys/broker_test.go
git commit -m "feat(toolkeys): proxy credentials only to their bound origin"
```

### Task 4: Versioned keyd LocalAPI and client

**Files:**
- Create: `internal/toolkeys/peercred_darwin.go`
- Create: `internal/toolkeys/peercred_other.go`
- Create: `internal/toolkeys/peercred_darwin_test.go`
- Create: `internal/toolkeys/localapi.go`
- Create: `internal/toolkeys/localapi_test.go`
- Create: `internal/toolkeys/client.go`
- Create: `internal/toolkeys/client_test.go`
- Create: `internal/toolkeys/daemon.go`

**Interfaces:**
- Consumes: Task 2 store/pending management and Task 3 broker.
- Produces: `Serve(ctx context.Context, Options) error` for hidden `bx keyd`.
- Produces: `NewClient(socketPath string) *Client` with `List`, `CreatePending`, `ListPending`, `CompletePending`, `SetEnabled`, `Replace`, `Delete`, and `Do`.

- [ ] **Step 1: Write failing LocalAPI contract tests**

Register these exact routes under `/v1`:

```text
GET    /v1/credentials
POST   /v1/pending
GET    /v1/pending
PUT    /v1/pending/{id}/complete
POST   /v1/credentials/{id}/enable
POST   /v1/credentials/{id}/pause
PUT    /v1/credentials/{id}/secret
DELETE /v1/credentials/{id}
POST   /v1/request
GET    /v1/health
```

Tests must require JSON content types, method rejection, 1 MiB control-body limit, structured finite error codes, no secret in any list/error response, and serialization compatibility through the real `Client` against a temporary Unix listener.

Darwin peer test:

```go
func TestPeerCredUIDDarwin(t *testing.T) {
    path := filepath.Join(t.TempDir(), "keyd.sock")
    ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
    if err != nil { t.Fatal(err) }
    defer ln.Close()
    accepted := make(chan *net.UnixConn, 1)
    go func() { c, _ := ln.AcceptUnix(); accepted <- c }()
    client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
    if err != nil { t.Fatal(err) }
    defer client.Close()
    server := <-accepted
    defer server.Close()
    uid, ok := peerCredUID(server)
    if !ok || uid != uint32(os.Geteuid()) { t.Fatalf("uid=%d ok=%v", uid, ok) }
}
```

- [ ] **Step 2: Confirm red**

Run: `go test ./internal/toolkeys -run 'TestLocalAPI|TestClient|TestPeerCredUIDDarwin'`

Expected: FAIL because LocalAPI/client symbols are missing.

- [ ] **Step 3: Implement owner/root authorization and daemon lifecycle**

On Darwin, use `unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)`. Authorize every route only when peer UID is root or equals `Options.OwnerUID`; fail closed when peer credentials are unavailable. Store the accepted `net.Conn` in `http.Server.ConnContext`, matching the existing supervisor LocalAPI pattern without importing supervisor.

`Options` must be:

```go
type Options struct {
    SocketPath      string
    CredentialPath string
    AuditPath      string
    OwnerUID       uint32
    HTTPClient     *http.Client
}
```

Create the socket parent as `0755`, remove a stale socket only after verifying it is a socket, listen, chmod the socket `0666`, and shut down on context cancellation. Secret completion and replacement bodies use `{"secret":"secret-value"}` only inside this owner-authorized Unix connection; handlers zero their local byte slice after persistence and never echo it.

- [ ] **Step 4: Verify and commit**

Run:

```bash
go test ./internal/toolkeys
GOOS=linux GOARCH=amd64 go test ./internal/toolkeys
git diff --check
```

Expected: PASS; non-Darwin peer authorization remains fail closed.

Commit:

```bash
git add internal/toolkeys/peercred_* internal/toolkeys/localapi.go internal/toolkeys/localapi_test.go internal/toolkeys/client.go internal/toolkeys/client_test.go internal/toolkeys/daemon.go
git commit -m "feat(toolkeys): add isolated keyd LocalAPI"
```

### Task 5: Optional launchd lifecycle and CLI bridge

**Files:**
- Create: `internal/toolkeys/paths_darwin.go`
- Create: `internal/toolkeys/paths_other.go`
- Create: `internal/toolkeys/launchd_darwin.go`
- Create: `internal/toolkeys/launchd_other.go`
- Create: `internal/toolkeys/launchd_test.go`
- Create: `internal/cli/toolkeys.go`
- Create: `internal/cli/toolkeys_test.go`
- Modify: `internal/cli/cli.go`

**Interfaces:**
- Produces: service label `com.getbx.toolkeys`, plist `/Library/LaunchDaemons/com.getbx.toolkeys.plist`, socket `/var/run/bx-toolkeys.sock`, state directory `/Library/Application Support/bx/toolkeys`, and separate log paths `/var/log/bx-toolkeys.log`/`.err.log`.
- Produces CLI: `bx toolkeys enable|disable|status|list|pending|request|complete|pause|resume|replace|delete` and hidden `bx keyd`.
- Secret-bearing `complete` and `replace` read exactly one token from stdin and reject terminal stdin unless `--interactive` is explicitly set.

- [ ] **Step 1: Write failing launchd and CLI tests**

Require the generated plist to contain only `/usr/local/bin/bx`, `keyd`, `--owner-uid`, the decimal UID, and the dedicated paths. Reject `run`, the bx core config path, bx core label, and bx core log paths.

CLI source-contract tests must require:

```go
func TestReadSecretFromStdinTrimsOneLineOnly(t *testing.T) {
    got, err := readSecret(strings.NewReader("secret-value\n"), false)
    if err != nil || got != "secret-value" { t.Fatalf("got=%q err=%v", got, err) }
    if _, err := readSecret(strings.NewReader("one\ntwo\n"), false); err == nil { t.Fatal("accepted multiple lines") }
}

func TestToolKeysCommandsNeverAcceptSecretFlag(t *testing.T) {
    app := New()
    cmd := findCommand(t, app, "toolkeys")
    for _, sub := range cmd.Subcommands {
        for _, flag := range sub.Flags {
            if flag.Names()[0] == "secret" || flag.Names()[0] == "token" { t.Fatalf("secret flag on %s", sub.Name) }
        }
    }
}

func findCommand(t *testing.T, app *cli.App, name string) *cli.Command {
    t.Helper()
    for _, command := range app.Commands {
        if command.Name == name { return command }
    }
    t.Fatalf("command %q not found", name)
    return nil
}
```

- [ ] **Step 2: Confirm red**

Run: `go test ./internal/toolkeys ./internal/cli -run 'TestToolKeys|TestReadSecret|TestLaunchd'`

Expected: FAIL because launchd and CLI files do not exist.

- [ ] **Step 3: Implement separate service lifecycle**

`bx toolkeys enable` must require Darwin/root, resolve owner UID from `--owner-uid` first and then `SUDO_UID`, reject zero/missing owner UID for normal personal setup, call `install.SelfInstall`, atomically write the dedicated plist, and run `launchctl bootstrap/kickstart` only for `system/com.getbx.toolkeys`. It must not call `install.Enable`, `install.Disable`, `bx up`, `bx down`, or touch `/etc/bx/config.yaml`.

`bx toolkeys disable` bootouts and removes only the Tool Keys plist; it preserves credentials unless `--purge` is supplied. `--purge` removes the root-only state after service shutdown and requires a second explicit CLI flag `--confirm-purge` so a scripted typo cannot erase keys.

BxMenu will pass its numeric `getuid()` as `--owner-uid`; Terminal users get the same value from `sudo` through `SUDO_UID`.

- [ ] **Step 4: Verify and commit**

Run:

```bash
go test ./internal/toolkeys ./internal/cli
go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
git diff --check
```

Expected: PASS. Inspect `git diff -- internal/supervisor internal/tun internal/dns internal/route internal/dialer internal/tunnel`; expected output is empty.

Commit:

```bash
git add internal/toolkeys/paths_* internal/toolkeys/launchd_* internal/cli/toolkeys.go internal/cli/toolkeys_test.go internal/cli/cli.go
git commit -m "feat(macos): install Tool Keys as an optional service"
```

### Task 6: Add Tool Keys to the existing MCP server

**Files:**
- Create: `internal/mcp/toolkeys_ops.go`
- Create: `internal/mcp/tools_toolkeys.go`
- Create: `internal/mcp/tools_toolkeys_test.go`
- Modify: `internal/mcp/server.go`
- Modify: `internal/mcp/errors.go`
- Modify: `internal/mcp/annotations_test.go`
- Modify: `internal/cli/cli.go`

**Interfaces:**
- Consumes: `toolkeys.Client` only; MCP never opens the credential store or performs outbound provider HTTP itself.
- Produces: `bx_credentials_list`, `bx_credential_request`, and `bx_api_request`.
- Preserves: existing `mcp.Serve(ctx, ops)` for tests/callers without Tool Keys; adds `mcp.ServeWithToolKeys(ctx, ops, toolOps)`.

- [ ] **Step 1: Write failing MCP tool tests**

Define a focused port:

```go
type ToolKeyOps interface {
    CredentialList(context.Context) ([]toolkeys.CredentialMeta, error)
    CredentialRequest(context.Context, toolkeys.PendingRequestInput) (toolkeys.PendingRequest, error)
    APIRequest(context.Context, toolkeys.APIRequest) (toolkeys.APIResponse, error)
}
```

Tests must assert exact tool names, JSON schemas, no `secret`/`token` input fields, no secret-bearing outputs, and annotations:

```go
readonly := []string{"bx_credentials_list"}
destructive := []string{"bx_credential_request", "bx_api_request"}
```

The fake ToolKeyOps should return `toolkeys.Error{Code: toolkeys.CodeBrokerUnavailable}` and the MCP result must contain `BROKER_UNAVAILABLE` plus remediation to enable Tool Keys, never remediation that tells the user to paste a key into chat.

- [ ] **Step 2: Confirm red**

Run: `go test ./internal/mcp -run 'TestToolKey|TestToolAnnotations'`

Expected: FAIL because Tool Keys MCP registration is missing.

- [ ] **Step 3: Implement the thin adapter**

Keep `newServer(ops Ops)` unchanged for existing tests. Add:

```go
func newServerWithToolKeys(ops Ops, tk ToolKeyOps) *mcpsdk.Server {
    s := newServer(ops)
    if tk != nil { registerToolKeys(s, tk) }
    return s
}

func ServeWithToolKeys(ctx context.Context, ops Ops, tk ToolKeyOps) error {
    return newServerWithToolKeys(ops, tk).Run(ctx, &mcpsdk.StdioTransport{})
}
```

`mcpAction` constructs `toolkeys.NewClient(toolkeys.SocketPath)` and a live adapter, then calls `ServeWithToolKeys`. Registration remains available when keyd is stopped so the agent receives a structured unavailable result and can guide the user to enable the optional feature; existing bx tools continue working.

- [ ] **Step 4: Verify and commit**

Run:

```bash
go test ./internal/mcp ./internal/cli
go test ./...
git diff --check
```

Expected: PASS and all pre-existing tool annotations remain unchanged.

Commit:

```bash
git add internal/mcp/toolkeys_ops.go internal/mcp/tools_toolkeys.go internal/mcp/tools_toolkeys_test.go internal/mcp/server.go internal/mcp/errors.go internal/mcp/annotations_test.go internal/cli/cli.go
git commit -m "feat(mcp): expose secret-free Tool Keys calls"
```

### Task 7: Add the minimal BxMenu Tool Keys experience

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/ToolKeys/ToolKeysModels.swift`
- Create: `apps/macos/BxMenu/Sources/BxMenu/ToolKeys/ToolKeysClient.swift`
- Create: `apps/macos/BxMenu/Sources/BxMenu/ToolKeys/CredentialPrompt.swift`
- Create: `apps/macos/BxMenu/Sources/BxMenu/ToolKeys/ToolKeysPanel.swift`
- Create: `apps/macos/BxMenu/Tests/ToolKeysPresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `apps/macos/BxMenu/README.md`

**Interfaces:**
- Consumes CLI JSON from `bx toolkeys status|list|pending`.
- Sends token values only through a child `Process.standardInput` pipe to `bx toolkeys complete` or `replace`; never command arguments, environment, stdout, stderr, or AppleScript source.
- Produces menu action `Tool Keys…`, pending prompt, enable action, list, pause/resume, replace, and delete.

- [ ] **Step 1: Write failing pure Swift presentation tests**

Create pure models and require this behavior:

```swift
let pending = ToolKeyPending(
    id: "p1",
    origin: "https://api.example.com",
    authLabel: "Bearer (suggested by Codex)",
    reason: "Create task",
    source: "Codex"
)
expect(pending.hostname == "api.example.com", "pending hostname")
expect(!pending.summary.lowercased().contains("scope"), "no scope prompt")
expect(!pending.summary.lowercased().contains("method"), "no method prompt")

let row = ToolKeyRow(label: "Example", hostname: "api.example.com", enabled: true, lastUsed: "2 minutes ago")
expect(row.actions == [.pause, .replace, .delete], "minimal enabled actions")
```

Add a source-contract test that reads `ToolKeysClient.swift`, requires `standardInput`, and rejects `process.arguments` construction containing `secret`, `token`, or the secure field value.

- [ ] **Step 2: Confirm red**

Run:

```bash
swiftc apps/macos/BxMenu/Sources/BxMenu/ToolKeys/ToolKeysModels.swift apps/macos/BxMenu/Tests/ToolKeysPresentationTests.swift -o /tmp/bx-toolkeys-presentation-tests
```

Expected: compile failure because Tool Keys Swift sources do not exist.

- [ ] **Step 3: Implement the secure prompt and process bridge**

Use `NSSecureTextField` in `CredentialPrompt`. Prominently display the agent-proposed hostname, auth label, reason, and the warning “Only paste a key issued for this service.” Do not request provider scope, method, path, OpenAPI, or provider type.

`ToolKeysClient.complete(id:secret:)` must configure:

```swift
let process = Process()
process.executableURL = URL(fileURLWithPath: "/usr/local/bin/bx")
process.arguments = ["toolkeys", "complete", id]
let input = Pipe()
process.standardInput = input
try process.run()
input.fileHandleForWriting.write(Data((secret + "\n").utf8))
try input.fileHandleForWriting.close()
process.waitUntilExit()
```

Immediately clear the secure field string after copying it into the local `String`; keep that string scoped to the completion method and never log it. Do not use `runPrivileged`, AppleScript, Terminal, environment variables, or shell quoting for token completion/replacement.

Add `Tool Keys…` to the menu regardless of bx tunnel state. If keyd is disabled, the panel shows one `Enable Tool Keys` action; it may use the existing privileged helper to run `bx toolkeys enable --owner-uid <getuid()>`, because that command contains no token. Poll pending requests on the existing five-second refresh and present each pending ID at most once.

- [ ] **Step 4: Verify and commit**

Run:

```bash
swiftc apps/macos/BxMenu/Sources/BxMenu/ToolKeys/ToolKeysModels.swift apps/macos/BxMenu/Tests/ToolKeysPresentationTests.swift -o /tmp/bx-toolkeys-presentation-tests
/tmp/bx-toolkeys-presentation-tests
swift build --package-path apps/macos/BxMenu -c release
go test ./...
git diff --check
```

Expected: PASS.

Commit:

```bash
git add apps/macos/BxMenu/Sources/BxMenu/ToolKeys apps/macos/BxMenu/Sources/BxMenu/main.swift apps/macos/BxMenu/Tests/ToolKeysPresentationTests.swift apps/macos/BxMenu/README.md
git commit -m "feat(macos): add minimal Tool Keys management"
```

### Task 8: Release isolation, documentation, and macOS end-to-end proof

**Files:**
- Create: `scripts/toolkeys-smoke.sh`
- Modify: `scripts/package-macos-release.sh`
- Modify: `scripts/verify-macos-release.sh`
- Modify: `README.md`
- Modify: `docs/agent-tools.md`
- Modify: `SECURITY.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Produces an explicit `scripts/toolkeys-smoke.sh --execute` test; default invocation prints planned operations and changes nothing.
- Documents the honest security boundary: prevents key disclosure/cross-origin use, does not prevent confused-deputy destructive use on the bound provider.

- [ ] **Step 1: Add failing release/source contracts**

Extend release verification to require the installed binary expose `bx toolkeys --help` and `bx keyd --help`, while confirming the package installer does not enable Tool Keys automatically. Add a dependency-direction check:

```bash
if rg -n 'github.com/getbx/bx/internal/toolkeys' internal/supervisor internal/tun internal/dns internal/route internal/dialer internal/tunnel; then
  echo 'bx core must not import toolkeys' >&2
  exit 1
fi
if rg -n 'github.com/getbx/bx/internal/(supervisor|tun|dns|route|dialer|tunnel)' internal/toolkeys; then
  echo 'toolkeys must not import bx core' >&2
  exit 1
fi
```

- [ ] **Step 2: Implement the explicit macOS smoke flow**

The script must require `--execute`, a clean test HTTPS origin, and a disposable test token. It must never accept the token as an argument; read it with `read -s` and pass it to `bx toolkeys complete` through stdin. Verify:

```text
enable optional keyd with owner UID
create pending request for the test origin
complete it through stdin
perform a successful bearer request
perform a 401 retry with a different AuthHint
verify a redirect target was not contacted
pause and require CREDENTIAL_DISABLED
replace, resume, and call again
delete and require CREDENTIAL_REQUIRED
grep Tool Keys logs, MCP captures, and command environment snapshots for the test token: zero matches
stop keyd and verify bx status still works
```

The script cleanup trap removes the disposable credential and disables keyd only when the script enabled it; it never calls `bx down`, changes routes/DNS, or stops bx core.

- [ ] **Step 3: Document user and agent behavior**

README user flow:

```text
Agent requests a credential for a visible HTTPS hostname.
bx opens a native secure prompt.
Paste the token there, never in chat.
The agent retries through bx; 401/403 remains normal provider feedback.
Pause, replace, or delete the key from Tool Keys in the menu.
```

`docs/agent-tools.md` must instruct agents to call `bx_credential_request` instead of asking for a token in chat. `SECURITY.md` must state the origin-bound guarantee and confused-deputy non-goal. `CLAUDE.md` must record the package boundary and forbidden import directions.

- [ ] **Step 4: Run the full verification gate**

Run:

```bash
go build ./...
go vet ./...
go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
swiftc apps/macos/BxMenu/Sources/BxMenu/ToolKeys/ToolKeysModels.swift apps/macos/BxMenu/Tests/ToolKeysPresentationTests.swift -o /tmp/bx-toolkeys-presentation-tests
/tmp/bx-toolkeys-presentation-tests
swift build --package-path apps/macos/BxMenu -c release
bash -n scripts/toolkeys-smoke.sh
scripts/package-macos-release.sh
scripts/verify-macos-release.sh
git diff --check
```

Expected: every command exits 0; dependency-direction checks print nothing; existing `internal/cli/update.go` user work remains preserved unless it was independently completed by its owner.

- [ ] **Step 5: Run the explicit real-machine smoke and commit**

Run: `scripts/toolkeys-smoke.sh --execute`

Expected: all steps report PASS, token search reports zero matches, and bx remains protected after keyd stop.

Commit:

```bash
git add scripts/toolkeys-smoke.sh scripts/package-macos-release.sh scripts/verify-macos-release.sh README.md docs/agent-tools.md SECURITY.md CLAUDE.md
git commit -m "test(toolkeys): prove secret-free optional integration"
```

## Plan Self-Review Results

- Spec coverage: origin binding, minimal agent-assisted input, no scope inference, root-only storage, exact redirect behavior, response redaction, MCP integration, BxMenu management, independent launchd lifecycle, failure isolation, audit retention, and real-machine proof each map to Tasks 1–8.
- Scope: V1 deliberately excludes provider recipes, method/path authorization, localhost TCP proxy, multipart/binary/streaming, team identity, cloud sync, and Windows/Linux UI.
- Type consistency: `AuthHint`, `CredentialMeta`, `PendingRequest`, `APIRequest`, `APIResponse`, `ToolKeyOps`, and the LocalAPI client are defined once and consumed by later tasks with matching names.
- Dirty worktree: `internal/cli/update.go` predates this feature and must be preserved; every commit stages explicit Tool Keys paths only.
