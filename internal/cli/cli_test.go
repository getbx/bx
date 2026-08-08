package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/stats"
	"github.com/urfave/cli/v2"
)

func TestAppHasVersion(t *testing.T) {
	app := New()
	if strings.TrimSpace(app.Version) == "" {
		t.Fatal("app version should not be empty")
	}
	if !appHasCommand(app, "logs") {
		t.Fatal("app should expose bx logs")
	}
	logs := findAppCommand(app, "logs")
	if !commandHasFlag(logs, "archive") || !commandHasFlag(logs, "dir") || !commandHasFlag(logs, "json") {
		t.Fatal("logs should expose --archive, --dir and --json")
	}
	if !appHasCommand(app, "dns") {
		t.Fatal("app should expose bx dns")
	}
	if !appHasCommand(app, "realtime") {
		t.Fatal("app should expose bx realtime")
	}
	if !appHasCommand(app, "preset") {
		t.Fatal("app should expose bx preset")
	}
	preset := findAppCommand(app, "preset")
	if !commandHasSubcommand(preset, "ls") || !commandHasSubcommand(preset, "show") || !commandHasSubcommand(preset, "apply") {
		t.Fatalf("preset subcommands = %+v, want ls/show/apply", preset.Subcommands)
	}
	if !appHasCommand(app, "webrtc-check") {
		t.Fatal("app should expose bx webrtc-check")
	}
	webrtc := findAppCommand(app, "webrtc-check")
	if !commandHasFlag(webrtc, "browser") || !commandHasFlag(webrtc, "expected-ip") {
		t.Fatal("webrtc-check should expose --browser and --expected-ip")
	}
	if !appHasCommand(app, "leak-check") {
		t.Fatal("app should expose bx leak-check")
	}
	leak := findAppCommand(app, "leak-check")
	if !commandHasFlag(leak, "json") || !commandHasFlag(leak, "browser") || !commandHasFlag(leak, "expected-ip") || !commandHasFlag(leak, "network") || !commandHasFlag(leak, "network-timeout") {
		t.Fatal("leak-check should expose --json, --browser, --expected-ip, --network and --network-timeout")
	}
	observe := findAppCommand(app, "observe")
	if observe == nil || !commandHasFlag(observe, "json") || !commandHasFlag(observe, "duration") || !commandHasFlag(observe, "interval") {
		t.Fatal("observe should expose --json, --duration and --interval")
	}
	status := findAppCommand(app, "status")
	if !commandHasFlag(status, "json") {
		t.Fatal("status should expose --json")
	}
	if !appHasCommand(app, "reconnect") {
		t.Fatal("app should expose bx reconnect")
	}
	if reconnect := findAppCommand(app, "reconnect"); !commandHasFlag(reconnect, "json") {
		t.Fatal("reconnect should expose --json")
	}
	inspect := findAppCommand(app, "inspect")
	if inspect == nil || !commandHasFlag(inspect, "json") {
		t.Fatal("inspect should expose --json")
	}
	realtime := findAppCommand(app, "realtime")
	if !commandHasSubcommand(realtime, "status") || !commandHasSubcommand(realtime, "on") || !commandHasSubcommand(realtime, "off") {
		t.Fatalf("realtime subcommands = %+v, want status/on/off", realtime.Subcommands)
	}
	if commandHasFlag(realtime, "no-restart") {
		t.Fatal("realtime must not offer a service-restart path")
	}
	if !subcommandHidden(realtime, "on") || !subcommandHidden(realtime, "off") {
		t.Fatal("realtime on/off should stay hidden from the normal help surface")
	}
}

func TestMacMenuReconnectDoesNotCycleProtection(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "menu.addAction(\"Troubleshoot: Reconnect\"") {
		t.Fatal("macOS menu should expose Reconnect only as troubleshooting")
	}
	if !strings.Contains(text, "func reconnectBx()") {
		t.Fatal("macOS menu should implement reconnect action")
	}
	if !strings.Contains(text, "guardianClient.requestRecovery()") {
		t.Fatal("macOS reconnect should submit directly to Guardian")
	}
	if strings.Contains(text, "runPrivileged(\"'\\(bxPath)' reconnect\")") {
		t.Fatal("macOS reconnect must not invoke the CLI through privileged AppleScript")
	}
	if strings.Contains(text, "'\\(bxPath)' down &&") {
		t.Fatal("macOS reconnect must not cycle bx down && bx up")
	}
}

func TestReconnectSubmitsOncePollsToSuccessAndPrintsHumanProgress(t *testing.T) {
	var output bytes.Buffer
	client := &scriptedRecoveryClient{
		submitted: guardian.RecoverySnapshot{
			ID: "recovery-7", State: "accepted", Stage: "queued", Reason: "manual",
		},
		current: []guardian.RecoverySnapshot{
			{ID: "recovery-7", State: "running", Stage: "transport_health", Reason: "manual"},
			{ID: "recovery-7", State: "succeeded", Stage: "succeeded", Reason: "manual"},
		},
	}
	var waits []time.Duration
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: client,
		output: &output,
		wait: func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			return nil
		},
		legacyReconnect: func(context.Context) error {
			t.Fatal("successful Guardian recovery must not use legacy Core reconnect")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 1 {
		t.Fatalf("Guardian submit calls = %d, want 1", client.submitCalls)
	}
	if got, want := waits, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}; !reflect.DeepEqual(got, want) {
		t.Fatalf("poll waits = %v, want %v", got, want)
	}
	if got, want := output.String(), "• Protection  Reconnecting\n✓ Protection  Reconnected\n"; got != want {
		t.Fatalf("human output = %q, want %q", got, want)
	}
}

func TestReconnectJSONReturnsFinalRecoverySnapshot(t *testing.T) {
	var output bytes.Buffer
	client := &scriptedRecoveryClient{
		submitted: guardian.RecoverySnapshot{
			ID: "recovery-8", State: "succeeded", Stage: "succeeded", Reason: "manual", Attempt: 2,
		},
	}
	if err := reconnectWithDependencies(context.Background(), true, reconnectDependencies{
		client: client,
		output: &output,
		wait:   func(context.Context, time.Duration) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	var got guardian.RecoverySnapshot
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode reconnect JSON: %v\n%s", err, output.String())
	}
	if got.ID != "recovery-8" || got.State != "succeeded" || got.Attempt != 2 {
		t.Fatalf("final snapshot = %+v", got)
	}
}

func TestReconnectReportsStableGuardianFailureWithoutLegacyFallback(t *testing.T) {
	client := &scriptedRecoveryClient{
		submitted: guardian.RecoverySnapshot{
			ID: "recovery-9", State: "accepted", Stage: "queued", Reason: "manual",
		},
		current: []guardian.RecoverySnapshot{{
			ID: "recovery-9", State: "failed", Stage: "observe", Reason: "manual",
			ErrorCode: "network_unavailable", Detail: "secret underlay detail",
		}},
	}
	legacyCalls := 0
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: client,
		output: io.Discard,
		wait:   func(context.Context, time.Duration) error { return nil },
		legacyReconnect: func(context.Context) error {
			legacyCalls++
			return nil
		},
	})
	if err == nil || err.Error() != "recovery failed: network_unavailable" {
		t.Fatalf("reconnect error = %v, want stable recovery code", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("reconnect error leaked Guardian detail: %v", err)
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy Core reconnect calls = %d, want 0", legacyCalls)
	}
}

func TestReconnectFallsBackOnlyForTypedGuardianUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		requestErr error
		wantLegacy int
		wantError  string
	}{
		{
			name:       "typed unavailable",
			requestErr: &guardian.UnavailableError{Err: errors.New("missing socket")},
			wantLegacy: 1,
		},
		{
			name:       "Guardian business failure",
			requestErr: errors.New("Guardian /v1/recoveries returned 500"),
			wantError:  "Guardian /v1/recoveries returned 500",
		},
		{
			name:       "possibly accepted POST",
			requestErr: &guardian.AmbiguousRecoveryError{Err: io.ErrUnexpectedEOF},
			wantError:  "Guardian recovery request may have been accepted: unexpected EOF",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			legacyCalls := 0
			err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
				client: &scriptedRecoveryClient{requestErr: tt.requestErr},
				output: io.Discard,
				wait:   func(context.Context, time.Duration) error { return nil },
				legacyReconnect: func(context.Context) error {
					legacyCalls++
					return nil
				},
			})
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tt.wantError {
				t.Fatalf("reconnect error = %v, want %q", err, tt.wantError)
			}
			if legacyCalls != tt.wantLegacy {
				t.Fatalf("legacy Core reconnect calls = %d, want %d", legacyCalls, tt.wantLegacy)
			}
		})
	}
}

func TestReconnectLegacyFallbackSuccessPrintsHumanProgress(t *testing.T) {
	var output bytes.Buffer
	legacyCalls := 0
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: &scriptedRecoveryClient{
			requestErr: &guardian.UnavailableError{Err: errors.New("missing Guardian socket")},
		},
		output: &output,
		legacyReconnect: func(context.Context) error {
			legacyCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("legacy reconnect exit error = %v, want nil", err)
	}
	if legacyCalls != 1 {
		t.Fatalf("legacy reconnect calls = %d, want 1", legacyCalls)
	}
	if got, want := output.String(), "• Protection  Reconnecting\n✓ Protection  Reconnected\n"; got != want {
		t.Fatalf("legacy human stdout = %q, want %q", got, want)
	}
}

func TestReconnectLegacyFallbackSuccessPrintsStableJSONOnly(t *testing.T) {
	var output bytes.Buffer
	err := reconnectWithDependencies(context.Background(), true, reconnectDependencies{
		client: &scriptedRecoveryClient{
			requestErr: &guardian.UnavailableError{Err: errors.New("missing Guardian socket")},
		},
		output:          &output,
		legacyReconnect: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("legacy JSON reconnect exit error = %v, want nil", err)
	}
	const want = "{\n  \"state\": \"succeeded\",\n  \"stage\": \"legacy_core\",\n  \"reason\": \"manual\",\n  \"attempt\": 1\n}\n"
	if got := output.String(); got != want {
		t.Fatalf("legacy JSON stdout = %q, want %q", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("legacy stdout is not valid JSON: %v", err)
	}
}

func TestReconnectObservationTimeoutKeepsRecoveryRunning(t *testing.T) {
	client := &scriptedRecoveryClient{submitted: guardian.RecoverySnapshot{
		ID: "recovery-10", State: "accepted", Stage: "queued", Reason: "manual",
	}}
	err := reconnectWithDependencies(context.Background(), false, reconnectDependencies{
		client: client,
		output: io.Discard,
		wait: func(context.Context, time.Duration) error {
			return context.DeadlineExceeded
		},
	})
	if err == nil || err.Error() != "recovery recovery-10 is still running: context deadline exceeded" {
		t.Fatalf("timeout error = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "failed") {
		t.Fatalf("timeout must not claim recovery failure: %v", err)
	}
}

type scriptedRecoveryClient struct {
	submitted   guardian.RecoverySnapshot
	current     []guardian.RecoverySnapshot
	requestErr  error
	currentErr  error
	submitCalls int
	currentCall int
}

func (c *scriptedRecoveryClient) RequestRecovery(context.Context, guardian.RecoveryRequest) (guardian.RecoverySnapshot, error) {
	c.submitCalls++
	return c.submitted, c.requestErr
}

func (c *scriptedRecoveryClient) CurrentRecovery(context.Context) (guardian.RecoverySnapshot, error) {
	if c.currentErr != nil {
		return guardian.RecoverySnapshot{}, c.currentErr
	}
	if c.currentCall >= len(c.current) {
		return c.submitted, nil
	}
	snapshot := c.current[c.currentCall]
	c.currentCall++
	return snapshot, nil
}

func TestMacMenuQuitBxStopsProtectionThenQuitsMenu(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "menu.addAction(quitBxActionTitle") {
		t.Fatal("macOS menu should expose an explicit Quit bx action")
	}

	// Scope the remaining checks to quitBx()'s own body, not the whole file:
	// turnOffBx() also calls performToggle(.turnOff), so a whole-file
	// substring check would pass even if quitBx() called something else
	// entirely.
	start := strings.Index(text, "func quitBx()")
	if start == -1 {
		t.Fatal("macOS menu should define quitBx()")
	}
	end := strings.Index(text[start:], "func turnOffBx()")
	if end == -1 {
		t.Fatal("could not find the end of quitBx() (expected turnOffBx() to follow it)")
	}
	body := text[start : start+end]

	// Quit no longer shells out to `bx down` via AppleScript (that path is
	// synchronous and blocked the menu for 71 minutes during the 2026-08-04
	// incident); it now goes through performToggle(.turnOff), which talks to
	// Guardian over the socket on a background queue.
	if !strings.Contains(body, "performToggle(.turnOff)") {
		t.Fatal("Quit bx should use the safe Guardian turnOff path")
	}
	// Quit closes the menu only after bx actually stopped. The terminate call
	// moved out of quitBx() into finishQuit(turnedOff:) when the forced-teardown
	// escape hatch was restored, so both halves are asserted: quitBx must route
	// its outcome there, and finishQuit must gate terminate on
	// quitTerminatesAfterTurnOff. That is strictly more than the previous
	// "quitBx mentions NSApp.terminate" check — an unconditional terminate in
	// quitBx would now fail, and it used to pass.
	if !strings.Contains(body, "finishQuit(turnedOff:") {
		t.Fatal("Quit bx should close the menu only after bx stops (via finishQuit(turnedOff:))")
	}
	quitStart := strings.Index(text, "func finishQuit(turnedOff: Bool)")
	if quitStart == -1 {
		t.Fatal("macOS menu should define finishQuit(turnedOff:)")
	}
	quitEnd := strings.Index(text[quitStart:], "\n    private func performToggle")
	if quitEnd == -1 {
		t.Fatal("could not find the end of finishQuit() (expected performToggle() to follow it)")
	}
	finishBody := text[quitStart : quitStart+quitEnd]
	if !strings.Contains(finishBody, "NSApp.terminate(nil)") {
		t.Fatal("finishQuit should terminate the menu once bx stopped")
	}
	// Terminating after a failed turn-off would leave protection running with
	// no menu bar indicator — the invisible-protection state this project has
	// repeatedly refused to ship (Quit Menu was deleted for exactly this).
	if !strings.Contains(finishBody, "quitTerminatesAfterTurnOff(turnedOff:") {
		t.Fatal("finishQuit must not terminate when the turn-off failed (no invisible protection)")
	}
	// A toggle may already be in flight when Quit is clicked (Turn Off
	// stuck, or Start Protection still connecting). performToggle's
	// re-entrancy guard makes a second direct call silently no-op, so
	// quitBx must consult quitDisposition and queue instead of calling
	// performToggle unconditionally (2026-08-07 fix round 1: quitBx used to
	// swallow the confirmed quit with no error and no exit whenever a
	// toggle was already running).
	if !strings.Contains(body, "quitDisposition(inFlight:") {
		t.Fatal("Quit bx must account for an in-flight toggle via quitDisposition, not call performToggle unconditionally")
	}
}

// 退出入口铺到每个状态之后,「没东西可关」的那几个状态必须能直接退出。
//
// .notInstalled / .missing / .setupNeeded 的定义就是「Guardian 不在那儿」(没装
// app、没有 /usr/local/bin/bx、没跑过 setup 因而 service_installed 为 fail)。走
// performToggle(.turnOff) 是一次注定失败的 socket 调用,逃生路径那次
// sudo bx down 同样注定失败,而 quitTerminatesAfterTurnOff 于是**拒绝退出** ——
// 恰恰是在退出入口刚刚变得可见的那几个状态里,点它什么都不会发生,用户被困在
// 一个关不掉的菜单里,却根本没有保护需要被守着。
//
// 判定本身(哪些状态算「没东西可关」、.off 为什么不算)在 ToggleController.swift
// 的 quitPlan 里由 QuitPlanTests 钉着;这条守卫只管**接线**——quitBx 必须先问
// quitPlan、并在它说 terminateImmediately 时真的 terminate。接线漏掉不会有任何
// 编译错误,而 main.swift 编不进 scripts/test-macos-menu.sh。
func TestMacMenuQuitTerminatesWhenThereIsNothingToTurnOff(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	body, ok := swiftFunctionBody(text, "private func quitBx()")
	if !ok {
		t.Fatal("找不到 quitBx 的函数体")
	}
	// ① 判定必须被问到,且**方向不能反**。把 `== .terminateImmediately` 改成
	// `!=` 能编译、能通过每一条 Go 与 Swift 测试,效果却是 `.connected` 下点 Quit
	// 立刻终止进程 —— 保护还开着而菜单栏图标没了,正是本项目一贯拒绝交付的那个
	// 状态。所以钉的是**配对**:terminateImmediately ⇒ terminate,其余 ⇒ 关闭路径。
	lines := strings.Split(body, "\n")
	planLine, planAt := "", -1
	for i, line := range lines {
		if strings.Contains(line, "quitPlan(state:") {
			planLine, planAt = line, i
		}
	}
	if planAt < 0 {
		t.Fatal("quitBx 必须先问 quitPlan:没东西可关时那次 turnOff 注定失败,而失败之后按阶段①的裁决又不退出")
	}
	if !strings.Contains(planLine, "== .terminateImmediately") {
		t.Fatalf("quitPlan 的比较方向必须是 `== .terminateImmediately`:反过来会让 .connected 下点 Quit 直接终止进程,"+
			"保护还开着而指示灯没了。实际那一行 = %q", strings.TrimSpace(planLine))
	}
	if strings.Contains(planLine, "!=") {
		t.Fatalf("quitPlan 那一行不得出现 `!=`,实际 = %q", strings.TrimSpace(planLine))
	}
	// 配对的另一半:这个分支里紧接着必须就是 terminate,不能是别的动作。
	next := ""
	for _, line := range lines[planAt+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		next = trimmed
		break
	}
	if next != "NSApp.terminate(nil)" {
		t.Fatalf("terminateImmediately 那个分支必须紧接着 NSApp.terminate(nil),实际下一句 = %q", next)
	}
	// 顺序是关键:quitPlan 必须问在 quitDisposition/performToggle 之前,否则那次
	// 注定失败的 socket 调用照样发出去,直接退出这条路永远走不到。
	plan := strings.Index(body, "quitPlan(state:")
	disposition := strings.Index(body, "quitDisposition(inFlight:")
	if disposition < 0 {
		t.Fatal("其余状态必须仍走关闭路径(quitDisposition),不得被这条捷径吃掉")
	}
	if plan > disposition {
		t.Fatal("quitPlan 必须问在 quitDisposition 之前,否则注定失败的 turnOff 照样先发出去了")
	}
	// terminate 只许出现在 quitPlan 那个分支里:落在它前面就是无条件退出,
	// 那会把「关不掉就不退出」这条阶段①的裁决整个废掉(保护在跑却没有指示灯)。
	if terminate := strings.Index(body, "NSApp.terminate(nil)"); terminate < 0 || terminate < plan {
		t.Fatal("quitBx 里的 terminate 必须由 quitPlan 把关,不得无条件退出")
	}

	// ② 映射必须**真的逐 case 覆盖 BxState**。此前这里只查函数存在,于是把函数体
	// 整个换成 `return .connected`(签名不动、穷尽性 switch 没了)照样绿,而
	// quitPlan 从此对每一个状态都收到错的输入 —— 守卫的注释宣称自己钉的是
	// 「逐 case 写在 menuStateKind 里」,它并没有。现在按 BxState 的真实 case 列表逐条查。
	mapping, ok := swiftFunctionBody(text, "private func menuStateKind() -> MenuStateKind")
	if !ok {
		t.Fatal("找不到 menuStateKind 的函数体")
	}
	if !strings.Contains(mapping, "switch state") {
		t.Fatal("menuStateKind 必须是一个对 state 的 switch,漏掉新 case 才会被编译器拦下")
	}
	cases := swiftEnumCaseNames(text, "enum BxState {")
	if len(cases) < 7 {
		t.Fatalf("BxState 的 case 一条都没解析出来或少得可疑(%v),守卫等于没跑", cases)
	}
	for _, name := range cases {
		if !strings.Contains(mapping, "case ."+name) {
			t.Errorf("menuStateKind 漏了 BxState 的 .%s —— quitPlan 会收到一个错的状态", name)
			continue
		}
		if name == "off" {
			// `.off` 是唯一不做同名映射的:它按来路分成两支,分开裁决(见 OffOrigin)。
			// 塌回一个结论就是本轮修复前那个「Guardian 已停也去弹授权框」的行为。
			for _, want := range []string{".offGuardianResponding", ".offServiceStopped"} {
				if !strings.Contains(mapping, want) {
					t.Errorf(".off 必须按来路分成两支(缺 %s):两条来路证据强度不同,合并会让界面断言一件不成立的事", want)
				}
			}
			continue
		}
		// 其余全是同名映射。把 `case .missing: return .connected` 这类错接抓住。
		mapped := false
		for _, line := range strings.Split(mapping, "\n") {
			if strings.Contains(line, "case ."+name) && strings.Contains(line, "return ."+name) {
				mapped = true
			}
		}
		if !mapped {
			t.Errorf("menuStateKind 必须把 BxState 的 .%s 映射到同名的 MenuStateKind.%s", name, name)
		}
	}
}

// `.off` 的两条来路必须**各自贴对标签**。
//
// quitPlan 对两支的裁决相反(信念 → 先关;新鲜观测 → 直接退出),所以标错等于
// 把裁决整个接反,而这不会有任何编译错误:两个 OffOrigin 类型相同、互换照样编译。
// 判定住在 quitPlan(QuitPlanTests 钉着),标签贴在 main.swift 的两个构造点上,
// 那里编不进 Swift 测试套件。
//
// 证据在代码里就摆着:loadState 那一支是 `bx status --json` 跑通、报告解码成功、
// menuProtectionVerdict 说保护关着 —— Guardian 就在应答,是个信念;diagnoseStopped
// 那一支是 status 已经失败之后,doctor 又观测到 service_active != ok。
func TestMacMenuOffOriginsMatchTheirEvidence(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, tt := range []struct {
		fn, want, reject, why string
	}{
		{
			fn: "private func loadState(", want: ".off(.guardianResponding)", reject: ".off(.serviceStopped)",
			why: "status --json 跑通、Guardian 在应答 —— 这是一个可能过时的信念,Quit 必须先关",
		},
		{
			fn: "private func diagnoseStopped(", want: ".off(.serviceStopped)", reject: ".off(.guardianResponding)",
			why: "status --json 已失败 + doctor 刚看到 launchd job 没装载 —— 两条新鲜否定观测,Quit 应直接退出",
		},
	} {
		body, ok := swiftFunctionBody(text, tt.fn)
		if !ok {
			t.Fatalf("找不到 %s 的函数体", tt.fn)
		}
		if !strings.Contains(body, tt.want) {
			t.Errorf("%s 必须构造 %s:%s", tt.fn, tt.want, tt.why)
		}
		if strings.Contains(body, tt.reject) {
			t.Errorf("%s 不得构造 %s —— 标错来路会把 quitPlan 的裁决整个接反(%s)", tt.fn, tt.reject, tt.why)
		}
	}
}

// 取一个 Swift enum 声明里所有 case 的名字(忽略关联值)。
func swiftEnumCaseNames(source, signature string) []string {
	body, ok := swiftFunctionBody(source, signature)
	if !ok {
		return nil
	}
	var names []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "case ") {
			continue
		}
		name := strings.TrimPrefix(trimmed, "case ")
		name = strings.TrimSpace(name)
		if cut := strings.IndexAny(name, "(:, \t"); cut >= 0 {
			name = name[:cut]
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Turning off must keep a working escape path when the Guardian socket call
// fails. `bx down` is the CLI that owns forcedMacOSTeardown (stop Core, bootout
// Guardian, remove the blocking barrier routes, restore DNS); the menu's socket
// call has no equivalent, and the state that needs it is real — with Guardian
// dead and Core alive the menu renders .warning, whose Turn Off and Quit both
// die at connect(). Stopping must never depend on first succeeding at something
// else (2026-08-04 incident).
//
// The escape is asserted here rather than in the Swift suites because it lives
// in main.swift, which scripts/test-macos-menu.sh cannot compile. The decision
// itself (toggleEscape / quitTerminatesAfterTurnOff) is unit-tested there.
func TestMacMenuTurnOffFallsBackToPrivilegedDown(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "private func performToggle")
	if start == -1 {
		t.Fatal("macOS menu should define performToggle")
	}
	end := strings.Index(text[start:], "private func startToggleTicker")
	if end == -1 {
		t.Fatal("could not find the end of performToggle (expected startToggleTicker to follow it)")
	}
	body := text[start : start+end]

	if !strings.Contains(body, "toggleEscape(action:") {
		t.Fatal("performToggle must consult toggleEscape when the socket call fails")
	}
	if !strings.Contains(body, "privilegedTurnOffScript(bxPath:") {
		t.Fatal("the escape path must run the privileged `bx down`, which owns forcedMacOSTeardown")
	}
	// The privileged fallback is synchronous (it blocks until the user answers
	// the authorization prompt). Running it on the main thread reproduces the
	// frozen menu the whole async rewrite existed to prevent, so it must stay
	// on the background queue — i.e. before the hop back to main.
	escape := strings.Index(body, "runPrivilegedScriptOffMainThread")
	if escape == -1 {
		t.Fatal("the privileged fallback must run off the main thread")
	}
	mainHop := strings.Index(body, "DispatchQueue.main.async")
	if mainHop == -1 || escape > mainHop {
		t.Fatal("the privileged fallback must run before performToggle hops back to the main queue")
	}
}

func TestMacMenuUsesGuardianInsteadOfCLIReconnectSupport(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "private let guardianClient = GuardianClient()") {
		t.Fatal("macOS menu should own a fixed Guardian client")
	}
	if strings.Contains(text, "func cliSupportsSafeReconnect()") || strings.Contains(text, "func runtimeSupportsSafeReconnect()") {
		t.Fatal("macOS menu must not probe the legacy CLI/Core reconnect path")
	}
}

// The menu must read DNS state only from the authoritative `bx status --json`
// report. A second source (shelling out to `bx dns status` and parsing its
// lines) can disagree with Guardian and paint the shield green while DNS is
// unmanaged, which is exactly the leak this guard prevents from returning.
func TestMacMenuDerivesDNSFromStatusJSONOnly(t *testing.T) {
	menuSource, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	// BxReport 已从 main.swift 移到 StatusReport.swift(它必须能被单测覆盖:
	// 把 Core 字段声明成非可选曾让 bx down 后的 partial 报告整份解码失败)。
	// 不变量不变——DNS 判定只能来自 bx status --json——只是字段现在住在另一个文件。
	reportSource, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "StatusReport.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(menuSource) + "\n" + string(reportSource)
	if strings.Contains(text, "loadDNSStatus") {
		t.Fatal("macOS menu must not keep a second DNS status source (loadDNSStatus)")
	}
	if strings.Contains(text, `runBx(["dns", "status"])`) {
		t.Fatal("macOS menu must not shell out to `bx dns status` for menu state")
	}
	if !strings.Contains(text, "dnsPresentation(") {
		t.Fatal("macOS menu should derive its DNS verdict from dnsPresentation")
	}
	for _, key := range []string{`case dnsState = "dns_state"`, `case dnsManaged = "dns_managed"`, `case dnsService = "dns_service"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("macOS menu should decode the authoritative status field: %s", key)
		}
	}
}

// The CLI and the macOS menu both render DNS state to a human, in two languages
// that cannot share code. They must not drift into saying the same state two
// different ways, so pin the wording on both sides. The raw enum stays in
// `bx status --json` for machines.
func TestGuardianDNSLabelMatchesMenuWording(t *testing.T) {
	cases := []struct {
		state   guardian.DNSState
		service string
		want    string
	}{
		{guardian.DNSManaged, "Wi-Fi", "Wi-Fi managed"},
		{guardian.DNSManaged, "", "Managed"},
		{guardian.DNSUnmanaged, "", "Not managed"},
		{guardian.DNSUnmanaged, "Wi-Fi", "Not managed"},
		{guardian.DNSUnknown, "", "Status unavailable"},
		{"", "", "Status unavailable"},
	}
	for _, tc := range cases {
		if got := guardianDNSLabel(tc.state, tc.service); got != tc.want {
			t.Errorf("guardianDNSLabel(%q, %q) = %q, want %q", tc.state, tc.service, got, tc.want)
		}
	}

	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "StatusPresentation.swift"))
	if err != nil {
		t.Fatal(err)
	}
	swift := string(source)
	for _, literal := range []string{`"\(service) managed"`, `"Managed"`, `"Not managed"`, `"Status unavailable"`} {
		if !strings.Contains(swift, literal) {
			t.Errorf("StatusPresentation.swift should render DNS with the same wording as the CLI: %s", literal)
		}
	}
}

// updateIcon lets a live recovery snapshot override the state-derived indicator,
// so a warning verdict must drop any snapshot that would paint the shield green
// — otherwise the icon shows green while the tunnel is unhealthy or DNS is
// unmanaged. loadState is an AppKit method that shells out, so this wiring has no
// unit test; pin it at the source level instead.
func TestMacMenuWarningsDropGreenRecoverySnapshot(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	// 签名按前缀匹配:loadState 现在带参数(它跑在后台线程,输入必须由参数带进来
	// 而不是从 self 读)。断言本身一个字没改。
	body, ok := swiftFunctionBody(string(source), "private func loadState(")
	if !ok {
		t.Fatal("could not locate loadState in main.swift")
	}
	// Structural, not literal: every `return .warning(` inside loadState must be
	// immediately preceded by an assignment to recoverySnapshot. Pinning the exact
	// snippet instead made this guard fail the moment the branch stopped hardcoding
	// its reason string, even though the invariant still held — the failure said
	// "not what I remember" rather than "you broke the protection".
	// Walk back over the plain assignments and comments that may sit between the
	// snapshot handling and the return (e.g. repairVersions), and require that one
	// of them assigned recoverySnapshot. Anything else — a guard, an if, a case —
	// ends the window: the branch reached its return without touching the snapshot.
	assignment := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]* = `)
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "return .warning(") {
			continue
		}
		filtered := false
		var lastSeen string
		for j := i - 1; j >= 0; j-- {
			previous := strings.TrimSpace(lines[j])
			if previous == "" || strings.HasPrefix(previous, "//") {
				continue
			}
			if strings.HasPrefix(previous, "recoverySnapshot = ") {
				filtered = true
				break
			}
			if !assignment.MatchString(previous) {
				lastSeen = previous
				break
			}
		}
		if !filtered {
			t.Fatalf("loadState line %d returns a warning without first filtering the recovery snapshot;\n  return: %s\n  branch begins: %s",
				i+1, strings.TrimSpace(line), lastSeen)
		}
	}
}

// swiftFunctionBody returns the brace-balanced body of the named Swift function.
func swiftFunctionBody(source, signature string) (string, bool) {
	start := strings.Index(source, signature)
	if start < 0 {
		return "", false
	}
	open := strings.Index(source[start:], "{")
	if open < 0 {
		return "", false
	}
	open += start
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[open+1 : i], true
			}
		}
	}
	return "", false
}

func TestMacMenuAndReadmeDescribeAutomaticSafeNetworkRecovery(t *testing.T) {
	menu, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(menu), "Automatically recovers safely after network changes") {
		t.Fatal("macOS menu should describe automatic safe recovery after network changes")
	}
	for _, want := range []string{"网络变化后自动安全恢复", "`bx reconnect`", "troubleshooting", "绝不回落直连"} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing recovery guidance %q", want)
		}
	}
}

func TestDarwinTestkitNetworkTransitionCheckIsDryRunAndUserGated(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "darwin-testkit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"--network-transition-check",
		"--acknowledge-physical-change",
		"before-status.json",
		"after-status.json",
		"user-sequence.txt",
		"NETWORK-CHANGED",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("network transition testkit missing %q", want)
		}
	}
	start := strings.Index(text, "run_network_transition_check() {")
	if start < 0 {
		t.Fatal("network transition check function is missing")
	}
	end := strings.Index(text[start:], "\n}\n\n")
	if end < 0 {
		t.Fatal("network transition check function end is missing")
	}
	body := text[start : start+end]
	for _, want := range []string{
		`if [[ "$EXECUTE" != "1" ]]`,
		`if [[ "$ACKNOWLEDGE_PHYSICAL_CHANGE" != "1" ]]`,
		`"$BX" status --json`,
		`"protection_state"[[:space:]]*:[[:space:]]*"protected"`,
		`"tunnel_healthy"[[:space:]]*:[[:space:]]*true`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("network transition check body missing %q", want)
		}
	}
	for _, forbidden := range []string{"networksetup -setairport", "airport -z", `"$BX" reconnect`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("network transition check actively changes the network with %q", forbidden)
		}
	}
}

func TestDarwinTestkitNetworkTransitionModeTerminatesBeforeLegacyHarness(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "darwin-testkit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	dispatch := strings.Index(text, `if [[ "$NETWORK_TRANSITION_CHECK" == "1" ]]`)
	legacy := strings.Index(text, "PLAN_ARGS=(darwin-plan")
	if dispatch < 0 || legacy < 0 || dispatch >= legacy {
		t.Fatalf("network transition dispatch=%d legacy harness=%d", dispatch, legacy)
	}
	between := text[dispatch:legacy]
	if !strings.Contains(between, "run_network_transition_check\n  exit 0") {
		t.Fatalf("network transition mode can fall through into legacy harness:\n%s", between)
	}
}

func TestDarwinTestkitNetworkTransitionLogDirectoryIsUniqueAndPrivate(t *testing.T) {
	tmp := t.TempDir()
	first := runDarwinTransitionFixture(t, tmp)
	second := runDarwinTransitionFixture(t, tmp)
	firstDir := transitionLogDir(t, first)
	secondDir := transitionLogDir(t, second)
	t.Cleanup(func() {
		_ = os.RemoveAll(firstDir)
		_ = os.RemoveAll(secondDir)
	})
	if firstDir == secondDir {
		t.Fatalf("default transition log directory was reused: %s", firstDir)
	}
	for _, dir := range []string{firstDir, secondDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("transition log directory mode = %04o, want 0700", got)
		}
	}
}

func TestDarwinTestkitRejectsExistingOrSymlinkLogDirectory(t *testing.T) {
	tmp := t.TempDir()
	existing := filepath.Join(tmp, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(tmp, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{existing, symlink} {
		cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
			"--network-transition-check", "--log-dir", path)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("existing log path %q was accepted:\n%s", path, output)
		}
		if !strings.Contains(string(output), "log directory must not already exist") {
			t.Fatalf("existing log path error = %q", output)
		}
	}
}

func TestDarwinTestkitRejectsSymlinkedLogDirectoryParent(t *testing.T) {
	root := trustedRepoTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(target, linkedParent); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
		"--network-transition-check", "--log-dir", filepath.Join(linkedParent, "logs"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("symlinked log parent was accepted:\n%s", output)
	}
	if !strings.Contains(string(output), "log directory parent must not contain symbolic links") {
		t.Fatalf("symlinked log parent error = %q", output)
	}
}

func TestDarwinTestkitRejectsGroupOrWorldWritableLogDirectoryParent(t *testing.T) {
	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "group writable", mode: 0o770},
		{name: "world writable", mode: 0o702},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := trustedRepoTempDir(t)
			untrusted := filepath.Join(root, "untrusted")
			if err := os.Mkdir(untrusted, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(untrusted, tt.mode); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
				"--network-transition-check", "--log-dir", filepath.Join(untrusted, "logs"))
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s log parent was accepted:\n%s", tt.name, output)
			}
			if !strings.Contains(string(output), "log directory parent must not be group/other writable") {
				t.Fatalf("%s log parent error = %q", tt.name, output)
			}
		})
	}
}

func TestDarwinTestkitAcceptsPrivateUserOwnedLogDirectoryParent(t *testing.T) {
	root := trustedRepoTempDir(t)
	logDir := filepath.Join(root, "logs")
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
		"--network-transition-check", "--log-dir", logDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("private user-owned log parent was rejected: %v\n%s", err, output)
	}
	info, err := os.Stat(logDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("custom transition log directory mode = %04o, want 0700", got)
	}
}

func TestDarwinTestkitExecutedTransitionFixtureRunsOnlyStatusSnapshots(t *testing.T) {
	tmp := trustedRepoTempDir(t)
	fakeBX := filepath.Join(tmp, "bx-fixture")
	fixture := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$0.args"
count=0
if [[ -f "$0.count" ]]; then count="$(cat "$0.count")"; fi
count=$((count + 1))
printf '%s\n' "$count" >"$0.count"
generation=wifi-a
if [[ "$count" -gt 1 ]]; then generation=wifi-b; fi
printf '{"protection_state":"protected","tunnel_healthy":true,"network_generation":"%s"}\n' "$generation"
`
	if err := os.WriteFile(fakeBX, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(tmp, "transition-logs")
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"),
		"--network-transition-check", "--execute", "--acknowledge-physical-change",
		"--bx", fakeBX, "--log-dir", logDir)
	cmd.Stdin = strings.NewReader("NETWORK-CHANGED\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture transition: %v\n%s", err, output)
	}
	args, err := os.ReadFile(fakeBX + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(args), "status --json\nstatus --json\n"; got != want {
		t.Fatalf("fixture bx calls = %q, want %q", got, want)
	}
	if strings.Contains(string(output), "darwin-plan") {
		t.Fatalf("transition fixture reached legacy harness:\n%s", output)
	}
}

func trustedRepoTempDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(wd, ".darwin-testkit-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func runDarwinTransitionFixture(t *testing.T, tmp string) string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "darwin-testkit.sh"), "--network-transition-check")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("network transition dry-run: %v\n%s", err, output)
	}
	return string(output)
}

func transitionLogDir(t *testing.T, output string) string {
	t.Helper()
	const marker = "Dry-run complete. Logs: "
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, marker) {
			return strings.TrimPrefix(line, marker)
		}
	}
	t.Fatalf("dry-run output omitted log directory:\n%s", output)
	return ""
}

func TestMacMenuUsesSingleCompactStatusIcon(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "compactStatusImage(for:") {
		t.Fatal("macOS menu should render its shield and state indicator as one compact icon")
	}
	if strings.Contains(text, "button.attributedTitle = statusDotTitle") {
		t.Fatal("macOS menu should not consume menu-bar space with a separate status-dot title")
	}
}

func TestMacOSMenuInstallerUsesSingularLaunchdLabels(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install-macos-menu.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		`launchctl bootout "$DOMAIN/$LEGACY_BUNDLE_ID"`,
		`rm -f "$LEGACY_AGENT_DST"`,
		`launchctl bootstrap "$DOMAIN" "$AGENT_DST"`,
		`launchctl kickstart -k "$DOMAIN/$BUNDLE_ID"`,
		`verify_single_agent`,
		`launchctl print "$DOMAIN/$BUNDLE_ID"`,
		`launchctl print "$DOMAIN/$LEGACY_BUNDLE_ID"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("macOS menu installer missing %q", want)
		}
	}
	for _, forbidden := range []string{"pgrep", `launchctl bootout "$DOMAIN" "$LEGACY_AGENT_DST"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("macOS menu installer contains non-singular check %q", forbidden)
		}
	}
}

func TestAppHidesLegacyAndDeveloperCommands(t *testing.T) {
	app := New()
	for _, name := range []string{"restart", "blink", "run", "debug-tun", "darwin-plan", "router-plan"} {
		command := findAppCommand(app, name)
		if command == nil || !command.Hidden {
			t.Fatalf("%s should stay available but hidden from normal help: %+v", name, command)
		}
	}
}

func TestBuildExecStart(t *testing.T) {
	// Linux: 标准路径格式无引号
	got := buildExecStartForGOOS("linux", "/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx run -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("Linux ExecStart 应跑 run, got %q", got)
	}

	// darwin legacy: Guardian 可执行文件在 /usr/local/bin/bx
	got = buildExecStartWith("darwin", "/usr/local/bin/bx", "/etc/bx/config.yaml", "/usr/local/bin/bx")
	want = "/usr/local/bin/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53"
	if got != want {
		t.Fatalf("darwin legacy ExecStart, got %q", got)
	}

	// darwin unified runtime: Guardian 可执行文件在统一 runtime 目录(含空格)
	got = buildExecStartWith("darwin", "/usr/local/bin/bx", "/etc/bx/config.yaml", "/Library/Application Support/bx/runtime/current/bx")
	want = "/Library/Application Support/bx/runtime/current/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53"
	if got != want {
		t.Fatalf("darwin unified runtime ExecStart (with spaces in path), got %q", got)
	}

	// Windows: 含空格路径必须加引号,否则服务 BinaryPathName 拆分会崩。
	got = buildExecStartForGOOS("windows", `C:\Program Files\bx\bx.exe`, `C:\ProgramData\bx\config.yaml`)
	want = `"C:\Program Files\bx\bx.exe" run -c "C:\ProgramData\bx\config.yaml"`
	if got != want {
		t.Fatalf("windows ExecStart 应对含空格路径加引号, got %q", got)
	}
}

func TestBlinkRoundTripThroughCLI(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := blink.Encode(link)
	dec, err := blink.Decode(enc)
	if err != nil || dec != link {
		t.Fatalf("round-trip 失败: %q err=%v", dec, err)
	}
}

func TestNormalizeClientLinkAcceptsRawBrook(t *testing.T) {
	raw := "brook://wssserver?wssserver=wss%3A%2F%2Fvps.example.com%3A443&username&password=pw"
	link, configLink, err := normalizeClientLink(raw)
	if err != nil {
		t.Fatal(err)
	}
	if link != raw {
		t.Fatalf("link = %q, want raw brook link", link)
	}
	if !strings.HasPrefix(configLink, "bx://") {
		t.Fatalf("config link should be normalized to bx://, got %q", configLink)
	}
	decoded, err := blink.Decode(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded config link = %q, want %q", decoded, raw)
	}
}

func TestNormalizeClientLinkAcceptsEncodedBX(t *testing.T) {
	raw := "brook://server?server=1.2.3.4%3A9999&password=pw"
	encoded := blink.Encode(raw)
	link, configLink, err := normalizeClientLink(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if link != raw || configLink != encoded {
		t.Fatalf("link/config = %q/%q, want %q/%q", link, configLink, raw, encoded)
	}
}

func TestNormalizeClientLinkAcceptsVless(t *testing.T) {
	raw := "vless://be625ca6@1.2.3.4:9998?security=reality&pbk=PUB&sid=ab12&sni=www.apple.com&flow=xtls-rprx-vision&fp=chrome"
	link, configLink, err := normalizeClientLink(raw)
	if err != nil {
		t.Fatalf("vless 链接应被接受: %v", err)
	}
	if link != raw {
		t.Fatalf("link = %q, want raw vless link", link)
	}
	if !strings.HasPrefix(configLink, "bx://") {
		t.Fatalf("config link 应换壳成 bx://, got %q", configLink)
	}
	decoded, err := blink.Decode(configLink)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != raw {
		t.Fatalf("decoded config link = %q, want %q", decoded, raw)
	}
}

func TestBXServerLink(t *testing.T) {
	link, err := bxServerLink("example.com", serverConfig{Listen: ":9999", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blink.Decode(link)
	if err != nil {
		t.Fatal(err)
	}
	want := "brook://server?server=example.com%3A9999&password=pw"
	if raw != want {
		t.Fatalf("raw link = %q, want %q", raw, want)
	}
}

func TestBXServerLinkRejectsHostWithPort(t *testing.T) {
	if _, err := bxServerLink("example.com:8443", serverConfig{Listen: ":9999", Password: "pw"}); err == nil {
		t.Fatal("host 带端口应报错,端口应来自 listen")
	}
}

func TestServerFirewallHint(t *testing.T) {
	got := serverFirewallHint(":9998")
	for _, want := range []string{"TCP 9998", "sudo ufw allow 9998/tcp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("firewall hint = %q, want contains %q", got, want)
		}
	}
	if got := serverFirewallHint("bad-listen"); got != "" {
		t.Fatalf("bad listen should not produce hint, got %q", got)
	}
}

func TestOpenUFWRejectsBadListen(t *testing.T) {
	if err := openUFW("bad-listen"); err == nil {
		t.Fatal("bad listen should fail")
	}
}

func TestDoctorHelpers(t *testing.T) {
	if got := boolStatus(true); got != "ok" {
		t.Fatalf("boolStatus(true)=%q", got)
	}
	if got := boolStatus(false); got != "fail" {
		t.Fatalf("boolStatus(false)=%q", got)
	}
	if got := redactLink("bx://secret"); got != "bx://<redacted>" {
		t.Fatalf("redact bx link = %q", got)
	}
	if got := redactLink("brook://server?password=pw"); got != "internal-link:<redacted>" {
		t.Fatalf("redact internal link = %q", got)
	}
	if got := shareDoctorStatus("active", "listening"); got != "ok" {
		t.Fatalf("shareDoctorStatus active/listening = %q", got)
	}
	if got := shareDoctorStatus("inactive", "listening"); got != "warn" {
		t.Fatalf("shareDoctorStatus inactive/listening = %q", got)
	}
	if got := hintForState("inactive", "sudo bx up", "bx logs"); got != "sudo bx up; bx logs" {
		t.Fatalf("hintForState inactive = %q", got)
	}
}

func TestClientDoctorJSONReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	if rep.OK {
		t.Fatal("missing client config should not be ok")
	}
	if rep.Kind != "client" || !rep.SecretsRedacted || rep.ChangesSystem || rep.ChangesNetwork || rep.RequiresRoot {
		t.Fatalf("unexpected client report metadata: %+v", rep)
	}
	if got := findCheck(rep.Checks, "config_readable"); got.Status != "fail" {
		t.Fatalf("config_readable = %+v, want fail", got)
	}
	if got := findCheck(rep.Checks, "udp_policy"); got.Status != "ok" || !strings.Contains(got.Detail, "relayed through bx tunnel") {
		t.Fatalf("udp_policy = %+v, want ok relay by default", got)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed doctorReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json should be parseable: %v\n%s", err, buf.String())
	}
	if parsed.Kind != "client" {
		t.Fatalf("parsed kind = %q", parsed.Kind)
	}
}

func TestClientDoctorIncludesPlatformChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	// terminal_proxy 是 collectTerminalProxyChecks 在所有 GOOS 上都会产出的检查名,
	// 用它证明 collectPlatformChecks 的结果已经并入 doctor(darwin 额外检查不便跨平台断言)。
	if got := findCheck(rep.Checks, "terminal_proxy"); got.Name == "" {
		t.Fatalf("doctor checks missing terminal_proxy platform check: %+v", rep.Checks)
	}
}

func TestCollectClientDoctorWithIncludePlatformChecksToggle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")

	withPlatform := collectClientDoctorWith(path, "example.com:443", 0, true, true)
	if got := findCheck(withPlatform.Checks, "terminal_proxy"); got.Name == "" {
		t.Fatalf("includePlatformChecks=true should include terminal_proxy check: %+v", withPlatform.Checks)
	}

	withoutPlatform := collectClientDoctorWith(path, "example.com:443", 0, true, false)
	if got := findCheck(withoutPlatform.Checks, "terminal_proxy"); got.Name != "" {
		t.Fatalf("includePlatformChecks=false must not include terminal_proxy check: %+v", withoutPlatform.Checks)
	}
}

func TestStatusReportIncludesTruthfulGuardianRecovery(t *testing.T) {
	started := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	rep := assembleClientStatusReport(
		stats.Report{TunnelHealthy: true, LatencyMS: 18},
		guardian.Status{
			SchemaVersion:     1,
			Protection:        guardian.ProtectionProtected,
			DNSState:          guardian.DNSManaged,
			DNSManaged:        true,
			NetworkGeneration: "wifi-b",
			Recovery: guardian.RecoverySnapshot{
				ID:         "recovery-8",
				State:      "running",
				Stage:      "transport_health",
				Reason:     "underlay_changed",
				Generation: "wifi-b",
				Attempt:    2,
				StartedAt:  started,
				UpdatedAt:  started.Add(time.Second),
			},
		},
	)

	var encoded map[string]any
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["protection_state"] != guardian.ProtectionRecovering {
		t.Fatalf("protection_state = %v, want recovering", encoded["protection_state"])
	}
	if encoded["network_generation"] != "wifi-b" {
		t.Fatalf("network_generation = %v, want wifi-b", encoded["network_generation"])
	}
	if encoded["core_available"] != true || encoded["core_evidence"] != "local_status_socket" {
		t.Fatalf("Core evidence = available:%v evidence:%v, want local status socket", encoded["core_available"], encoded["core_evidence"])
	}
	recovery, ok := encoded["recovery"].(map[string]any)
	if !ok || recovery["recovery_id"] != "recovery-8" || recovery["stage"] != "transport_health" {
		t.Fatalf("recovery = %#v", encoded["recovery"])
	}
}

func TestStatusReportIncludesGuardianDNSState(t *testing.T) {
	rep := assembleClientStatusReport(stats.Report{TunnelHealthy: true}, guardian.Status{
		Protection: guardian.ProtectionProtected,
		DNSState:   guardian.DNSManaged,
		DNSManaged: true,
		DNSService: "Wi-Fi",
	})
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\"dns_state\":\"managed\"",
		"\"dns_managed\":true",
		"\"dns_service\":\"Wi-Fi\"",
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("missing %s: %s", want, data)
		}
	}
	if got := renderClientStatus(rep); !strings.Contains(got, "DNS     Wi-Fi managed") {
		t.Fatalf("human status = %q, want managed Wi-Fi DNS", got)
	}
}

func TestDarwinStatusDowngradesProtectedWhenGuardianDNSIsNotManaged(t *testing.T) {
	var legacy guardian.Status
	if err := json.Unmarshal([]byte(`{"protection_state":"protected"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		status guardian.Status
	}{
		{name: "legacy Guardian JSON without dns_state", status: legacy},
		{name: "unknown DNS", status: guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnknown}},
		{name: "unmanaged DNS", status: guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnmanaged}},
		{name: "managed state without managed evidence", status: guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSManaged}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := readClientStatusReportWith(
				func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
				func() (guardian.Status, error) { return tt.status, nil },
				"darwin",
			)
			if err != nil {
				t.Fatal(err)
			}
			if rep.ProtectionState != guardian.ProtectionNeedsAttention {
				t.Fatalf("status = %+v, want needs_attention", rep)
			}
		})
	}

	rep, err := readClientStatusReportWith(
		func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Protection: guardian.ProtectionProtected, DNSState: guardian.DNSUnknown}, nil
		},
		"linux",
	)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProtectionState != guardian.ProtectionProtected {
		t.Fatalf("non-Darwin status = %+v, want protected", rep)
	}
}

func TestStatusUsesGuardianWhenCoreSocketIsUnavailable(t *testing.T) {
	coreCalls := 0
	guardianCalls := 0
	rep, err := readClientStatusReportWith(
		func() (stats.Report, error) {
			coreCalls++
			return stats.Report{}, errors.New("missing Core socket")
		},
		func() (guardian.Status, error) {
			guardianCalls++
			return guardian.Status{
				Protection: guardian.ProtectionNeedsAttention,
				Recovery: guardian.RecoverySnapshot{
					State: "failed", Stage: "verify", ErrorCode: "verification_failed",
				},
			}, nil
		},
		"darwin",
	)
	if err != nil {
		t.Fatalf("status rejected authoritative Guardian state: %v", err)
	}
	if coreCalls != 1 || guardianCalls != 1 {
		t.Fatalf("status calls Core=%d Guardian=%d, want one each", coreCalls, guardianCalls)
	}
	if rep.ProtectionState != guardian.ProtectionNeedsAttention {
		t.Fatalf("partial status = %+v, want Repair Required", rep)
	}
	if got := renderClientStatus(rep); !strings.Contains(got, "Repair Required") {
		t.Fatalf("human status = %q, want Repair Required", got)
	}
}

func TestCoreUnavailableStatusIsPartialAndTruthful(t *testing.T) {
	tests := []struct {
		name      string
		status    guardian.Status
		wantState string
		wantLabel string
	}{
		{
			name:      "Guardian off",
			status:    guardian.Status{Protection: guardian.ProtectionOff, Recovery: guardian.RecoverySnapshot{State: "idle", Stage: "idle"}},
			wantState: guardian.ProtectionOff,
			wantLabel: "Off",
		},
		{
			name: "Guardian blocked",
			status: guardian.Status{
				Protection: guardian.ProtectionBlocked,
				Recovery:   guardian.RecoverySnapshot{State: "failed", Stage: "transport_health", ErrorCode: "transport_unavailable"},
			},
			wantState: guardian.ProtectionBlocked,
			wantLabel: "Blocked",
		},
		{
			name: "Guardian needs attention outranks recovery",
			status: guardian.Status{
				Protection: guardian.ProtectionNeedsAttention,
				Recovery:   guardian.RecoverySnapshot{State: "running", Stage: "verify"},
			},
			wantState: guardian.ProtectionNeedsAttention,
			wantLabel: "Repair Required",
		},
		{
			name: "Guardian recovering",
			status: guardian.Status{
				Protection: guardian.ProtectionRecovering,
				Recovery:   guardian.RecoverySnapshot{State: "running", Stage: "transport_health"},
			},
			wantState: guardian.ProtectionRecovering,
			wantLabel: "Reconnecting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := readClientStatusReportWith(
				func() (stats.Report, error) { return stats.Report{}, errors.New("missing Core socket") },
				func() (guardian.Status, error) { return tt.status, nil },
				"darwin",
			)
			if err != nil {
				t.Fatal(err)
			}
			if rep.ProtectionState != tt.wantState {
				t.Fatalf("protection_state = %q, want %q", rep.ProtectionState, tt.wantState)
			}

			data, err := json.Marshal(rep)
			if err != nil {
				t.Fatal(err)
			}
			var encoded map[string]any
			if err := json.Unmarshal(data, &encoded); err != nil {
				t.Fatal(err)
			}
			if encoded["core_available"] != false || encoded["core_evidence"] != "unavailable" {
				t.Fatalf("Core evidence = available:%v evidence:%v, want unavailable", encoded["core_available"], encoded["core_evidence"])
			}
			for _, key := range []string{"server", "socks_addr", "tunnel_healthy", "latency_ms", "restarts", "udp_mode"} {
				if _, ok := encoded[key]; ok {
					t.Fatalf("partial JSON fabricated unevidenced Core field %q: %s", key, data)
				}
			}

			human := renderClientStatus(rep)
			for _, want := range []string{tt.wantLabel, "Core", "Unavailable", "cannot be verified"} {
				if !strings.Contains(human, want) {
					t.Fatalf("partial human status = %q, want %q", human, want)
				}
			}
			for _, forbidden := range []string{"bx 状态", "kill-switch", "真实 IP", "隧道", "健康"} {
				if strings.Contains(human, forbidden) {
					t.Fatalf("partial human status made unevidenced Core claim %q: %s", forbidden, human)
				}
			}
		})
	}
}

func TestStatusDoesNotClaimProtectedWithoutCoreEvidence(t *testing.T) {
	rep, err := readClientStatusReportWith(
		func() (stats.Report, error) { return stats.Report{}, errors.New("missing Core socket") },
		func() (guardian.Status, error) {
			return guardian.Status{
				Protection: guardian.ProtectionProtected,
				Recovery:   guardian.RecoverySnapshot{State: "idle", Stage: "idle"},
			}, nil
		},
		"darwin",
	)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ProtectionState != guardian.ProtectionNeedsAttention {
		t.Fatalf("Core-less status = %+v, want fail-closed needs_attention", rep)
	}
}

func TestCLIRepairRequiredOutranksTransientRecovery(t *testing.T) {
	rep := assembleClientStatusReport(stats.Report{TunnelHealthy: true}, guardian.Status{
		Protection: guardian.ProtectionNeedsAttention,
		Recovery:   guardian.RecoverySnapshot{State: "accepted", Stage: "queued"},
	})
	if rep.ProtectionState != guardian.ProtectionNeedsAttention {
		t.Fatalf("protection = %q, want needs_attention", rep.ProtectionState)
	}
	if got := renderClientStatus(rep); !strings.Contains(got, "Repair Required") || strings.Contains(got, "Status  Reconnecting") {
		t.Fatalf("human status = %q, want Repair Required precedence", got)
	}
}

func TestHumanStatusDistinguishesRecoverySafetyStates(t *testing.T) {
	tests := []struct {
		name       string
		protection string
		recovery   guardian.RecoverySnapshot
		want       string
	}{
		{
			name:       "reconnecting",
			protection: guardian.ProtectionRecovering,
			recovery:   guardian.RecoverySnapshot{State: "running", Stage: "verify"},
			want:       "Reconnecting",
		},
		{
			name:       "blocked",
			protection: guardian.ProtectionBlocked,
			recovery:   guardian.RecoverySnapshot{State: "failed", Stage: "transport_health", ErrorCode: "transport_unavailable"},
			want:       "Blocked",
		},
		{
			name:       "repair required",
			protection: guardian.ProtectionNeedsAttention,
			recovery:   guardian.RecoverySnapshot{State: "failed", Stage: "verify", ErrorCode: "verification_failed"},
			want:       "Repair Required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderClientStatus(clientStatusReport{
				Report:          &stats.Report{TunnelHealthy: true},
				CoreAvailable:   true,
				CoreEvidence:    "local_status_socket",
				ProtectionState: tt.protection,
				Recovery:        tt.recovery,
			})
			if !strings.Contains(got, tt.want) {
				t.Fatalf("status output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDarwinStatusFallbackRequiresRepairWhenGuardianIsUnavailable(t *testing.T) {
	status := guardianStatusFallback(stats.Report{TunnelHealthy: true}, "darwin")
	if status.Protection != guardian.ProtectionNeedsAttention {
		t.Fatalf("darwin fallback protection = %q, want needs_attention", status.Protection)
	}
	if status.Recovery.State != "failed" || status.Recovery.ErrorCode != "recovery_unavailable" {
		t.Fatalf("darwin fallback recovery = %+v", status.Recovery)
	}

	linux := guardianStatusFallback(stats.Report{TunnelHealthy: true}, "linux")
	if linux.Protection != guardian.ProtectionProtected {
		t.Fatalf("linux fallback protection = %q, want protected", linux.Protection)
	}
}

func TestDoctorReportsLatestRecoveryWithoutDirectFallback(t *testing.T) {
	check := recoveryDoctorCheck(guardian.RecoverySnapshot{
		ID:         "recovery-8",
		State:      "failed",
		Stage:      "transport_health",
		Reason:     "underlay_changed",
		Generation: "wifi-b",
		ErrorCode:  "transport_unavailable",
		Attempt:    3,
	})
	if check.Name != "network_recovery" || check.Status != "warn" {
		t.Fatalf("recovery doctor check = %+v", check)
	}
	if !strings.Contains(check.Detail, "stage=transport_health") ||
		!strings.Contains(check.Detail, "error_code=transport_unavailable") {
		t.Fatalf("recovery doctor detail = %q", check.Detail)
	}
	guidance := strings.ToLower(check.Hint)
	if strings.Contains(guidance, "direct") || strings.Contains(guidance, "fallback") {
		t.Fatalf("doctor suggested unsafe fallback: %q", check.Hint)
	}
	if !strings.Contains(guidance, "bx logs") || !strings.Contains(guidance, "bx reconnect") {
		t.Fatalf("doctor hint = %q, want logs and troubleshooting reconnect", check.Hint)
	}
}

func TestGuardianDNSDoctorCheck(t *testing.T) {
	managed := guardianDNSDoctorCheck(guardian.Status{
		DNSState: guardian.DNSManaged, DNSManaged: true, DNSService: "Wi-Fi",
	})
	if managed.Status != "ok" {
		t.Fatalf("managed = %+v", managed)
	}
	unmanaged := guardianDNSDoctorCheck(guardian.Status{DNSState: guardian.DNSUnmanaged})
	if unmanaged.Status != "fail" || unmanaged.Hint == "" {
		t.Fatalf("unmanaged = %+v", unmanaged)
	}
}

func TestClientDoctorReportsProxyUDPPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	got := findCheck(rep.Checks, "udp_policy")
	if got.Status != "ok" || !strings.Contains(got.Detail, "relayed through bx tunnel") || got.Hint != "" {
		t.Fatalf("udp_policy = %+v, want ok proxy relay", got)
	}
}

func TestClientDoctorReportsBlockedUDPPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: block\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := collectClientDoctor(path, "example.com:443", 0, true)
	got := findCheck(rep.Checks, "udp_policy")
	if got.Status != "warn" || !strings.Contains(got.Hint, "Google Meet") || !strings.Contains(got.Hint, "sudo bx realtime on") {
		t.Fatalf("udp_policy = %+v, want block warning with realtime hint", got)
	}
}

func TestClientInspectIncludesDoctorAndStatusError(t *testing.T) {
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	path := filepath.Join(t.TempDir(), "missing.yaml")
	rep := collectClientInspect(path, "example.com:443", 0, true)
	if rep.OK {
		t.Fatal("missing config and missing status socket should not be ok")
	}
	if !rep.SecretsRedacted || rep.ChangesSystem || rep.ChangesNetwork {
		t.Fatalf("unexpected inspect metadata: %+v", rep)
	}
	if rep.Capabilities.Product != "bx" {
		t.Fatalf("capabilities product = %q", rep.Capabilities.Product)
	}
	if rep.Doctor.Kind != "client" {
		t.Fatalf("doctor kind = %q", rep.Doctor.Kind)
	}
	if rep.StatusError == "" {
		t.Fatal("inspect should keep status socket failure as data")
	}
	if len(rep.NextActions) == 0 {
		t.Fatal("inspect should include next actions")
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed inspectReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json should be parseable: %v\n%s", err, buf.String())
	}
}

func TestWebRTCCheckLowRiskWhenUDPRelayed(t *testing.T) {
	cfg := &config.Config{UDP: config.UDP{Mode: "proxy", Transport: "hysteria2://x"}}
	rep := assessWebRTCCheck(cfg, &stats.Report{
		TunnelHealthy: true,
		UDPMode:       "proxy",
		UDPTransport:  "hysteria2@example.com",
	}, nil, install.DNSStatus{Supported: true, Enabled: true, Service: "Wi-Fi"}, nil)
	if !rep.OK || rep.Risk != "low" {
		t.Fatalf("webrtc report = %+v, want ok low", rep)
	}
	if !rep.BrowserVerificationRequired || rep.LeakProof != "not_proven" {
		t.Fatalf("webrtc report should keep browser verification boundary: %+v", rep)
	}
	if got := findCheck(rep.Checks, "udp_path"); got.Status != "ok" || !strings.Contains(got.Detail, "relayed") {
		t.Fatalf("udp_path = %+v, want relayed ok", got)
	}
}

func TestWebRTCCheckHighRiskForDirectUDP(t *testing.T) {
	cfg := &config.Config{UDP: config.UDP{Mode: "direct-realtime"}}
	rep := assessWebRTCCheck(cfg, &stats.Report{
		TunnelHealthy: true,
		UDPMode:       "direct-realtime",
	}, nil, install.DNSStatus{Supported: true, Enabled: true, Service: "Wi-Fi"}, nil)
	if rep.OK || rep.Risk != "high" {
		t.Fatalf("webrtc report = %+v, want high risk", rep)
	}
	if got := findCheck(rep.Checks, "udp_path"); got.Status != "fail" || !strings.Contains(got.Detail, "real network") {
		t.Fatalf("udp_path = %+v, want direct UDP fail", got)
	}
}

func TestApplyBrowserICECandidatesDetectsUnexpectedPublicIP(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.10 55000 typ srflx raddr 192.168.1.5 rport 55000",
		},
	}, []string{"203.0.113.20"})
	if rep.OK || rep.Risk != "high" || rep.LeakProof != "unexpected_public_ip_detected" {
		t.Fatalf("browser candidates should detect unexpected public IP: %+v", rep)
	}
	if got := findCheck(rep.Checks, "browser_unexpected_public_ip"); got.Status != "fail" || !strings.Contains(got.Detail, "203.0.113.10") || strings.Contains(got.Hint, "real public IP") {
		t.Fatalf("browser_unexpected_public_ip = %+v, want unexpected public IP without real-IP overclaim", got)
	}
}

func TestApplyBrowserICECandidatesAllowsExpectedProxyIP(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.20 55000 typ srflx",
		},
	}, []string{"203.0.113.20"})
	if !rep.OK || rep.Risk != "low" || rep.LeakProof != "no_public_leak_detected" {
		t.Fatalf("expected proxy IP should be accepted: %+v", rep)
	}
	if got := findCheck(rep.Checks, "browser_expected_public_ip"); got.Status != "ok" || !strings.Contains(got.Detail, "203.0.113.20") {
		t.Fatalf("browser_expected_public_ip = %+v, want expected proxy IP", got)
	}
}

func TestApplyBrowserICECandidatesWithoutExpectedIPCannotProveSafety(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 203.0.113.10 55000 typ srflx",
		},
	}, nil)
	if rep.OK || rep.Risk != "high" || rep.LeakProof != "public_ip_detected_without_expected" {
		t.Fatalf("public IP without expectation should be inconclusive/high: %+v", rep)
	}
}

func TestApplyBrowserICECandidatesFlagsLANAddress(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 192.168.50.18 55000 typ host",
		},
	}, nil)
	if rep.OK || rep.Risk != "medium" || rep.LeakProof != "local_network_candidate_detected" {
		t.Fatalf("LAN candidate should be medium risk: %+v", rep)
	}
}

func TestApplyBrowserICECandidatesIgnoresUnspecifiedAddress(t *testing.T) {
	rep := webrtcCheckReport{Risk: "low", LeakProof: "not_proven", BrowserVerificationRequired: true}
	applyBrowserICECandidates(&rep, browserICEResult{
		Candidates: []string{
			"candidate:1 1 udp 2122260223 0.0.0.0 9 typ host",
		},
	}, nil)
	if !rep.OK || rep.Risk != "low" || rep.LeakProof != "no_public_leak_detected" {
		t.Fatalf("0.0.0.0 should not count as leak: %+v", rep)
	}
}

func TestAssembleLeakCheckReportAggregatesNetworkRisks(t *testing.T) {
	doctor := doctorReport{Kind: "client", Version: "test", SecretsRedacted: true}
	doctor.addCheck("service_active", "ok", "active", "")
	webrtc := webrtcCheckReport{
		OK:              true,
		Kind:            "webrtc",
		Version:         "test",
		SecretsRedacted: true,
		Risk:            "low",
		LeakProof:       "no_public_leak_detected",
		Checks: []checkReport{
			{Name: "dns", Status: "ok", Detail: "system DNS -> 127.0.0.1"},
			{Name: "udp_path", Status: "ok", Detail: "non-DNS UDP relayed through bx tunnel"},
			{Name: "browser_public_ip", Status: "ok", Detail: "no unexpected public IP"},
		},
	}
	rep := assembleLeakCheckReport(doctor, webrtc, nil)
	if !rep.OK || rep.Risk != "low" {
		t.Fatalf("leak report = %+v, want ok low", rep)
	}
	for _, name := range []string{"service", "dns", "udp", "webrtc", "ipv6", "quic"} {
		if got := findCheck(rep.Checks, name); got.Name == "" {
			t.Fatalf("leak report missing %s: %+v", name, rep.Checks)
		}
	}
}

func TestAssembleLeakCheckReportRaisesRiskForWebRTC(t *testing.T) {
	doctor := doctorReport{Kind: "client", Version: "test", SecretsRedacted: true}
	webrtc := webrtcCheckReport{
		Risk:      "high",
		LeakProof: "unexpected_public_ip_detected",
		Checks: []checkReport{
			{Name: "browser_unexpected_public_ip", Status: "fail", Detail: "203.0.113.10"},
		},
	}
	rep := assembleLeakCheckReport(doctor, webrtc, nil)
	if rep.OK || rep.Risk != "high" {
		t.Fatalf("leak report = %+v, want high", rep)
	}
	if got := findCheck(rep.Checks, "webrtc"); got.Status != "fail" || !strings.Contains(got.Detail, "unexpected_public_ip_detected") {
		t.Fatalf("webrtc aggregate = %+v, want fail proof", got)
	}
}

func TestAssessNetworkProbeAcceptsExpectedIPv4Exit(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{
		IPv4:   "203.0.113.20",
		DNSIPs: []string{"198.18.0.42"},
	}, []string{"203.0.113.20"})
	if !report.OK || report.Risk != "low" {
		t.Fatalf("network probe report = %+v, want ok low", report)
	}
	if got := findCheck(report.Checks, "egress_ipv4"); got.Status != "ok" || !strings.Contains(got.Detail, "203.0.113.20") {
		t.Fatalf("egress_ipv4 = %+v, want expected ok", got)
	}
	if got := findCheck(report.Checks, "dns_resolution"); got.Status != "ok" || !strings.Contains(got.Detail, "fake-IP") {
		t.Fatalf("dns_resolution = %+v, want fake-IP ok", got)
	}
}

func TestAssessNetworkProbeFlagsUnexpectedIPv4Exit(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{IPv4: "203.0.113.10"}, []string{"203.0.113.20"})
	if report.OK || report.Risk != "high" {
		t.Fatalf("network probe report = %+v, want high risk", report)
	}
	if got := findCheck(report.Checks, "egress_ipv4"); got.Status != "fail" || !strings.Contains(got.Detail, "203.0.113.10") {
		t.Fatalf("egress_ipv4 = %+v, want unexpected fail", got)
	}
}

func TestAssessNetworkProbeFlagsPublicIPv6Egress(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{IPv6: "2001:db8::1"}, nil)
	if report.OK || report.Risk != "high" {
		t.Fatalf("network probe report = %+v, want high risk", report)
	}
	if got := findCheck(report.Checks, "egress_ipv6"); got.Status != "fail" || !strings.Contains(got.Detail, "2001:db8::1") {
		t.Fatalf("egress_ipv6 = %+v, want public IPv6 fail", got)
	}
}

func TestAssessNetworkProbeDoesNotTreatIPv4BodyAsIPv6Leak(t *testing.T) {
	report := assessNetworkProbe(networkProbeResult{IPv4: "203.0.113.10", IPv6: "203.0.113.10"}, []string{"203.0.113.10"})
	if !report.OK || report.Risk != "low" {
		t.Fatalf("network probe report = %+v, want low risk", report)
	}
	if got := findCheck(report.Checks, "egress_ipv6"); got.Status != "info" || !strings.Contains(got.Detail, "IPv4 address") {
		t.Fatalf("egress_ipv6 = %+v, want IPv4 body info", got)
	}
}

func TestAssembleLeakCheckReportIncludesNetworkProbe(t *testing.T) {
	doctor := doctorReport{Kind: "client", Version: "test", SecretsRedacted: true}
	doctor.addCheck("service_active", "ok", "active", "")
	webrtc := webrtcCheckReport{OK: true, Risk: "low"}
	network := &networkProbeReport{
		OK:   true,
		Risk: "low",
		Checks: []checkReport{
			{Name: "egress_ipv4", Status: "ok", Detail: "203.0.113.20"},
			{Name: "egress_ipv6", Status: "ok", Detail: "no IPv6 egress observed"},
		},
	}
	rep := assembleLeakCheckReport(doctor, webrtc, network)
	if !rep.OK || rep.Network == nil {
		t.Fatalf("leak report = %+v, want ok with network probe", rep)
	}
	if got := findCheck(rep.Checks, "egress_ipv4"); got.Status != "ok" {
		t.Fatalf("egress_ipv4 aggregate = %+v, want ok", got)
	}
}

func TestAssessObserveWindowWarnsOnUDPBlocks(t *testing.T) {
	start := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 2, Blocked: 0, UDPBlocked: 1, BytesDown: 1000}}
	end := stats.Report{Snapshot: stats.Snapshot{Proxy: 15, Direct: 3, Blocked: 0, UDPBlocked: 4, BytesDown: 9000}, TunnelHealthy: true, UDPMode: "proxy"}
	rep := assessObserveWindow([]stats.Report{start, end}, 30*time.Second)
	if rep.OK || rep.Risk != "medium" {
		t.Fatalf("observe report = %+v, want medium risk", rep)
	}
	if got := findCheck(rep.Checks, "udp_blocks"); got.Status != "warn" || !strings.Contains(got.Detail, "3") {
		t.Fatalf("udp_blocks = %+v, want warn with delta", got)
	}
	if len(rep.Recommendations) == 0 {
		t.Fatalf("observe report should include recommendations: %+v", rep)
	}
}

func TestAssessObserveWindowSuggestsReproducingWhenNoActivity(t *testing.T) {
	start := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 2}}
	end := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 2}, TunnelHealthy: true}
	rep := assessObserveWindow([]stats.Report{start, end}, 30*time.Second)
	if rep.OK || rep.Risk != "medium" {
		t.Fatalf("observe report = %+v, want medium risk", rep)
	}
	if got := findCheck(rep.Checks, "activity"); got.Status != "warn" || !strings.Contains(got.Hint, "reproduce") {
		t.Fatalf("activity = %+v, want reproduce hint", got)
	}
}

func TestAssessObserveWindowVideoScenarioAddsTestSteps(t *testing.T) {
	start := stats.Report{Snapshot: stats.Snapshot{Proxy: 10, Direct: 0}}
	end := stats.Report{Snapshot: stats.Snapshot{Proxy: 20, Direct: 0, BytesDown: 20000}, TunnelHealthy: true, UDPMode: "proxy"}
	rep := assessObserveWindow([]stats.Report{start, end}, 30*time.Second, "video")
	if len(rep.TestSteps) == 0 {
		t.Fatalf("video observe should include test steps: %+v", rep)
	}
	if got := findCheck(rep.Checks, "split_shape"); got.Status != "info" || !strings.Contains(got.Detail, "proxy") {
		t.Fatalf("split_shape = %+v, want proxy info", got)
	}
	if !containsText(rep.Recommendations, "CDN") {
		t.Fatalf("video recommendations should mention CDN: %+v", rep.Recommendations)
	}
}

func TestArchiveClientLogsRecordsReason(t *testing.T) {
	dir, err := archiveClientLogsWithReason(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(dir, "meta.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), "reason=doctor") {
		t.Fatalf("meta should include archive reason:\n%s", meta)
	}
	for _, name := range []string{"doctor.json", "recovery.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("archive should include %s: %v", name, err)
		}
	}
}

func TestPersistRecoverySnapshotRedactsFreeFormTransportError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	secret := "vless://user:password@example.test?token=secret"
	if err := persistRecoverySnapshot(path, guardian.RecoverySnapshot{
		ID:        "recovery-8",
		State:     "failed",
		Stage:     "transport_health",
		ErrorCode: "future_secret_error",
		Detail:    secret,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "future_secret_error") {
		t.Fatalf("recovery archive leaked free-form failure: %s", data)
	}
	var got guardian.RecoverySnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ErrorCode != "recovery_failed" || got.Detail != "" {
		t.Fatalf("recovery archive = %+v, want stable redacted failure", got)
	}
}

func TestAutoArchiveAfterClientCommandRecordsExactError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BX_LOG_ARCHIVE_DIR", root)
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(root, "missing.sock"))

	commandErr := fmt.Errorf("control request failed: %w", context.DeadlineExceeded)
	autoArchiveAfterClientCommand("reconnect", &commandErr, false)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(root, entries[0].Name(), "command-error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "context deadline exceeded\n" {
		t.Fatalf("command error = %q, want exact context deadline error", got)
	}
	if strings.Contains(string(got), "transport failure") {
		t.Fatalf("command error was rewritten as transport failure: %q", got)
	}
}

func TestAutoArchiveAfterClientCommandRedactsErrorDetails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BX_LOG_ARCHIVE_DIR", root)
	t.Setenv("BX_STATUS_SOCKET", filepath.Join(root, "missing.sock"))

	commandErr := errors.New("control-plane secret=token-value server detail")
	autoArchiveAfterClientCommand("reconnect", &commandErr, false)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, entries[0].Name(), "command-error.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "client command failed\n" {
		t.Fatalf("command error = %q, want redacted diagnostic", got)
	}
	if strings.Contains(string(got), "token-value") || strings.Contains(string(got), "server detail") {
		t.Fatalf("secret-bearing command error persisted: %q", got)
	}
}

func TestLogsReportFromTailSuccess(t *testing.T) {
	rep := logsReportFromTail("client", 25, "line1\nline2\n", nil)
	if !rep.OK || rep.Kind != "logs" || rep.Service != "client" || rep.Lines != 25 || rep.Text != "line1\nline2\n" || rep.Error != "" {
		t.Fatalf("logs report = %+v, want successful report", rep)
	}
}

func TestLogsReportFromTailErrorKeepsTextAndHint(t *testing.T) {
	rep := logsReportFromTail("client", 0, "partial\n", os.ErrPermission)
	if rep.OK || rep.Lines != 100 || rep.Text != "partial\n" || rep.Error == "" || !strings.Contains(rep.Hint, "sudo bx logs") {
		t.Fatalf("logs report = %+v, want error report with partial text and hint", rep)
	}
}

func TestDefaultLogArchiveRootIsAbsolute(t *testing.T) {
	t.Setenv("BX_LOG_ARCHIVE_DIR", "")
	root := defaultLogArchiveRoot()
	if root == "" || !filepath.IsAbs(root) {
		t.Fatalf("default log archive root should be absolute, got %q", root)
	}
}

func TestDefaultLogArchiveRootHonorsEnv(t *testing.T) {
	want := filepath.Join(t.TempDir(), "diagnostics")
	t.Setenv("BX_LOG_ARCHIVE_DIR", want)
	if got := defaultLogArchiveRoot(); got != want {
		t.Fatalf("default log archive root = %q, want env %q", got, want)
	}
}

func TestApplyPlatformRiskRaisesRiskForTailscaleWarn(t *testing.T) {
	rep := leakCheckReport{
		Risk: "low",
		Checks: []checkReport{
			{Name: "tailscale", Status: "warn", Detail: "overlay route absent"},
		},
	}
	applyPlatformRisk(&rep)
	if rep.OK || rep.Risk != "medium" {
		t.Fatalf("tailscale warning should make leak report medium risk: %+v", rep)
	}
}

func TestPruneLogArchivesKeepsNewest(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 15; i++ {
		name := filepath.Join(root, "bx-logs-20260101-1200"+leftPadInt(i, 2))
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneLogArchives(root, 12); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		if entry.IsDir() {
			got = append(got, entry.Name())
		}
	}
	if len(got) != 12 {
		t.Fatalf("dirs after prune = %d, want 12: %v", len(got), got)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120000")); !os.IsNotExist(err) {
		t.Fatalf("oldest archive should be pruned, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120014")); err != nil {
		t.Fatalf("newest archive should be kept: %v", err)
	}
}

func TestPruneLogArchivesAllowsFewerThanKeep(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bx-logs-20260101-120000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pruneLogArchives(root, 12); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "bx-logs-20260101-120000")); err != nil {
		t.Fatalf("single archive should be kept: %v", err)
	}
}

func TestCapabilitiesReport(t *testing.T) {
	rep := capabilities()
	if rep.SchemaVersion != 1 || rep.Product != "bx" || !rep.SecretsRedacted {
		t.Fatalf("unexpected capabilities metadata: %+v", rep)
	}
	doctor := findCapability(rep.Commands, "bx doctor --json")
	if !doctor.Stable || doctor.RequiresRoot || doctor.ChangesSystem || doctor.ChangesNetwork || !doctor.ReadsSecrets {
		t.Fatalf("unexpected doctor capability: %+v", doctor)
	}
	if len(doctor.Arguments) == 0 || len(doctor.Examples) == 0 {
		t.Fatalf("doctor capability should include arguments/examples: %+v", doctor)
	}
	inspect := findCapability(rep.Commands, "bx inspect --json")
	if !inspect.Stable || inspect.RequiresRoot || inspect.ChangesSystem || inspect.ChangesNetwork || !inspect.ReadsSecrets {
		t.Fatalf("unexpected inspect capability: %+v", inspect)
	}
	webrtc := findCapability(rep.Commands, "bx webrtc-check --json")
	if !webrtc.Stable || webrtc.RequiresRoot || webrtc.ChangesSystem || webrtc.ChangesNetwork || !webrtc.ReadsSecrets {
		t.Fatalf("unexpected webrtc-check capability: %+v", webrtc)
	}
	notes := strings.Join(webrtc.SafeNotes, " ")
	if !strings.Contains(notes, "ICE candidates") || !strings.Contains(notes, "prove a WebRTC public-IP leak") {
		t.Fatalf("webrtc-check should describe browser ICE proof capability: %+v", webrtc)
	}
	leak := findCapability(rep.Commands, "bx leak-check --json")
	if !leak.Stable || leak.RequiresRoot || leak.ChangesSystem || leak.ChangesNetwork || !leak.ReadsSecrets {
		t.Fatalf("unexpected leak-check capability: %+v", leak)
	}
	if !strings.Contains(strings.Join(leak.SafeNotes, " "), "network-path") {
		t.Fatalf("leak-check should state network-path scope: %+v", leak)
	}
	observe := findCapability(rep.Commands, "bx observe --json")
	if !observe.Stable || observe.RequiresRoot || observe.ChangesSystem || observe.ChangesNetwork {
		t.Fatalf("unexpected observe capability: %+v", observe)
	}
	if !strings.Contains(strings.Join(observe.SafeNotes, " "), "status socket") {
		t.Fatalf("observe should document local-only status socket sampling: %+v", observe)
	}
	setup := findCapability(rep.Commands, "sudo bx setup <client-link>")
	if setup.Command == "" || !strings.Contains(strings.Join(setup.Arguments, " "), "<client-link>") {
		t.Fatalf("setup capability should use client-link wording: %+v", setup)
	}
	invite := findCapability(rep.Commands, "sudo bx invite [name]")
	if invite.Command == "" || !invite.RequiresRoot || !invite.ChangesSystem || invite.ChangesNetwork {
		t.Fatalf("unexpected invite capability: %+v", invite)
	}
	if !strings.Contains(strings.Join(invite.SafeNotes, " "), "preferred human-facing") {
		t.Fatalf("invite capability should guide agents toward human sharing: %+v", invite)
	}
	serverInstall := findCapability(rep.Commands, "sudo bx server install --host <host>")
	if serverInstall.Command == "" || !serverInstall.RequiresRoot || !serverInstall.ChangesSystem || serverInstall.ChangesNetwork {
		t.Fatalf("unexpected server install capability: %+v", serverInstall)
	}
	if !strings.Contains(strings.Join(serverInstall.Arguments, " "), "--open-ufw") {
		t.Fatalf("server install capability should advertise --open-ufw: %+v", serverInstall)
	}
	if !strings.Contains(strings.Join(serverInstall.SafeNotes, " "), "May change firewall only when --open-ufw is passed.") {
		t.Fatalf("server install capability should document --open-ufw firewall note: %+v", serverInstall)
	}
	userList := findCapability(rep.Commands, "sudo bx user list --json")
	if userList.Command == "" || !userList.RequiresRoot || userList.ChangesSystem || userList.ChangesNetwork {
		t.Fatalf("unexpected user list capability: %+v", userList)
	}
	userInvite := findCapability(rep.Commands, "sudo bx user invite <name>")
	if userInvite.Command == "" || !userInvite.RequiresRoot || !userInvite.ChangesSystem || userInvite.ChangesNetwork {
		t.Fatalf("unexpected user invite capability: %+v", userInvite)
	}
	probe := findCapability(rep.Commands, "bx probe <client-link>")
	if probe.Command == "" || !strings.Contains(strings.Join(probe.Examples, " "), "<client-link>") {
		t.Fatalf("probe capability should use client-link wording: %+v", probe)
	}
	up := findCapability(rep.Commands, "sudo bx up")
	if !up.RequiresRoot || !up.ChangesSystem || !up.ChangesNetwork {
		t.Fatalf("unexpected up capability: %+v", up)
	}
	status := findCapability(rep.Commands, "bx status --json")
	if !status.Stable || status.RequiresRoot || status.ChangesSystem || status.ChangesNetwork {
		t.Fatalf("unexpected status json capability: %+v", status)
	}
	if !strings.Contains(strings.Join(status.SafeNotes, " "), "menu bar") {
		t.Fatalf("status json should mention status surfaces: %+v", status)
	}
	reconnect := findCapability(rep.Commands, "sudo bx reconnect")
	if !reconnect.Stable || !reconnect.RequiresRoot || reconnect.ChangesSystem || reconnect.ChangesNetwork {
		t.Fatalf("unexpected reconnect capability: %+v", reconnect)
	}
	if !strings.Contains(strings.Join(reconnect.SafeNotes, " "), "Does not release") {
		t.Fatalf("reconnect should document its fail-closed scope: %+v", reconnect)
	}
	if restart := findCapability(rep.Commands, "sudo bx restart"); restart.Command != "" {
		t.Fatalf("legacy restart must not be advertised to agents: %+v", restart)
	}
	update := findCapability(rep.Commands, "sudo bx update")
	if !update.Stable || !update.RequiresRoot || !update.ChangesSystem || update.ChangesNetwork {
		t.Fatalf("unexpected update capability: %+v", update)
	}
	if !strings.Contains(strings.Join(update.SafeNotes, " "), "Does not restart") {
		t.Fatalf("update should document preserved protection: %+v", update)
	}
	updateNotes := strings.Join(update.SafeNotes, " ")
	if !strings.Contains(updateNotes, "fail-closed") || !strings.Contains(updateNotes, "rolls back") {
		t.Fatalf("update should document fail-closed Guardian transaction semantics: %+v", update)
	}
	if !strings.Contains(updateNotes, "do not simulate an update by combining down and up") {
		t.Fatalf("update should tell agents not to simulate updates with down/up: %+v", update)
	}
	direct := findCapability(rep.Commands, "sudo bx direct add <domain>")
	if !direct.Stable || !direct.RequiresRoot || !direct.ChangesSystem || direct.ChangesNetwork || !direct.ReadsSecrets {
		t.Fatalf("unexpected direct add capability: %+v", direct)
	}
	if !strings.Contains(strings.Join(direct.SafeNotes, " "), "mutually exclusive") {
		t.Fatalf("direct add should document direct/proxy mutual exclusion: %+v", direct)
	}
	proxy := findCapability(rep.Commands, "sudo bx proxy add <domain>")
	if !proxy.Stable || !proxy.RequiresRoot || !proxy.ChangesSystem || proxy.ChangesNetwork || !proxy.ReadsSecrets {
		t.Fatalf("unexpected proxy add capability: %+v", proxy)
	}
	if !strings.Contains(strings.Join(proxy.SafeNotes, " "), "force tunnel") {
		t.Fatalf("proxy add should document force tunnel behavior: %+v", proxy)
	}
	presetApply := findCapability(rep.Commands, "sudo bx preset apply <name>")
	if !presetApply.Stable || !presetApply.RequiresRoot || !presetApply.ChangesSystem || presetApply.ChangesNetwork || !presetApply.ReadsSecrets {
		t.Fatalf("unexpected preset apply capability: %+v", presetApply)
	}
	presetNotes := strings.ToLower(strings.Join(presetApply.SafeNotes, " "))
	if !strings.Contains(presetNotes, "explicit opt-in") || !strings.Contains(presetNotes, "direct") || !strings.Contains(presetNotes, "dns") {
		t.Fatalf("preset apply should document opt-in config-only behavior: %+v", presetApply)
	}
	logs := findCapability(rep.Commands, "bx logs")
	if !logs.Stable || logs.ChangesSystem || logs.ChangesNetwork {
		t.Fatalf("unexpected logs capability: %+v", logs)
	}
	if !strings.Contains(strings.ToLower(strings.Join(logs.SafeNotes, " ")), "automatic diagnostics") {
		t.Fatalf("logs capability should mention automatic diagnostics archive: %+v", logs)
	}
	udpStatus := findCapability(rep.Commands, "bx realtime status")
	if !udpStatus.Stable || udpStatus.RequiresRoot || udpStatus.ChangesSystem || udpStatus.ChangesNetwork {
		t.Fatalf("unexpected realtime status capability: %+v", udpStatus)
	}
	if !strings.Contains(strings.Join(udpStatus.SafeNotes, " "), "UDP") {
		t.Fatalf("realtime status should mention UDP: %+v", udpStatus)
	}
	realtimeOn := findCapability(rep.Commands, "sudo bx realtime on")
	if !realtimeOn.Stable || !realtimeOn.RequiresRoot || !realtimeOn.ChangesSystem || realtimeOn.ChangesNetwork || !realtimeOn.ReadsSecrets {
		t.Fatalf("unexpected realtime on capability: %+v", realtimeOn)
	}
	if !strings.Contains(strings.Join(realtimeOn.SafeNotes, " "), "Relays non-DNS UDP") {
		t.Fatalf("realtime on should document UDP relay behavior: %+v", realtimeOn)
	}
	if !strings.Contains(strings.Join(realtimeOn.SafeNotes, " "), "without restarting") {
		t.Fatalf("realtime on should document preserved protection: %+v", realtimeOn)
	}
	realtimeOff := findCapability(rep.Commands, "sudo bx realtime off")
	if !realtimeOff.Stable || !realtimeOff.RequiresRoot || !realtimeOff.ChangesSystem || realtimeOff.ChangesNetwork || !realtimeOff.ReadsSecrets {
		t.Fatalf("unexpected realtime off capability: %+v", realtimeOff)
	}
	dnsOn := findCapability(rep.Commands, "sudo bx dns on")
	if !dnsOn.RequiresRoot || !dnsOn.ChangesSystem || !dnsOn.ChangesNetwork {
		t.Fatalf("unexpected dns on capability: %+v", dnsOn)
	}
	menuInstall := findCapability(rep.Commands, "scripts/install-macos-menu.sh install")
	if menuInstall.Command == "" || menuInstall.RequiresRoot || menuInstall.ChangesNetwork || menuInstall.ChangesSystem {
		t.Fatalf("unexpected macOS menu install capability: %+v", menuInstall)
	}
	if strings.Contains(strings.ToLower(menuInstall.Summary), "companion") {
		t.Fatalf("macOS menu install should describe the app as the default menu bar app, not a companion: %+v", menuInstall)
	}
	if !strings.Contains(strings.Join(menuInstall.SafeNotes, " "), "Does not start protection") {
		t.Fatalf("macOS menu install should clarify it does not start protection: %+v", menuInstall)
	}
	menuStatus := findCapability(rep.Commands, "scripts/install-macos-menu.sh status")
	if menuStatus.Command == "" || !menuStatus.Stable || menuStatus.ChangesSystem || menuStatus.ChangesNetwork {
		t.Fatalf("unexpected macOS menu status capability: %+v", menuStatus)
	}
	menuRestart := findCapability(rep.Commands, "scripts/install-macos-menu.sh restart")
	if menuRestart.Command == "" || menuRestart.RequiresRoot || menuRestart.ChangesSystem || menuRestart.ChangesNetwork {
		t.Fatalf("unexpected macOS menu restart capability: %+v", menuRestart)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{menuRestart.Summary, strings.Join(menuRestart.SafeNotes, " ")}, " ")), "companion") {
		t.Fatalf("macOS menu restart should describe the menu bar app, not a companion: %+v", menuRestart)
	}
	if !strings.Contains(strings.Join(menuRestart.SafeNotes, " "), "not protection") {
		t.Fatalf("macOS menu restart should clarify it does not restart protection: %+v", menuRestart)
	}
	menuUninstall := findCapability(rep.Commands, "scripts/install-macos-menu.sh uninstall")
	if menuUninstall.Command == "" || menuUninstall.RequiresRoot || menuUninstall.ChangesSystem || menuUninstall.ChangesNetwork {
		t.Fatalf("unexpected macOS menu uninstall capability: %+v", menuUninstall)
	}
	if !strings.Contains(strings.Join(menuUninstall.SafeNotes, " "), "Does not turn off protection") {
		t.Fatalf("macOS menu uninstall should clarify it does not turn off protection: %+v", menuUninstall)
	}
	macRelease := findCapability(rep.Commands, "scripts/package-macos-release.sh")
	if macRelease.Command == "" || !macRelease.Stable || macRelease.RequiresRoot || macRelease.ChangesSystem || macRelease.ChangesNetwork {
		t.Fatalf("unexpected macOS release capability: %+v", macRelease)
	}
	macReleaseVerify := findCapability(rep.Commands, "scripts/verify-macos-release.sh")
	if macReleaseVerify.Command == "" || !macReleaseVerify.Stable || macReleaseVerify.RequiresRoot || macReleaseVerify.ChangesSystem || macReleaseVerify.ChangesNetwork {
		t.Fatalf("unexpected macOS release verify capability: %+v", macReleaseVerify)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	var parsed capabilitiesReport
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("capabilities json should be parseable: %v\n%s", err, buf.String())
	}
}

func leftPadInt(v, width int) string {
	s := strconv.Itoa(v)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func TestRenderRealtimeStatusFallback(t *testing.T) {
	out := renderRealtimeStatus(nil)
	for _, want := range []string{
		"realtime supported: true",
		"udp mode: proxy",
		"udp blocked: unknown",
		"relayed through bx tunnel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime fallback missing %q:\n%s", want, out)
		}
	}
}

func TestRenderRealtimeStatusFromReport(t *testing.T) {
	out := renderRealtimeStatus(&stats.Report{
		Snapshot: stats.Snapshot{UDPBlocked: 42},
		UDPMode:  "block",
		UDPNote:  "custom udp note",
	})
	for _, want := range []string{
		"udp mode: block",
		"udp blocked: 42",
		"custom udp note",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("realtime report missing %q:\n%s", want, out)
		}
	}
}

func TestSetRealtimeModeUpdatesClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRealtimeMode(path, "proxy"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UDP.Mode != "proxy" {
		t.Fatalf("udp mode after on = %q, want proxy", cfg.UDP.Mode)
	}
	if err := setRealtimeMode(path, "block"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UDP.Mode != "block" {
		t.Fatalf("udp mode after off = %q, want block", cfg.UDP.Mode)
	}
}

func TestPlanRealtimePostChange(t *testing.T) {
	tests := []struct {
		name          string
		unitInstalled bool
		activeState   string
		wantContains  string
	}{
		{"active stays protected", true, "active", "保持运行"},
		{"not installed", false, "inactive", "sudo bx up"},
		{"inactive installed", true, "inactive", "下次 sudo bx up"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planRealtimePostChange(tt.unitInstalled, tt.activeState)
			if !strings.Contains(got.Message, tt.wantContains) {
				t.Fatalf("plan = %+v, want message containing %q", got, tt.wantContains)
			}
		})
	}
}

func TestSetRealtimeModePreservesBXLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	link := blink.Encode("brook://server?server=example.com%3A443&password=pw")
	if err := os.WriteFile(path, []byte("server: \""+link+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRealtimeMode(path, "direct-realtime"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, link) {
		t.Fatalf("setRealtimeMode should preserve bx link, got:\n%s", text)
	}
	if strings.Contains(text, "brook://server?") {
		t.Fatalf("setRealtimeMode should not rewrite bx link to internal link:\n%s", text)
	}
}

func TestRealtimeReportFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: \"brook://x\"\nudp:\n  mode: proxy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := realtimeReportFromConfig(path)
	if rep == nil {
		t.Fatal("expected realtime report from config")
	}
	if rep.UDPMode != "proxy" || !strings.Contains(rep.UDPNote, "relayed through bx tunnel") {
		t.Fatalf("report = %+v, want proxy relay note", rep)
	}
}

func TestServerDoctorJSONReport(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yaml")
	sharesDir := filepath.Join(dir, "shares")
	if err := writeServerConfig(cfgPath, serverConfig{Listen: ":10998", Password: "secret"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sharesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rep := collectServerDoctor(cfgPath, sharesDir)
	if rep.Kind != "server" || !rep.SecretsRedacted || !rep.RequiresRoot {
		t.Fatalf("unexpected server report metadata: %+v", rep)
	}
	if got := findCheck(rep.Checks, "config_parse"); got.Status != "ok" {
		t.Fatalf("config_parse = %+v, want ok", got)
	}
	if got := findCheck(rep.Checks, "shares"); got.Status != "info" || got.Detail != "none" {
		t.Fatalf("shares = %+v, want none info", got)
	}
}

func TestShareJSONViewsExposeOnlyOperationalFields(t *testing.T) {
	shares := []shareInfo{{
		Name:   "alice",
		Config: serverConfig{Listen: ":10001", Password: "pw"},
	}}
	views := shareViews(shares)
	if len(views) != 1 || views[0].Name != "alice" || views[0].Listen != ":10001" {
		t.Fatalf("share views = %+v", views)
	}
	var buf bytes.Buffer
	if err := writeJSON(&buf, sharesReport{OK: true, SecretsRedacted: true, Shares: views}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "pw") {
		t.Fatalf("shares json should not expose password: %s", buf.String())
	}
}

func findCheck(checks []checkReport, name string) checkReport {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return checkReport{}
}

func containsText(values []string, pattern string) bool {
	for _, value := range values {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}

func findCapability(commands []commandCapability, command string) commandCapability {
	for _, item := range commands {
		if item.Command == command {
			return item
		}
	}
	return commandCapability{}
}

func appHasCommand(app *cli.App, name string) bool {
	return findAppCommand(app, name) != nil
}

func findAppCommand(app *cli.App, name string) *cli.Command {
	for _, command := range app.Commands {
		if command.Name == name {
			return command
		}
	}
	return nil
}

func commandHasSubcommand(command *cli.Command, name string) bool {
	for _, sub := range command.Subcommands {
		if sub.Name == name {
			return true
		}
	}
	return false
}

func subcommandHidden(command *cli.Command, name string) bool {
	for _, sub := range command.Subcommands {
		if sub.Name == name {
			return sub.Hidden
		}
	}
	return false
}

func commandHasFlag(command *cli.Command, name string) bool {
	for _, flag := range command.Flags {
		for _, flagName := range flag.Names() {
			if flagName == name {
				return true
			}
		}
	}
	return false
}

func TestIsListening(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !isListening(port) {
		t.Fatalf("port %s should be detected as listening", port)
	}
}

func TestCopyIfExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.log")
	dst := filepath.Join(dir, "copy.log")
	if err := os.WriteFile(src, []byte("raw log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyIfExists(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "raw log\n" {
		t.Fatalf("copy = %q", b)
	}
	if err := copyIfExists(filepath.Join(dir, "missing.log"), filepath.Join(dir, "missing-copy.log")); err != nil {
		t.Fatalf("missing source should be ignored: %v", err)
	}
}

func TestWriteReadServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	cfg := serverConfig{Listen: ":9999", Password: "pw"}
	if err := writeServerConfig(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
	got, err := readServerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Fatalf("config = %+v, want %+v", got, cfg)
	}
}

func TestWriteServerConfigForceResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "pw"}, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestRotateServerConfigPreservesListenAndResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "old"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := rotateServerConfig(path, "new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":9999" || got.Password != "new" {
		t.Fatalf("rotated config = %+v", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestShareHelpers(t *testing.T) {
	if got, err := cleanShareName("alice-1"); err != nil || got != "alice-1" {
		t.Fatalf("cleanShareName = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a b", "x/y"} {
		if _, err := cleanShareName(bad); err == nil {
			t.Fatalf("bad share name %q should fail", bad)
		}
	}
	dir := t.TempDir()
	if got := shareConfigPath(dir, "alice"); got != filepath.Join(dir, "alice.yaml") {
		t.Fatalf("shareConfigPath = %q", got)
	}
}

func TestStringFlagReadsPostArgFlags(t *testing.T) {
	args := []string{"alice", "--host", "example.com", "--listen=:10077"}
	if got := stringFlagFromArgs(args, "host"); got != "example.com" {
		t.Fatalf("host = %q", got)
	}
	if got := stringFlagFromArgs(args, "listen"); got != ":10077" {
		t.Fatalf("listen = %q", got)
	}
}

func TestReadSharesSorted(t *testing.T) {
	dir := t.TempDir()
	for _, item := range []struct {
		name   string
		listen string
	}{
		{"bob", ":10002"},
		{"alice", ":10001"},
	} {
		if err := writeServerConfig(shareConfigPath(dir, item.name), serverConfig{Listen: item.listen, Password: "pw"}, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readShares(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Fatalf("shares = %+v", got)
	}
}

func TestUserViewsFromShares(t *testing.T) {
	users := userViews([]shareInfo{
		{Name: "alice", Config: serverConfig{Type: "reality", Link: "vless://id@example.com:443"}},
		{Name: "bob", Config: serverConfig{Listen: ":10000", Password: "pw"}},
	})
	if len(users) != 2 {
		t.Fatalf("users = %+v", users)
	}
	if users[0].Name != "alice" || users[0].Type != "reality" || users[0].Status != "active" || users[0].Plan != "default" {
		t.Fatalf("reality user view = %+v", users[0])
	}
	if users[1].Name != "bob" || users[1].Type != "brook" || users[1].Listen != ":10000" || users[1].Plan != "default" {
		t.Fatalf("brook user view = %+v", users[1])
	}
}

func TestReadShare(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Type: "reality", Link: "vless://id@example.com:443"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := readShare(dir, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "alice" || got.Config.Type != "reality" {
		t.Fatalf("readShare = %+v", got)
	}
}

func TestNextShareListenSkipsExistingShares(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10000", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := nextShareListen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != ":10001" {
		t.Fatalf("nextShareListen = %q, want :10001", got)
	}
}

func TestSetupCommandQuotesLinks(t *testing.T) {
	main := "bx://main"
	udp := "bx://udp"
	if got := setupCommand(main, ""); got != "sudo bx setup 'bx://main'" {
		t.Fatalf("setupCommand main = %q", got)
	}
	if got := setupCommand(main, udp); got != "sudo bx setup 'bx://main' --udp 'bx://udp'" {
		t.Fatalf("setupCommand udp = %q", got)
	}
}

func TestInviteTextIsUserFriendly(t *testing.T) {
	out := inviteText("alice", "bx://main", "bx://udp")
	for _, want := range []string{"bx invite: alice", "给用户", "菜单栏 App", "bx://main", "UDP: bx://udp", "sudo bx setup 'bx://main' --udp 'bx://udp'", "sudo bx up"} {
		if !strings.Contains(out, want) {
			t.Fatalf("invite text missing %q:\n%s", want, out)
		}
	}
}

func TestInviteShareConfigShowsExistingBrookShare(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10000", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	cfg, err := inviteShareConfig("alice", filepath.Join(t.TempDir(), "server.yaml"), dir, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Link == "" || !strings.Contains(cfg.Link, "brook://") || !strings.Contains(cfg.Link, "example.com") {
		t.Fatalf("invite share cfg link = %q", cfg.Link)
	}
}

func TestResolveConfigPathKeepsExplicitMissingPath(t *testing.T) {
	// 用户显式传入的不存在路径应原样返回(不偷偷回退),便于错误信息指向用户路径
	p := "/nonexistent/explicit/whoami-bx-test.yaml"
	if got := resolveConfigPath(p); got != p {
		t.Fatalf("显式缺失路径应原样返回, got %q", got)
	}
}

func TestMCPInstallText(t *testing.T) {
	out := mcpInstallText("/usr/local/bin/bx")
	for _, want := range []string{
		"claude mcp add --scope user bx -- /usr/local/bin/bx mcp",
		`"command": "/usr/local/bin/bx"`,
		`"args": ["mcp"]`,
		"AI agent",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mcpInstallText 缺 %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestRawLinkRisk(t *testing.T) {
	// 裸凭据链接 → 提示
	for _, raw := range []string{
		"vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
		"  vless://x@h:1 ", // 带空白也认
	} {
		if rawLinkRisk(raw) == "" {
			t.Errorf("裸链接应提示风险: %q", raw)
		}
	}
	// 已换壳 / 非链接 → 不提示
	for _, wrapped := range []string{
		"bx://eyJ2IjoxfQ",
		"blink://abc",
		"",
		"garbage",
	} {
		if rawLinkRisk(wrapped) != "" {
			t.Errorf("已换壳/非裸链接不该提示: %q", wrapped)
		}
	}
}

func TestProtocolAdvisory(t *testing.T) {
	// 弱协议(对当今强 DPI/探测易识别)→ 建议换 REALITY
	for _, weak := range []string{
		"trojan://pw@1.2.3.4:443?sni=x.com",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388",
		"vmess://eyJhZGQiOiIxLjIuMy40IiwicG9ydCI6IjQ0MyIsImlkIjoieCIsIm5ldCI6InRjcCJ9",
	} {
		a := protocolAdvisory(weak)
		if a == "" || !strings.Contains(a, "REALITY") {
			t.Errorf("弱协议应提示换 REALITY: %q → %q", weak, a)
		}
	}
	// hysteria2 缺 obfs → 提示加 salamander
	if a := protocolAdvisory("hysteria2://pw@1.2.3.4:8443?sni=x.com"); !strings.Contains(a, "obfs") {
		t.Errorf("裸 hysteria2 应提示加 obfs: %q", a)
	}
	// hysteria2 已带 obfs → 不提示
	if a := protocolAdvisory("hysteria2://pw@1.2.3.4:8443?sni=x.com&obfs=salamander&obfs-password=p"); a != "" {
		t.Errorf("带 obfs 的 hysteria2 不该提示: %q", a)
	}
	// reality / brook(一等公民/默认)→ 不提示
	for _, ok := range []string{
		"vless://uuid@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com",
		"brook://server?server=1.2.3.4%3A9999&password=pw",
	} {
		if a := protocolAdvisory(ok); a != "" {
			t.Errorf("reality/brook 不该提示: %q → %q", ok, a)
		}
	}
}

func TestResolveConfigLinksBundle(t *testing.T) {
	l1 := "vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com"
	l2 := "brook://server?server=1.2.3.4%3A9999&password=pw"
	bundle := blink.EncodeMulti([]string{l1, l2})
	probe, configLinks, err := resolveConfigLinks(bundle)
	if err != nil {
		t.Fatalf("resolve bundle: %v", err)
	}
	if probe != l1 {
		t.Fatalf("probe 应=主传输 %q, got %q", l1, probe)
	}
	if len(configLinks) != 2 {
		t.Fatalf("应 2 个 configLink, got %d", len(configLinks))
	}
	// 各自换壳,解回应等于原 link
	for i, want := range []string{l1, l2} {
		got, err := blink.Decode(configLinks[i])
		if err != nil || got != want {
			t.Fatalf("configLink[%d] 解回=%q want=%q err=%v", i, got, want, err)
		}
	}
}

func TestResolveConfigLinksRawSingle(t *testing.T) {
	raw := "vless://u@h:1?security=reality&pbk=K&sid=a&sni=s"
	probe, configLinks, err := resolveConfigLinks(raw)
	if err != nil || probe != raw || len(configLinks) != 1 {
		t.Fatalf("裸单链接: probe=%q n=%d err=%v", probe, len(configLinks), err)
	}
}

func TestClientDoctorVlessServerLinkOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("server: \"vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com\"\n"), 0o600)
	rep := collectClientDoctor(path, "x:443", 0, true)
	got := findCheck(rep.Checks, "server_link")
	if got.Status != "ok" {
		t.Fatalf("vless server_link 应 ok,实得 %+v", got)
	}
}

func TestVlessUUIDHelpers(t *testing.T) {
	link := "vless://old-uuid-1234@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com"
	if got := uuidFromVlessLink(link); got != "old-uuid-1234" {
		t.Errorf("extract uuid: got %q", got)
	}
	swapped := swapVlessUUID(link, "new-uuid-5678")
	if uuidFromVlessLink(swapped) != "new-uuid-5678" {
		t.Errorf("swap uuid 失败: %q", swapped)
	}
	// 其余部分(host/port/query)不变
	if !strings.Contains(swapped, "@1.2.3.4:443?security=reality&pbk=P&sid=ab&sni=www.cloudflare.com") {
		t.Errorf("swap 不该动其余部分: %q", swapped)
	}
	// 非 vless 链接
	if uuidFromVlessLink("brook://x") != "" {
		t.Error("非 vless 应返回空")
	}
}

func TestStatusReportIncludesUpdateObservabilityFields(t *testing.T) {
	rep := assembleClientStatusReport(
		stats.Report{TunnelHealthy: true},
		guardian.Status{
			Protection:      guardian.ProtectionProtected,
			Phase:           guardian.PhaseActivating,
			CoreVersion:     "1.2.3",
			GuardianVersion: "1.2.2",
			RuntimeVersion:  "1.2.3",
		},
	)

	var encoded map[string]any
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}

	// Check all four fields are present and correct
	if encoded["phase"] != "activating" {
		t.Fatalf("phase = %v, want activating", encoded["phase"])
	}
	if encoded["core_version"] != "1.2.3" {
		t.Fatalf("core_version = %v, want 1.2.3", encoded["core_version"])
	}
	if encoded["guardian_version"] != "1.2.2" {
		t.Fatalf("guardian_version = %v, want 1.2.2", encoded["guardian_version"])
	}
	if encoded["runtime_version"] != "1.2.3" {
		t.Fatalf("runtime_version = %v, want 1.2.3", encoded["runtime_version"])
	}
}

func TestStatusReportOmitsEmptyPhase(t *testing.T) {
	rep := assembleClientStatusReport(
		stats.Report{TunnelHealthy: true},
		guardian.Status{
			Protection: guardian.ProtectionProtected,
			// Phase is empty (default)
		},
	)

	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}

	// Check that phase is not in the JSON (omitempty)
	if strings.Contains(string(data), "\"phase\"") {
		t.Fatalf("phase should be omitted when empty, but found in: %s", string(data))
	}
}

// 事故里「翻诊断包只拿到陈旧 Core 日志」正是因为归档只收 ClientLogPaths()。
// Guardian 失败的完整原因写在 Guardian 日志里,必须一并收进诊断包。
func TestArchiveClientLogsCollectsGuardianLogs(t *testing.T) {
	source := t.TempDir()
	guardLog := filepath.Join(source, "bx-guard.err.log")
	if err := os.WriteFile(guardLog, []byte("guardian_needs_attention code=core_ownership_uncertain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore := swapGuardianArchiveLogPaths([]string{guardLog, filepath.Join(source, "absent.log")})
	defer restore()

	dir, err := archiveClientLogsWithReason(t.TempDir(), "up")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "bx-guard.err.log"))
	if err != nil {
		t.Fatalf("诊断包必须包含 Guardian 日志:%v", err)
	}
	if !strings.Contains(string(got), "core_ownership_uncertain") {
		t.Errorf("Guardian 日志内容未收全,实际 = %q", got)
	}
}

// Guardian 日志是 0600 root-only:非 root 跑 bx logs --archive(以及 up/down
// 失败后的自动归档)读不到很正常,不能因此让整个诊断包失败。
func TestArchiveClientLogsSurvivesUnreadableGuardianLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 能读任何文件,无法构造不可读场景")
	}
	source := t.TempDir()
	guardLog := filepath.Join(source, "bx-guard.err.log")
	if err := os.WriteFile(guardLog, []byte("secret\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	restore := swapGuardianArchiveLogPaths([]string{guardLog})
	defer restore()

	dir, err := archiveClientLogsWithReason(t.TempDir(), "up")
	if err != nil {
		t.Fatalf("Guardian 日志读不到不应让归档失败:%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "meta.txt")); err != nil {
		t.Fatalf("归档仍应产出其余材料:%v", err)
	}
}

func swapGuardianArchiveLogPaths(paths []string) func() {
	previous := guardianArchiveLogPaths
	guardianArchiveLogPaths = func() []string { return paths }
	return func() { guardianArchiveLogPaths = previous }
}

// bx status --json 必须把观测与信念并列发布,并在二者不一致时明写 divergence。
// 真实事故形态:Guardian 报 protected,而流量其实走 en0 明文直连——旧的平坦
// 结构里这件事根本表达不出来。
func TestClientStatusPublishesObservationAndDivergence(t *testing.T) {
	protectedStatus := func() (guardian.Status, error) {
		return guardian.Status{
			Desired:    guardian.DesiredOn,
			Protection: guardian.ProtectionProtected,
			DNSState:   guardian.DNSManaged,
			DNSManaged: true,
		}, nil
	}
	healthyCore := func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil }

	t.Run("信念与事实不符时必须产出 divergence", func(t *testing.T) {
		rep, err := readClientStatusReportWithObserver(
			healthyCore, protectedStatus, "darwin",
			func(context.Context) observe.ObservedState {
				return observe.ObservedState{
					CaptureOK: observe.False, CaptureInterface: "en0",
					DNSManaged: observe.True, TunnelHealthy: observe.True,
				}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Observed == nil {
			t.Fatal("必须发布观测,否则消费者无从分辨哪些字段是记忆")
		}
		if rep.ProtectionState != guardian.ProtectionProtected {
			t.Errorf("观测不得覆盖信念,protection_state = %q, want protected", rep.ProtectionState)
		}
		var found bool
		for _, d := range rep.Divergence {
			if d.Field == "capture_ok" {
				found = true
			}
		}
		if !found {
			t.Errorf("劫持未生效却声称 protected 必须产出 capture_ok divergence,实际 = %+v", rep.Divergence)
		}

		encoded := map[string]any{}
		payload, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &encoded); err != nil {
			t.Fatal(err)
		}
		if encoded["observed"] == nil {
			t.Errorf("JSON 必须含 observed,实际 = %s", payload)
		}
		if encoded["divergence"] == nil {
			t.Errorf("JSON 必须含 divergence,实际 = %s", payload)
		}
		if encoded["desired"] != "on" {
			t.Errorf("JSON 必须发布意图 desired,实际 = %s", payload)
		}
	})

	t.Run("一致时必须安静", func(t *testing.T) {
		rep, err := readClientStatusReportWithObserver(
			healthyCore, protectedStatus, "darwin",
			func(context.Context) observe.ObservedState {
				return observe.ObservedState{
					CaptureOK: observe.True, CaptureInterface: "utun4",
					DNSManaged: observe.True, TunnelHealthy: observe.True,
					BarrierPresent: observe.False,
				}
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Divergence) != 0 {
			t.Errorf("观测与信念一致时不应有 divergence,实际 = %+v", rep.Divergence)
		}
	})
}

// 观测不得改变 bx status 的成败:Core socket 不可达时该报错的仍报错,
// 能出报告的仍出报告。
func TestClientStatusObservationDoesNotChangeOutcome(t *testing.T) {
	observer := func(context.Context) observe.ObservedState {
		return observe.ObservedState{Errors: []observe.ObserveError{{Item: "capture_ok", Err: "boom"}}}
	}
	if _, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{}, errors.New("missing Core socket") },
		func() (guardian.Status, error) { return guardian.Status{}, errors.New("guardian down") },
		"darwin", observer,
	); err == nil {
		t.Error("Core 与 Guardian 双双不可达时仍须报错,观测不得掩盖")
	}

	rep, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Desired: guardian.DesiredOn, Protection: guardian.ProtectionProtected}, nil
		},
		"linux", observer,
	)
	if err != nil {
		t.Fatalf("观测项失败不得让 bx status 失败:%v", err)
	}
	var explained bool
	for _, d := range rep.Divergence {
		if d.Field == "capture_ok" && d.Observed == "unknown" {
			explained = true
		}
	}
	if !explained {
		t.Errorf("观测失败必须以 unknown 的 divergence 现身,实际 = %+v", rep.Divergence)
	}
}

// desired=off 却观测到屏障/DNS 残留,是"用户已关闭保护但整机仍断网"这类事故的
// 唯一可见信号。
func TestClientStatusFlagsResidueWhenDesiredOff(t *testing.T) {
	rep, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Desired: guardian.DesiredOff, Protection: guardian.ProtectionOff}, nil
		},
		"darwin",
		func(context.Context) observe.ObservedState {
			return observe.ObservedState{BarrierPresent: observe.True, DNSManaged: observe.True}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, d := range rep.Divergence {
		fields[d.Field] = true
	}
	if !fields["barrier_present"] || !fields["dns_managed"] {
		t.Errorf("关闭意图下的屏障/DNS 残留必须各产出一条 divergence,实际 = %+v", rep.Divergence)
	}
}

// 观测的路由/DNS 原语目前只有 macOS 实现。在其余平台附一份全 Unknown 的观测,
// 不带来任何新事实(tunnel_healthy 本就取自同一个控制 socket,已在扁平字段里),
// 却让 divergence 恒非空——每次 bx status 都吐 5 条「该项无法观测」。那会把
// divergence 训练成用户和 agent 学会忽略的噪声,正好毁掉它唯一的价值。
func TestObservationOnlyAttachedWherePrimitivesExist(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			if observerForPlatform(platform) != nil {
				t.Errorf("%s 上不得附观测:原语不存在,只会产出恒定噪声", platform)
			}
		})
	}
	if observerForPlatform("darwin") == nil {
		t.Error("darwin 上必须附观测——本期全部价值都在这里")
	}
}

// 平台不支持时,报告里宁可没有 observed,也不能有一份「全 Unknown + 满屏
// 无法观测」的观测:字段缺席是诚实的「没问」,后者是把静态平台限制伪装成
// 每次调用都新发生的差异。
func TestClientStatusOmitsObservationOnUnsupportedPlatform(t *testing.T) {
	rep, err := readClientStatusReportWithObserver(
		func() (stats.Report, error) { return stats.Report{TunnelHealthy: true}, nil },
		func() (guardian.Status, error) {
			return guardian.Status{Desired: guardian.DesiredOn, Protection: guardian.ProtectionProtected}, nil
		},
		"linux",
		observerForPlatform("linux"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Observed != nil {
		t.Errorf("不支持观测的平台不得发布 observed,实际 = %+v", rep.Observed)
	}
	if len(rep.Divergence) != 0 {
		t.Errorf("不支持观测的平台不得发布 divergence,实际 = %+v", rep.Divergence)
	}
}

// 处在 off / 未配置 / 未安装 时,用户唯一想点的就是那个建设性主动作
// (Start Protection / Set Up bx / Install bx)。把它排在 View Logs 与
// Run Doctor 之下是本末倒置——真机反馈 2026-08-06:「start protection
// 是不是应该在最上面,最显眼的地方」。
//
// 破坏性动作(Turn Off / Quit)不在此列:它们留在菜单底部是 macOS 惯例,
// 也避免误点。所以这条只钉建设性主动作与诊断入口的相对次序。
func TestMacMenuPutsConstructiveActionBeforeDiagnostics(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func rebuildMenu()")
	if !ok {
		t.Fatal("could not locate rebuildMenu in main.swift")
	}
	firstDiagnostic := strings.Index(body, `"View Logs"`)
	if firstDiagnostic < 0 {
		t.Fatal("rebuildMenu should still offer View Logs")
	}
	for _, action := range []string{`"Start Protection"`, `"Set Up bx..."`, `"Install bx…"`} {
		at := strings.Index(body, action)
		if at < 0 {
			t.Errorf("rebuildMenu should still offer %s", action)
			continue
		}
		if at > firstDiagnostic {
			t.Errorf("%s must come before the first View Logs entry — it is the one action a user in that state wants, and burying it under diagnostics is why they could not start bx from the menu", action)
		}
	}
}

// bx setup 在已有配置上必须**只换传输**,不得整份重写。
//
// 真机事故 2026-08-06:用户 sudo bx setup <新服务器> 想换机器,看到
// 「✅ 服务器连通 366ms」以为成功,实际旧逻辑是「配置已存在就拒绝」——配置没写,
// 随后 bx up 用旧配置起来显示 Protected,用户完全有理由相信自己换过去了。
// 而唯一的出路 --force 会整份重写,把手写的 apple/steam 直连策略一起冲掉。
func TestSetupDispositionNeverRewritesExistingConfigWithoutForce(t *testing.T) {
	for _, tt := range []struct {
		name   string
		exists bool
		force  bool
		want   setupDisposition
	}{
		{"无配置:全新写入", false, false, setupWriteFresh},
		{"有配置:只换传输,保留其余", true, false, setupUpdateTransports},
		{"有配置 + --force:整份重写", true, true, setupOverwrite},
		{"无配置 + --force:仍是全新写入", false, true, setupWriteFresh},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideSetupDisposition(tt.exists, tt.force); got != tt.want {
				t.Errorf("decideSetupDisposition(exists=%v, force=%v) = %v, want %v",
					tt.exists, tt.force, got, tt.want)
			}
		})
	}
}

// bx setup 只接受一个链接。多出来的位置参数几乎必然是「flag 写在了位置参数后面」
// ——urfave/cli 沿用 Go flag 包语义,遇到第一个位置参数就停止解析 flag,于是
//
//	sudo bx setup '<链接>' --udp '<链接>'
//
// 里的 --udp 会被当成两个普通参数**静默忽略**。真机 2026-08-06/07:Mac 与
// ws-via-vps 上都是这么敲的,结果 udp.transport 一直停在旧服务器,而命令看起来
// 完全成功。静默吞掉用户明确写出来的意图,是最坏的一类失败。
func TestSetupExtraArgsAreRejectedNotIgnored(t *testing.T) {
	err := checkSetupArgs([]string{"bx://main", "--udp", "bx://udp"})
	if err == nil {
		t.Fatal("多余的位置参数必须报错——它几乎总是被吞掉的 flag,静默忽略会让用户以为配置成功了")
	}
	if !strings.Contains(err.Error(), "--udp") {
		t.Errorf("报错必须点名那个被吞掉的 flag,实际 = %v", err)
	}
	if !strings.Contains(err.Error(), "bx setup --udp") {
		t.Errorf("报错必须给出正确写法(flag 放前面),实际 = %v", err)
	}

	if err := checkSetupArgs([]string{"bx://main"}); err != nil {
		t.Errorf("单个链接是正常用法,不该报错:%v", err)
	}
	if err := checkSetupArgs(nil); err != nil {
		t.Errorf("无参数由既有的用法提示处理,这里不该报错:%v", err)
	}
}

// 退出入口必须在每个 BxState 分支都能找到。此前 quitBxActionTitle 只出现在
// .connected / .warning 两个分支里,.off / .setupNeeded / .missing /
// .notInstalled / .updateNeeded 下菜单**没有任何退出入口**,用户只能等下次
// 登录随 launchd 清场。main.swift 编不进 scripts/test-macos-menu.sh(它要
// AppKit),这条不变量只能读源码文本来守。
//
// 守的是「无条件」而不是「出现过」:把那一次挪回任意一个 case 里,它在函数体
// 里的花括号深度就不再是 0,守卫立刻转红——只数出现次数抓不到这种回退,因为
// 挪进 .connected 之后次数还是 1。
// (`if let inFlight` 那个提前 return 的进度浮层自带一次退出项,它在深度 1,
// 不参与本计数;恢复浮层没有退出项是记录在案的既有取舍,见 CLAUDE.md。)
func TestMacMenuQuitActionPresentInEveryState(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func rebuildMenu()")
	if !ok {
		t.Fatal("找不到 rebuildMenu 的函数体")
	}
	unconditional := 0
	depth := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && !strings.HasPrefix(trimmed, "//") && strings.Contains(trimmed, "quitBxActionTitle") {
			unconditional++
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
	if unconditional != 1 {
		t.Fatalf("退出项必须在 rebuildMenu 顶层(不在任何 case/if 里)无条件加一次,实际在顶层出现 %d 次", unconditional)
	}
}

// 轮询必须按菜单开合调频,不能再固定 5 秒。
//
// 原实现不论有没有人看都 5 秒 spawn 两个进程(`bx --version` + `bx status --json`),
// 而 macOS 上后者要跑完整观测、整轮封顶 5 秒——几乎满占空比。
func TestMacMenuPollsOnCadence(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "menuPollInterval(menuOpen:") {
		t.Fatal("刷新间隔必须来自 menuPollInterval,不得再硬编码")
	}
	if strings.Contains(text, "withTimeInterval: 5, repeats: true") {
		t.Fatal("固定 5 秒的轮询定时器仍在,调频没有生效")
	}
	// 菜单展开期间主 runloop 在 NSEventTrackingRunLoopMode,Timer.scheduledTimer
	// 只进 .default —— 实测在 tracking 模式下触发 0 次。打开档那 2 秒不挂进
	// .common 就等于不存在,而那正是它唯一该干活的时候。
	for _, want := range []string{"RunLoop.main.add(", "forMode: .common"} {
		if !strings.Contains(text, want) {
			t.Fatalf("轮询定时器必须挂进 .common 模式,缺 %q", want)
		}
	}
	// 定时器那一拍**不是**用户动作:声称是的话,被丢弃的每一拍都会补跑,
	// 而打开档 2 秒一拍、刷新可能 5 秒 —— 补跑接补跑,占空比 100%。
	reschedule, ok := swiftFunctionBody(text, "private func rescheduleRefreshTimer(")
	if !ok {
		t.Fatal("找不到 rescheduleRefreshTimer 的函数体")
	}
	// 只禁 true:userInitiated 现在没有默认值,轮询那一拍必须显式写 false。
	if strings.Contains(reschedule, "userInitiated: true") {
		t.Fatal("轮询定时器不得把自己那一拍标成用户动作,否则被丢弃的每一拍都补跑、占空比 100%")
	}
	if !strings.Contains(reschedule, "userInitiated: false") {
		t.Fatal("轮询那一拍必须显式表态(userInitiated: false)")
	}
}

// 菜单对象必须始终是同一个,重填就地做。
//
// 两个后果都不产生编译错误、也不会被别的测试抓到:① AppKit 在用户点击那一刻就
// 捕获了当时的菜单对象,rebuildMenu 换个新的只会在**下一次**打开时才出现,用户
// 看到的永远是上一拍的数据;② delegate 只在 configureMenu 里设一次,菜单对象一换
// 就再也不触发 menuWillOpen/menuDidClose,轮询永久停在关闭档(30 秒)。
func TestMacMenuRebuildsMenuInPlace(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	body, ok := swiftFunctionBody(text, "private func rebuildMenu()")
	if !ok {
		t.Fatal("找不到 rebuildMenu 的函数体")
	}
	if strings.Contains(body, "statusItem.menu =") {
		t.Fatal("rebuildMenu 不得更换 statusItem.menu:换对象等于换掉 delegate,且新菜单要到下一次打开才可见")
	}
	if !strings.Contains(body, "removeAllItems()") {
		t.Fatal("rebuildMenu 必须就地清空重填(removeAllItems)")
	}
	configure, ok := swiftFunctionBody(text, "private func configureMenu()")
	if !ok {
		t.Fatal("找不到 configureMenu 的函数体")
	}
	if !strings.Contains(configure, "menu.delegate = self") {
		t.Fatal("菜单 delegate 必须在 configureMenu 里设上,否则 menuWillOpen/menuDidClose 不触发、轮询永久停在关闭档")
	}
}

// 恢复状态的写回必须带代际号,陈旧的一律丢弃。
//
// 采集移到后台线程之后,refresh() 在 t 采样输入、applyRefresh 在 t+Δ(最长 5 秒)
// 写回结果,窗口里有三个主线程写者(reconnectBx / publishRecovery / pollRecovery
// 的终态清理)。放行陈旧写回会复活一个已经结束的恢复浮层,并重新武装观察者 ——
// 若 Guardian 此时已开始另一次恢复,recoveryID 对不上就会对一次**成功**的恢复
// 报出红色的「Reconnect Failed / Recovery was replaced」。
func TestMacMenuDropsStaleRecoveryWriteBack(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	// 两个载体都要在写入时 bump —— 用 didSet 而不是逐个改写者,是因为漏掉任何
	// 一个写者都不会有编译错误,只会在真机上偶发一次假红。
	if got := strings.Count(text, "didSet { recoveryGeneration.bump() }"); got != 2 {
		t.Fatalf("recoverySnapshot 与 reconnectInFlight 都必须在写入时 bump 代际号,实际 %d 处", got)
	}
	body, ok := swiftFunctionBody(text, "private func applyRefresh(")
	if !ok {
		t.Fatal("找不到 applyRefresh 的函数体")
	}
	gate := strings.Index(body, "acceptsWriteBack(captured:")
	write := strings.Index(body, "recoverySnapshot = outcome.recoverySnapshot")
	if gate < 0 || write < 0 || gate > write {
		t.Fatal("恢复快照的写回必须先过代际号检查,否则陈旧结果会复活已经结束的恢复浮层")
	}
	refresh, ok := swiftFunctionBody(text, "private func refresh(")
	if !ok {
		t.Fatal("找不到 refresh 的函数体")
	}
	if !strings.Contains(refresh, "recoveryGeneration.value") {
		t.Fatal("refresh 必须在采样输入的同时记下代际号")
	}
}

// 本文件里不许出现 Timer.scheduledTimer:它只进 .default 模式,而菜单展开期间
// 主 runloop 在 NSEventTrackingRunLoopMode —— 实测触发 0 次。冻住的正是菜单开着时
// 唯一在动的两样:图标呼吸,以及「Connecting — N 秒」那个计数器。后者是阶段①的
// 全部交付物,它存在的理由就是 2026-08-04 那次 bx down 卡了 71 分钟而菜单是死的;
// 一个冻住的计数器正是我们要消灭的症状本身。
func TestMacMenuTimersRunInCommonMode(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "///") {
			continue
		}
		if strings.Contains(trimmed, "Timer.scheduledTimer") {
			t.Fatalf("第 %d 行仍用 Timer.scheduledTimer(只进 .default,菜单展开时不触发),改用 commonModeTimer:%s", i+1, trimmed)
		}
	}
}

// 每个 refresh 调用点都必须显式表态是不是用户动作。
//
// 漏传的代价是**静默的**:用户刚开完/关完保护,那次刷新若撞上在途的一次就被丢掉
// 且不补跑,菜单要到下一个自然拍才纠正(关闭档最长 30 秒),没有任何报错 ——
// 而 startBx/turnOffBx/setup 正是全 app 最常见的三个动作。
//
// 主防线是 Swift 那边把 userInitiated 声明成**没有默认值**,编译器强制每个调用点
// 当场表态,连将来新增的也跑不掉;这条守卫是它在 CI 里的替身(CI 不编 Swift app),
// 顺带钉住「不许有人后来给它补一个默认值」。
func TestMacMenuRefreshCallsDeclareUserIntent(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "func refresh(userInitiated: Bool = ") {
		t.Fatal("userInitiated 不得有默认值:新加的调用点会默默落进「不补跑」那一档,而漏传是静默失败")
	}
	if !strings.Contains(text, "func refresh(userInitiated: Bool)") {
		t.Fatal("找不到 refresh(userInitiated:) 的声明")
	}
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, "refresh()") {
			t.Fatalf("第 %d 行是裸 refresh():必须显式写明 userInitiated —— %s", i+1, trimmed)
		}
	}
}

// 子进程的管道必须在 waitUntilExit 之前排空。
//
// 反过来写会在子进程输出超过管道缓冲区(约 64KB)时死锁:子进程阻塞在 write、
// 我们阻塞在 wait。今天四条命令的输出都只有几 KB、够不着,但这条路径跑在
// refreshGate 后面 —— 一旦死锁,闸门永久关死,菜单无声无息地停止更新。
func TestMacMenuDrainsSubprocessPipesBeforeWaiting(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := swiftFunctionBody(string(source), "private func runBx(")
	if !ok {
		t.Fatal("找不到 runBx 的函数体")
	}
	// 只看代码:注释里解释这个坑时也会写到这两个名字。
	var code []string
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			code = append(code, line)
		}
	}
	body := strings.Join(code, "\n")
	drain := strings.Index(body, "readDataToEndOfFile")
	wait := strings.Index(body, "waitUntilExit")
	if drain < 0 || wait < 0 {
		t.Fatal("runBx 里找不到管道读取或进程等待")
	}
	if drain > wait {
		t.Fatal("必须先排空管道再等退出:反过来会在输出超过 64KB 时死锁,并把 refreshGate 永久锁死")
	}
}

// 菜单栏 app 里**用户看得见的字**必须全英文。
//
// 阶段①的开关文案(Connecting / Disconnecting、逾时提示、三条失败指引)与阶段②
// 的数据行同处一个菜单,一中一英是两套语气拼在一起。代码注释不在此列 —— 它们
// 是写给读源码的人的,项目通篇中文注释。
//
// 判据是「非注释部分不得出现中日韩字符」:Sources 里的 CJK 只可能出现在注释或
// 字符串里,注释剥掉之后剩下的就是用户看得见的那部分。
func TestMacMenuUserFacingStringsAreEnglish(t *testing.T) {
	dir := filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".swift") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		raw := strings.Split(string(source), "\n")
		for i, code := range swiftCodeLines(string(source)) {
			line := raw[i]
			for _, r := range code {
				if isCJK(r) {
					t.Errorf("%s:%d 用户可见的字必须是英文(注释不在此列):%s",
						entry.Name(), i+1, strings.TrimSpace(line))
					break
				}
			}
		}
	}
	if scanned == 0 {
		t.Fatal("一个 Swift 源文件都没扫到,守卫等于没跑")
	}
}

// 把源码逐行剥掉注释,返回与输入等长的「只剩代码」的行数组(下标即行号-1)。
//
// 行注释与**块注释**都要处理:`/* 中文 */` 若不剥,CJK 守卫会对一段合法的中文
// 注释误报。`//` 与 `/*` 只有在**不处于字符串字面量中**时才算注释起点,
// 否则 "https://…" 这类字面量会被从中间截断。块注释状态跨行保持。
func swiftCodeLines(source string) []string {
	lines := strings.Split(source, "\n")
	out := make([]string, len(lines))
	inBlock := false
	for i, line := range lines {
		runes := []rune(line)
		var code []rune
		inString := false
		for j := 0; j < len(runes); j++ {
			r := runes[j]
			switch {
			case inBlock:
				if r == '*' && j+1 < len(runes) && runes[j+1] == '/' {
					inBlock = false
					j++
				}
			case inString:
				code = append(code, r)
				if r == '\\' && j+1 < len(runes) {
					j++ // 跳过被转义的那个字符,别让 \" 结束字符串
					code = append(code, runes[j])
					continue
				}
				if r == '"' {
					inString = false
				}
			case r == '"':
				inString = true
				code = append(code, r)
			case r == '/' && j+1 < len(runes) && runes[j+1] == '/':
				j = len(runes) // 行注释:本行到此为止
			case r == '/' && j+1 < len(runes) && runes[j+1] == '*':
				inBlock = true
				j++
			default:
				code = append(code, r)
			}
		}
		out[i] = string(code)
	}
	return out
}

// 折叠连续空白,让按行钉死的表达式不被重新缩进/换空格弄红。
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // 汉字
		return true
	case r >= 0x3000 && r <= 0x303F: // 中日韩标点(、。「」——)
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // 全角形式(,:;)
		return true
	}
	return false
}

// 数据行的异常计数必须真的驱动图标。
//
// 三态行模型(ok / bad / unknown)整个存在的理由就是这一条:`.connected` 不是
// 「一切都好」的同义词 —— 隧道不健康、DNS 掉管,报告仍然解码成功、状态仍然是
// `.connected`,而图标若只看 `state` 就会画一面**实心绿盾**,同时菜单正文里那行
// 明晃晃写着 "Tunnel unhealthy ✗"。图标是余光扫过唯一看得到的东西,正文没人盯着。
//
// 判定(哪些行算 bad、unknown 为什么不计入)在 MenuRows.swift 由 MenuRowsTests
// 钉着,但**接不接线在 main.swift**:把 `menuRowsNow().anomalyCount > 0 ? … :`
// 删掉,Swift 照样编译、整套测试照样全绿,只是绿盾开始撒谎。
func TestMacMenuAnomalyCountDrivesTheIcon(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func menuIconStateNow()")
	if !ok {
		t.Fatal("找不到 menuIconStateNow 的函数体")
	}
	// 只看代码:注释里解释这条不变量时也会写到 anomalyCount。
	var code []string
	for _, line := range strings.Split(body, "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			code = append(code, line)
		}
	}
	decision := ""
	for _, line := range code {
		if strings.Contains(line, "anomalyCount") {
			decision = strings.TrimSpace(line)
		}
	}
	if decision == "" {
		t.Fatal("图标必须看数据行的异常计数:隧道不健康时 state 仍是 .connected,只看 state 会画出一面撒谎的绿盾")
	}
	// 计数必须**就地决定**返回哪一态,而且**谓词本身也要钉死**。
	//
	// 这条守卫被收紧过两次,两次都是自己的假绿:
	// ① 第一版只在整个函数体里找 anomalyCount 与 .attention,被
	//    `_ = menuRowsNow().anomalyCount` + 无条件 `return .protected` 骗过;
	// ② 第二版按行查 `.attention`/`.protected`/`return` 三个词都在,却仍被
	//    `anomalyCount < 0 ? .attention : .protected` 骗过 —— 计数永远不小于 0,
	//    于是盾牌恒为实心绿,而菜单正文里那行正写着 `Tunnel unhealthy ✗`。
	// 谓词是一句机械的表达式,原样钉住即可;改写它的人应当先读这段。
	const decisionExpr = "anomalyCount > 0 ? .attention : .protected"
	if !strings.Contains(collapseSpaces(decision), decisionExpr) {
		t.Fatalf("异常计数必须就地按 %q 决定图标(不得算了又丢掉、也不得改谓词方向):实际那一行 = %q",
			decisionExpr, decision)
	}
	// `.unknown` 不计入异常是 MenuRows 那边的判定;这里顺带钉住 main.swift 不许
	// 绕过 anomalyCount 自己数行(那会把「没问出来」重新压成「坏了」)。
	if strings.Contains(strings.Join(code, "\n"), ".mark ==") {
		t.Fatal("main.swift 不得自己数行:三态的取舍(unknown 不算异常)只能由 MenuRows.anomalyCount 说了算")
	}
}

// `.updateNeeded` 画裂盾是**经过裁决保留**的,不是抄来的。
//
// 「CLI 太旧」与「流量可能没被保护」紧急程度不同,共用一个字形值得怀疑。四态固定,
// 可选只有两个:空心盾(.off)会**断言**「没在保护」—— 而 `.updateNeeded` 这条路径
// 在跑 `bx status --json` 之前就返回了,菜单对保护开没开一无所知,那是一句它无权
// 说的话,还偏偏是四态里最安静最容易被忽略的一个;裂盾说的是「要看一眼」,而
// 「指示灯读不到状态」正是要看一眼的事。紧急程度的差别由菜单正文承担(副标题
// "Update Required"、状态行 "Update bx CLI")。
//
// 这条守卫不是防抖动,是**要求下一个想改它的人先读那段裁决**:改法本身没有编译
// 错误、也没有别的测试会红。
func TestMacMenuUpdateNeededSharesTheAttentionGlyphDeliberately(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func menuIconStateNow()")
	if !ok {
		t.Fatal("找不到 menuIconStateNow 的函数体")
	}
	if !strings.Contains(body, "case .warning, .updateNeeded:") {
		t.Fatal("`.updateNeeded` 的图标归属被改动了:先读 menuIconStateNow 里那段裁决——" +
			"空心盾会断言「没在保护」,而这条路径在跑 bx status --json 之前就返回了,菜单无权那么说")
	}
	// 裁决必须留在代码旁边。删掉理由、只留下这一行 case,下一个人就只看得到一个
	// 看起来像是随手复用的分支。
	if !strings.Contains(body, "bx status --json") {
		t.Fatal("共用裂盾的理由必须写在 menuIconStateNow 里,否则它读起来只是一次随手复用")
	}
}

// 刷新闸门必须被释放,且释放必须是无条件的。
//
// RefreshGate.begin 一旦返回 true,闸门就关上了;不 end,菜单**从此不再更新** ——
// 定时器照跑、每一拍都被 begin 挡回去,图标停在最后一次成功刷新的样子,没有报错、
// 没有卡顿、没有任何症状。评审用变异坐实过:删掉 applyRefresh 里那次 end(),
// 整套测试全绿。RefreshGate 自己的规则在 MenuCadence.swift 由 MenuCadenceTests
// 钉着,但**调不调它在 main.swift**,而 main.swift 编不进 test-macos-menu.sh。
//
// 守的是「无条件」:end() 落进任何 if/guard/catch 里,失败那条路上就会漏掉它,
// 而失败恰恰是最需要下一拍能重来的时候。
func TestMacMenuAlwaysReleasesRefreshGate(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	refresh, ok := swiftFunctionBody(text, "private func refresh(")
	if !ok {
		t.Fatal("找不到 refresh 的函数体")
	}
	if !strings.Contains(refresh, "refreshGate.begin(") {
		t.Fatal("refresh 必须过闸门,否则并发刷新会堆成一串拿到时已作废的结果")
	}
	apply, ok := swiftFunctionBody(text, "private func applyRefresh(")
	if !ok {
		t.Fatal("找不到 applyRefresh 的函数体")
	}
	// 落定路径必须释放闸门。applyRefresh 是 refresh 唯一的收尾点(采集回主线程后
	// 一次性落定),begin 与 end 因此必须在这一对函数里配平。
	if !strings.Contains(apply, "refreshGate.end()") {
		t.Fatal("applyRefresh 必须释放刷新闸门,否则菜单会无声无息地永久停止更新")
	}
	// 释放必须在函数体顶层(不在任何 case/if/闭包里)出现:一旦被条件包住,
	// 就有一条路径会带着关死的闸门返回。
	unconditional := 0
	depth := 0
	for _, line := range strings.Split(apply, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 0 && !strings.HasPrefix(trimmed, "//") && strings.Contains(trimmed, "refreshGate.end()") {
			unconditional++
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
	if unconditional != 1 {
		t.Fatalf("refreshGate.end() 必须在 applyRefresh 顶层无条件调用一次,实际顶层出现 %d 次", unconditional)
	}
	// 早退那条路(begin 返回 false)绝不能 end:那次刷新根本没占住闸门,
	// 替别人放掉闸门等于让两次采集同时在跑。
	guardLine := ""
	for _, line := range strings.Split(refresh, "\n") {
		if strings.Contains(line, "refreshGate.begin(") {
			guardLine = line
		}
	}
	if !strings.Contains(guardLine, "guard ") && !strings.Contains(guardLine, "if ") {
		t.Fatalf("refresh 必须在 begin 返回 false 时早退,实际那一行 = %q", guardLine)
	}
	if strings.Contains(refresh, "refreshGate.end()") {
		t.Fatal("refresh 自己不得 end:被闸门挡回去的那一次没占住闸门,替别人放掉等于允许两次采集并行")
	}
}

// 后台采集的结果必须先回主队列再落定。
//
// applyRefresh 碰的全是 AppKit 对象(statusItem.button 的图像与 alpha、NSMenu 的
// 增删项)。AppKit 不是线程安全的:在 DispatchQueue.global 上直接调它不会崩在
// 当场,而是偶发 —— 菜单项错乱、图标不更新、随机 crash,且**只在真机上、只在
// 采集恰好慢过一次点击时**发生。评审用变异坐实过:去掉这一跳,整套测试全绿。
//
// 同一条要求也落在 loadState 的反面(它 spawn 四个子进程,必须留在后台),
// 那半边由 TestMacMenuRefreshRunsSubprocessesOffMainThread 守;这里守的是回程。
func TestMacMenuAppliesRefreshOnMainQueue(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func refresh(")
	if !ok {
		t.Fatal("找不到 refresh 的函数体")
	}
	apply := strings.Index(body, "applyRefresh(")
	if apply < 0 {
		t.Fatal("refresh 里找不到 applyRefresh 调用")
	}
	hop := strings.Index(body, "DispatchQueue.main.async")
	if hop < 0 {
		t.Fatal("后台采集的结果必须回主队列再落定:applyRefresh 全程在碰 AppKit 对象,off-main 是偶发失败而不是当场报错")
	}
	if hop > apply {
		t.Fatal("回主队列那一跳必须在 applyRefresh 之前")
	}
	// 采集那一跳仍必须在前面 —— 否则「回主队列」就成了从主线程跳到主线程,
	// 子进程照样在主线程上跑。
	background := strings.Index(body, "DispatchQueue.global")
	if background < 0 || background > hop {
		t.Fatal("必须先甩到后台队列采集、再回主队列落定")
	}
}

// 图标的可访问性接线:形态是主要载体,动效可被关掉,颜色可被系统接管。
//
// 这三条判定都在 MenuIcon.swift 里由 Swift 单测钉着,但**接不接线在 main.swift**,
// 而 main.swift 编不进 scripts/test-macos-menu.sh。漏接线不会有任何测试转红:
// 图标照样画得出来,只是「减弱动态效果」被无视、灰色盾在深色菜单栏上消失。
func TestMacMenuIconRespectsReduceMotionAndSystemTint(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "accessibilityDisplayShouldReduceMotion") {
		t.Fatal("图标必须尊重系统的「减弱动态效果」——菜单栏常驻视野边缘,是最不该无视它的地方")
	}
	// 四态一律 template,由系统按菜单栏明暗上色。这比原先「无色两态才 template」
	// 更严:任何一态自绘颜色,都必然在浅色或深色菜单栏之一里看不见。
	if !strings.Contains(text, "image.isTemplate = true") {
		t.Fatal("图标必须是 template:写死颜色必有一种菜单栏明暗下看不见")
	}
	if strings.Contains(text, "image.isTemplate = false") {
		t.Fatal("isTemplate 不得为 false")
	}
	if strings.Contains(text, "systemGreen") || strings.Contains(text, "systemYellow") {
		t.Fatal("图标不得自绘颜色:形态承担全部信息,颜色只会在某一种菜单栏下失效")
	}
}

// 刷新的子进程一律不在主线程跑。
//
// 一次刷新 spawn 四个 bx 子进程,其中 status --json 在 macOS 上要跑完整观测、
// 整轮封顶 5 秒。同步跑在主线程上,菜单就会在点击与出现之间肉眼可见地卡住,
// 菜单开着时更是每一拍都冻一次。
func TestMacMenuRefreshRunsSubprocessesOffMainThread(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	// 前缀匹配:refresh 现在带 userInitiated 参数(定时器那一拍不补跑)。
	body, ok := swiftFunctionBody(string(source), "private func refresh(")
	if !ok {
		t.Fatal("找不到 refresh 的函数体")
	}
	dispatch := strings.Index(body, "DispatchQueue.global")
	if dispatch < 0 {
		t.Fatal("refresh 必须把采集甩到后台队列,不能在主线程 spawn 子进程")
	}
	load := strings.Index(body, "loadState(")
	if load < 0 {
		t.Fatal("refresh 里找不到 loadState 调用")
	}
	if load < dispatch {
		t.Fatal("loadState 必须在后台队列里调用 —— 它 spawn 四个子进程,其中一个封顶 5 秒")
	}
}
