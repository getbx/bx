# UDP Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make bx clearly report that non-DNS UDP is currently blocked and that WebRTC/Google Meet may be affected, without changing runtime UDP behavior.

**Architecture:** Keep UDP behavior unchanged in `internal/dialer`: non-DNS UDP remains fail-closed. Add an explicit UDP policy field to status reports, render it in `bx status`, expose a `udp_policy` doctor check, and add machine-readable capabilities entries for UDP diagnostics and planned realtime commands.

**Tech Stack:** Go, `urfave/cli/v2`, existing `internal/stats`, `internal/cli`, and JSON status socket.

---

## File Structure

- Modify `internal/stats/render.go`: add UDP policy fields to the status wire report and render a concise UDP line with an independent UDP blocked count.
- Modify `internal/stats/stats.go`: add `UDPBlocked` to counters and snapshots.
- Modify `internal/stats/stats_test.go`: test status rendering includes UDP policy and blocked count.
- Modify `internal/dialer/dialer.go`: increment `UDPBlocked` only on the existing UDP fast-block path.
- Modify `internal/supervisor/run.go`: set the current UDP policy in `serveStats`; first implementation is always `block`.
- Modify `internal/cli/cli.go`: add `udp_policy` to client doctor JSON and add UDP/realtime capabilities.
- Modify `internal/cli/cli_test.go`: test doctor and capabilities expose the new UDP diagnostics.

---

### Task 1: Status Report UDP Policy

**Files:**
- Modify: `internal/stats/render.go`
- Modify: `internal/stats/stats.go`
- Modify: `internal/stats/stats_test.go`
- Modify: `internal/dialer/dialer.go`
- Modify: `internal/supervisor/run.go`

- [ ] **Step 1: Write the failing status render test**

Append this test to `internal/stats/stats_test.go`:

```go
func TestRenderShowsUDPPolicy(t *testing.T) {
	rep := Report{
		Snapshot: Snapshot{
			Active:  2,
			Proxy:   3,
			Direct:  1,
			Blocked:    9,
			UDPBlocked: 4,
		},
		Server:        "example.com",
		SocksAddr:     "127.0.0.1:60000",
		TunnelHealthy: true,
		LatencyMS:     42,
		UDPMode:       "block",
		UDPNote:       "non-DNS UDP blocked; WebRTC/Google Meet may stutter",
	}
	out := Render(rep)
	for _, want := range []string{
		"UDP",
		"mode block",
		"阻断 4",
		"Google Meet",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/stats -run TestRenderShowsUDPPolicy
```

Expected: fail to compile because `Report.UDPMode` and `Report.UDPNote` do not exist, or fail because render output lacks UDP.

- [ ] **Step 3: Add UDP fields and rendering**

In `internal/stats/render.go`, extend `Snapshot` and `Report`:

```go
type Snapshot struct {
	Active     int64 `json:"active"`
	Proxy      int64 `json:"proxy"`
	Direct     int64 `json:"direct"`
	Blocked    int64 `json:"blocked"`
	UDPBlocked int64 `json:"udp_blocked"`
	BytesUp    int64 `json:"bytes_up"`
	BytesDown  int64 `json:"bytes_down"`
}

type Report struct {
	Snapshot
	Server        string `json:"server"`
	SocksAddr     string `json:"socks_addr"`
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
	Restarts      int    `json:"restarts"`
	UDPMode       string `json:"udp_mode"`
	UDPNote       string `json:"udp_note,omitempty"`
}
```

In `Render`, after the connection line, add:

```go
	udpMode := r.UDPMode
	if udpMode == "" {
		udpMode = "block"
	}
	fmt.Fprintf(&b, "  UDP     mode %s  阻断 %d", udpMode, r.UDPBlocked)
	if r.UDPNote != "" {
		fmt.Fprintf(&b, "  %s", r.UDPNote)
	}
	fmt.Fprintln(&b)
```

The surrounding section should become:

```go
	fmt.Fprintf(&b, "  隧道    %s  延迟 %dms  重连 %d\n", health, r.LatencyMS, r.Restarts)
	fmt.Fprintf(&b, "  连接    活跃 %d  代理 %d  直连 %d  阻断 %d\n", r.Active, r.Proxy, r.Direct, r.Blocked)
	udpMode := r.UDPMode
	if udpMode == "" {
		udpMode = "block"
	}
	fmt.Fprintf(&b, "  UDP     mode %s  阻断 %d", udpMode, r.UDPBlocked)
	if r.UDPNote != "" {
		fmt.Fprintf(&b, "  %s", r.UDPNote)
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "  分流    代理 %.1f%% / 直连 %.1f%%\n", ratio, 100-ratio)
```

- [ ] **Step 4: Set UDP policy in supervisor status report**

In `internal/supervisor/run.go`, inside `serveStats`, add fields to the `stats.Report` literal:

```go
				UDPMode:       "block",
				UDPNote:       "non-DNS UDP blocked; WebRTC/Google Meet may stutter",
```

The report literal should include:

```go
			rep := stats.Report{
				Snapshot:      c.Snapshot(),
				Server:        server,
				SocksAddr:     t.SocksAddr(),
				TunnelHealthy: ts.Up,
				LatencyMS:     ts.LatencyMS,
				Restarts:      ts.Restarts,
				UDPMode:       "block",
				UDPNote:       "non-DNS UDP blocked; WebRTC/Google Meet may stutter",
			}
```

- [ ] **Step 5: Run status tests**

Run:

```bash
go test ./internal/stats
```

Expected: pass.

- [ ] **Step 6: Commit**

Run:

```bash
git add internal/stats/render.go internal/stats/stats_test.go internal/supervisor/run.go
git commit -m "feat: show udp policy in status"
```

---

### Task 2: Client Doctor UDP Hint

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing doctor test**

In `internal/cli/cli_test.go`, inside `TestClientDoctorJSONReport` after the existing `config_readable` assertion, add:

```go
	if got := findCheck(rep.Checks, "udp_policy"); got.Status != "warn" || !strings.Contains(got.Hint, "Google Meet") {
		t.Fatalf("udp_policy = %+v, want warn with Google Meet hint", got)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/cli -run TestClientDoctorJSONReport
```

Expected: fail because the `udp_policy` check is missing.

- [ ] **Step 3: Implement doctor check**

In `internal/cli/cli.go`, inside `collectClientDoctor`, after the `status_socket` check and before `rep.OK = !rep.hasFail()`, add:

```go
	rep.addCheck(
		"udp_policy",
		"warn",
		"non-DNS UDP blocked",
		"Google Meet/WebRTC may stutter; use sudo bx down on trusted routed networks",
	)
```

- [ ] **Step 4: Run doctor test**

Run:

```bash
go test ./internal/cli -run TestClientDoctorJSONReport
```

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: add udp policy doctor hint"
```

---

### Task 3: Realtime Status Capability

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing command and capabilities test**

In `internal/cli/cli_test.go`, inside `TestCapabilitiesReport` after the `logs` capability assertion, add:

```go
	udpStatus := findCapability(rep.Commands, "bx realtime status")
	if !udpStatus.Stable || udpStatus.RequiresRoot || udpStatus.ChangesSystem || udpStatus.ChangesNetwork {
		t.Fatalf("unexpected realtime status capability: %+v", udpStatus)
	}
	if !strings.Contains(strings.Join(udpStatus.SafeNotes, " "), "UDP") {
		t.Fatalf("realtime status should mention UDP: %+v", udpStatus)
	}
	if got := findCapability(rep.Commands, "sudo bx realtime on"); got.Command != "" {
		t.Fatalf("capabilities should not expose unimplemented realtime on: %+v", got)
	}
```

Also add `realtime` to `TestAppHasVersion`:

```go
	if !appHasCommand(app, "realtime") {
		t.Fatal("app should expose bx realtime")
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/cli -run 'TestAppHasVersion|TestCapabilitiesReport'
```

Expected: fail because the realtime command and capability are missing.

- [ ] **Step 3: Add realtime status command and capability**

In `internal/cli/cli.go`, add the top-level command:

```go
{Name: "realtime", Usage: "查看实时 UDP 策略", Subcommands: realtimeCommands()},
```

Add the command builder:

```go
func realtimeCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "status", Usage: "查看 UDP / 实时应用策略", Action: realtimeStatusAction},
	}
}
```

Add `realtimeStatusAction` near `statusAction`:

```go
func realtimeStatusAction(c *cli.Context) error {
	mode := "block"
	note := "non-DNS UDP blocked; WebRTC/Google Meet may stutter"
	blocked := "unknown"
	if conn, err := net.Dial("unix", supervisor.SockPath); err == nil {
		defer conn.Close()
		var rep stats.Report
		if err := json.NewDecoder(conn).Decode(&rep); err == nil {
			if rep.UDPMode != "" {
				mode = rep.UDPMode
			}
			if rep.UDPNote != "" {
				note = rep.UDPNote
			}
			blocked = fmt.Sprint(rep.UDPBlocked)
		}
	}
	fmt.Println("realtime supported: true")
	fmt.Printf("udp mode: %s\n", mode)
	fmt.Printf("udp blocked: %s\n", blocked)
	fmt.Printf("detail: %s\n", note)
	return nil
}
```

In `capabilities()`, add this entry after `bx logs` and before `bx dns status`. Do not expose unimplemented `on/off` commands:

```go
			{
				Command:        "bx realtime status",
				Category:       "udp",
				Summary:        "Inspect UDP/realtime policy. Current builds block non-DNS UDP, which can affect WebRTC.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"bx realtime status", "bx doctor --json"},
				SafeNotes:      []string{"Read-only.", "UDP policy is currently visible through bx status and bx doctor --json."},
			},
```

- [ ] **Step 4: Run capabilities test**

Run:

```bash
go test ./internal/cli -run 'TestAppHasVersion|TestCapabilitiesReport'
```

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: expose realtime udp capabilities"
```

---

### Task 4: Final Verification and Push

**Files:**
- No new files.

- [ ] **Step 1: Run full tests**

Run:

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 2: Inspect git history and status**

Run:

```bash
git status --short
git log --oneline -5
```

Expected: worktree clean; latest commits are the three UDP diagnostics commits.

- [ ] **Step 3: Push**

Run:

```bash
git push
```

Expected: branch pushes to `origin/master`.

---

## Self-Review

Spec coverage:

- Default UDP remains fail-closed: Task 1 reports `block`, no dialer behavior changes.
- Status/doctor/capabilities disclose UDP policy: Tasks 1, 2, and 3 cover them.
- Realtime command discovery: Task 3 exposes planned commands and risk notes.
- UDP relay implementation: intentionally not covered; this is first-batch diagnostics only.

Placeholder scan:

- No TBD/TODO placeholders.
- Every code change step includes exact snippets.

Type consistency:

- `UDPMode` and `UDPNote` are added to `stats.Report` and set by `serveStats`.
- Tests use existing helpers `findCheck` and `findCapability`.
