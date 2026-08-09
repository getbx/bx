package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
)

func TestLocalAPIStatusIsReadableWithoutPeerCredentials(t *testing.T) {
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: 42, Protection: ProtectionProtected},
		recoveryCurrent: RecoverySnapshot{
			ID:         "recovery-8",
			State:      "running",
			Stage:      "transport_health",
			Reason:     "underlay_changed",
			Generation: "wifi-b",
			Attempt:    2,
		},
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CorePID != 42 || got.Protection != ProtectionRecovering ||
		got.NetworkGeneration != "wifi-b" || got.Recovery.ID != "recovery-8" {
		t.Fatalf("response = %+v", got)
	}
}

func TestLocalAPIStatusNormalizesDNSState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state DNSState
		want  DNSState
	}{
		{name: "zero value", want: DNSUnknown},
		{name: "invalid value", state: DNSState("invalid"), want: DNSUnknown},
		{name: "managed", state: DNSManaged, want: DNSManaged},
		{name: "unmanaged", state: DNSUnmanaged, want: DNSUnmanaged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := &fakeController{status: Status{
				SchemaVersion: 1,
				Desired:       DesiredOn,
				Phase:         PhaseCommitted,
				Protection:    ProtectionProtected,
				DNSState:      tc.state,
			}}
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			var got Status
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.DNSState != tc.want {
				t.Fatalf("dns_state = %q, want %q", got.DNSState, tc.want)
			}
		})
	}
}

func TestObservableStatusNeedsAttentionOutranksAcceptedRecovery(t *testing.T) {
	controller := &fakeController{
		status: Status{
			SchemaVersion: 1,
			Desired:       DesiredOn,
			Phase:         PhaseNeedsAttention,
			Protection:    ProtectionNeedsAttention,
			LastError:     "core_ownership_uncertain",
		},
		recoveryCurrent: RecoverySnapshot{State: "accepted", Stage: "queued", Reason: "manual"},
	}

	got := observableStatus(controller, controller, LocalAPIOptions{})
	if got.Protection != ProtectionNeedsAttention {
		t.Fatalf("protection = %q, want needs_attention", got.Protection)
	}
}

func TestLocalAPIStatusPreservesLatestNetworkGenerationAcrossManualRecovery(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core

	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "underlay_changed", Generation: "wifi-b"}); err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "succeeded" })

	if _, err := env.manager.RequestPathRecovery(RecoveryRequest{Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	core.waitForRequest(t)
	t.Cleanup(func() {
		core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{State: "succeeded", Stage: "succeeded"}})
	})

	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.NetworkGeneration != "wifi-b" || got.Recovery.Generation != "" || got.Recovery.Reason != "manual" {
		t.Fatalf("status = %+v, want latest network generation plus manual recovery snapshot", got)
	}
}

func TestLocalAPIMutationsRequireRootPeer(t *testing.T) {
	tests := []struct {
		name      string
		uid       uint32
		gotUID    bool
		wantCode  int
		wantCalls int
	}{
		{name: "missing credentials", wantCode: http.StatusForbidden},
		{name: "logged-in user", uid: 501, gotUID: true, wantCode: http.StatusForbidden},
		{name: "root", uid: 0, gotUID: true, wantCode: http.StatusOK, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected}}
			request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.upCalls != tt.wantCalls {
				t.Fatalf("Up calls = %d, want %d", controller.upCalls, tt.wantCalls)
			}
		})
	}
}

func TestLocalAPIDownReturnsControllerFailure(t *testing.T) {
	controller := &fakeController{downErr: errors.New("restore failed")}
	request := httptest.NewRequest(http.MethodPost, "/v1/down", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if controller.downCalls != 1 {
		t.Fatalf("Down calls = %d", controller.downCalls)
	}
}

// 调用方只看到 "guardian operation failed" 时无从下手;必须回传失败码——但只
// 有当这次失败真的通过 needsAttention 设置了 LastError(即 before != after)
// 时才回传,故 mutate 在失败前把它自己会做的事(设置 LastError)也做了,
// 模拟 Manager 内部真实调用 needsAttention 的效果。
func TestMutationHandlerReturnsFailureCodeAndLogs(t *testing.T) {
	var buf bytes.Buffer
	restore := swapGuardianLogOutput(&buf)
	defer restore()

	controller := &fakeController{}
	handler := mutationHandler(controller,
		func(context.Context) error {
			controller.simulateNeedsAttention("core_ownership_uncertain")
			return errors.New("inspect recorded Core PID 5129: boom")
		},
		newAcceptedMutations(), LocalAPIOptions{}, "/v1/up")

	rec := httptest.NewRecorder()
	handler(rec, rootMutationRequest(t))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "core_ownership_uncertain" {
		t.Errorf("响应必须带失败码,实际 body = %v", body)
	}
	// 原始错误可能含路径/链接/凭据,绝不能回传给调用方。
	if strings.Contains(rec.Body.String(), "boom") ||
		strings.Contains(rec.Body.String(), "5129") {
		t.Errorf("响应不得包含原始错误内容,实际 = %s", rec.Body.String())
	}
	// 但 Guardian 自己的日志(root-only)必须记全,否则排查无据。
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("Guardian 日志必须记录完整错误,实际 = %q", buf.String())
	}
}

// 「连续两次因同一原因失败」正是回传失败码这一功能存在的理由——用户那次
// bx up 就是同一原因反复失败。needsAttention 每次都真的执行了一遍,只是恰好
// 设成了同一个字符串值;若靠比较 LastError 的值判断"这次失败是否真的设置过
// 它",第二次会被误判为陈旧而丢弃,轮询时 code 就会时有时无,反而像"问题变了"。
// 必须靠"这次调用期间 needsAttention 是否真的跑过"(代际号)而非值比较来判断。
func TestMutationHandlerReturnsCodeOnRepeatedIdenticalFailure(t *testing.T) {
	controller := &fakeController{}
	failingMutate := func(context.Context) error {
		controller.simulateNeedsAttention("core_ownership_uncertain")
		return errors.New("inspect recorded Core PID 5129: boom")
	}
	handler := mutationHandler(controller, failingMutate, newAcceptedMutations(), LocalAPIOptions{}, "/v1/up")

	for attempt := 1; attempt <= 2; attempt++ {
		rec := httptest.NewRecorder()
		handler(rec, rootMutationRequest(t))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("第 %d 次:状态码 = %d, want 500", attempt, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "core_ownership_uncertain" {
			t.Fatalf("第 %d 次失败必须仍回传相同失败码,实际 body = %v", attempt, body)
		}
	}
}

// 真实失败路径里有好几条从不调用 needsAttention 就直接返回 err(如 Down 的
// DNS-restore-恢复成功分支清空 LastError 后返回,或任何深层错误直接冒泡):
// 此时 500 响应里若仍夹带 controller.Status().LastError,拿到的会是上一次
// 不相关失败留下的陈旧值(或空串),把排查者指向错误方向。宁可不带 code,
// 也不能带错的 code。
// (recoveryBlocked 与 acquireMutation 排队超时是例外:它们有各自的哨兵错误,
// 由 failureCodeForError 命名 —— 见
// TestMutationHandlerNamesRecoveryIncompleteAndBusyFailures。那不是陈旧值,
// 而是对本次失败的准确描述。)
func TestMutationHandlerOmitsStaleOrEmptyCodeWhenLastErrorUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		before string
		mutate func(*fakeController) func(context.Context) error
	}{
		{
			name:   "LastError 未变(模拟 acquireMutation 超时 / recoveryBlocked 直接 return err)",
			before: "stale_unrelated_code",
			mutate: func(*fakeController) func(context.Context) error {
				return func(context.Context) error { return context.DeadlineExceeded }
			},
		},
		{
			name:   "LastError 被清空为空串(模拟 Down 的 DNS-restore-恢复成功分支)",
			before: "stale_unrelated_code",
			mutate: func(c *fakeController) func(context.Context) error {
				return func(context.Context) error {
					c.status.LastError = ""
					return errors.New("restore managed DNS: dns restore failed")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := swapGuardianLogOutput(&buf)
			defer restore()

			controller := &fakeController{status: Status{LastError: tt.before}}
			handler := mutationHandler(controller, tt.mutate(controller), newAcceptedMutations(), LocalAPIOptions{}, "/v1/up")

			rec := httptest.NewRecorder()
			handler(rec, rootMutationRequest(t))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("状态码 = %d, want 500", rec.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if code, ok := body["code"]; ok {
				t.Errorf("LastError 未因这次失败而更新时不应回传 code,实际 code = %q", code)
			}
		})
	}
}

// newAcceptedMutations 构造一个开放接受中的 acceptedMutations,与 NewLocalAPI
// 内部构造的初始状态一致,供直接调用 mutationHandler/updateHandler 的测试使用。
func newAcceptedMutations() *acceptedMutations {
	return &acceptedMutations{accepting: true, drained: make(chan struct{})}
}

// rootRecoveryRequest 构造一个带 root peer 凭据(uid==0)的 /v1/recoveries POST
// 请求,满足 recoveryRequestHandler 的鉴权前提。
func rootRecoveryRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(body))
	return request.WithContext(withPeerCredentials(request.Context(), 0, true))
}

// recoveryRequestHandler 此前把 err 整个丢弃,只回固定文案——同 mutationHandler
// 的问题,brief 澄清后一并修:记录完整错误到 Guardian 日志,响应只带失败码
// (且只在这次失败真的更新了 LastError 时才带,同 issue① 的陈旧码约束)。
func TestRecoveryRequestHandlerReturnsFailureCodeAndLogs(t *testing.T) {
	var buf bytes.Buffer
	restore := swapGuardianLogOutput(&buf)
	defer restore()

	controller := &fakeController{
		recoveryErr:           errors.New("inspect recorded Core PID 5129: boom"),
		recoverySetsLastError: "recovery_admission_failed",
	}
	handler := recoveryRequestHandler(controller, controller, 0)

	rec := httptest.NewRecorder()
	handler(rec, rootRecoveryRequest(t, `{"reason":"manual"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "recovery_admission_failed" {
		t.Errorf("响应必须带失败码,实际 body = %v", body)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "5129") {
		t.Errorf("响应不得包含原始错误内容,实际 = %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("Guardian 日志必须记录完整错误,实际 = %q", buf.String())
	}
}

// 同 mutationHandler:若这次失败没有更新 LastError,500 响应不得夹带一个更早
// 不相关失败留下的陈旧 code。
func TestRecoveryRequestHandlerOmitsStaleCodeWhenLastErrorUnchanged(t *testing.T) {
	controller := &fakeController{
		status:      Status{LastError: "stale_unrelated_code"},
		recoveryErr: errors.New("path recovery admission failed"),
		// recoverySetsLastError intentionally left empty: this failure path
		// does not touch LastError, matching acquireMutation-timeout-style
		// real failures.
	}
	handler := recoveryRequestHandler(controller, controller, 0)

	rec := httptest.NewRecorder()
	handler(rec, rootRecoveryRequest(t, `{"reason":"manual"}`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if code, ok := body["code"]; ok {
		t.Errorf("LastError 未因这次失败而更新时不应回传 code,实际 code = %q", code)
	}
}

// migrationHandler 此前也原样丢弃 migration.Migrate 的错误——同一个问题,
// 一并按同样约束修:日志记全 + 只回失败码(且遵守"不回传陈旧码"约束)。
// 迁移失败(legacy Core 接管)是低频、高风险、极难诊断的场景,恰是本任务
// 要解决的。
func TestMigrationHandlerReturnsFailureCodeAndLogs(t *testing.T) {
	var buf bytes.Buffer
	restore := swapGuardianLogOutput(&buf)
	defer restore()

	controller := &fakeController{
		migrateErr:           errors.New("inspect recorded Core PID 5129: boom"),
		migrateSetsLastError: "legacy_core_migration_pending",
	}
	handler := migrationHandler(controller, controller, newAcceptedMutations(), LocalAPIOptions{})

	rec := httptest.NewRecorder()
	handler(rec, rootMigrationRequest(t))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "legacy_core_migration_pending" {
		t.Errorf("响应必须带失败码,实际 body = %v", body)
	}
	if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "5129") {
		t.Errorf("响应不得包含原始错误内容,实际 = %s", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("Guardian 日志必须记录完整错误,实际 = %q", buf.String())
	}
}

// 同 mutationHandler/recoveryRequestHandler:若这次失败没有更新 LastError,
// 500 响应不得夹带一个更早不相关失败留下的陈旧 code。
func TestMigrationHandlerOmitsStaleCodeWhenLastErrorUnchanged(t *testing.T) {
	controller := &fakeController{
		status:     Status{LastError: "stale_unrelated_code"},
		migrateErr: errors.New("migration admission failed"),
		// migrateSetsLastError intentionally left empty.
	}
	handler := migrationHandler(controller, controller, newAcceptedMutations(), LocalAPIOptions{})

	rec := httptest.NewRecorder()
	handler(rec, rootMigrationRequest(t))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if code, ok := body["code"]; ok {
		t.Errorf("LastError 未因这次失败而更新时不应回传 code,实际 code = %q", code)
	}
}

// rootMigrationRequest 构造一个带 root peer 凭据、通过校验的 /v1/migrate POST
// 请求,满足 migrationHandler 的鉴权与元数据前提。
func rootMigrationRequest(t *testing.T) *http.Request {
	t.Helper()
	body := `{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/migrate", strings.NewReader(body))
	return request.WithContext(withPeerCredentials(request.Context(), 0, true))
}

// rootMutationRequest 构造一个带 root peer 凭据(uid==0)的 POST 请求,满足
// mutationHandler/updateHandler/migrationHandler 的鉴权前提。
func rootMutationRequest(t *testing.T) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
	return request.WithContext(withPeerCredentials(request.Context(), 0, true))
}

func TestLocalAPIMigrateRequiresRootAndStrictMetadata(t *testing.T) {
	validBody := []byte(`{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"]}`)
	tests := []struct {
		name      string
		uid       uint32
		gotUID    bool
		body      []byte
		wantCode  int
		wantCalls int
	}{
		{name: "logged-in user", uid: 501, gotUID: true, body: validBody, wantCode: http.StatusForbidden},
		{name: "unknown secret field", uid: 0, gotUID: true, body: []byte(`{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"],"server_link":"vless://secret"}`), wantCode: http.StatusBadRequest},
		{name: "root metadata", uid: 0, gotUID: true, body: validBody, wantCode: http.StatusOK, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected}}
			request := httptest.NewRequest(http.MethodPost, "/v1/migrate", bytes.NewReader(tt.body))
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.migrateCalls != tt.wantCalls {
				t.Fatalf("Migrate calls = %d, want %d", controller.migrateCalls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && !reflect.DeepEqual(controller.migration, MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}) {
				t.Fatalf("migration request = %+v", controller.migration)
			}
		})
	}
}

func TestLocalAPIMigrateRejectsDataBeyondBodyLimit(t *testing.T) {
	controller := &fakeController{}
	body := []byte(`{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"]}`)
	body = append(body, bytes.Repeat([]byte(" "), (64<<10)-len(body))...)
	body = append(body, []byte(`{"server_link":"vless://secret"}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/migrate", bytes.NewReader(body))
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()

	NewLocalAPI(controller).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if controller.migrateCalls != 0 {
		t.Fatal("oversized migration metadata reached controller")
	}
}

func TestLocalAPIUpdateRequiresRootAndStrictMetadata(t *testing.T) {
	validBody := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz","app_path":"/Applications/Bx.app","app_uid":0,"app_gid":0}`)
	tests := []struct {
		name      string
		uid       uint32
		gotUID    bool
		body      []byte
		wantCode  int
		wantCalls int
	}{
		{name: "missing credentials", body: validBody, wantCode: http.StatusForbidden},
		{name: "non-root", uid: 501, gotUID: true, body: validBody, wantCode: http.StatusForbidden},
		{name: "unknown bypass field", uid: 0, gotUID: true, body: append(bytes.TrimSuffix(validBody, []byte("}")), []byte(`,"gateway":"192.0.2.1"}`)...), wantCode: http.StatusBadRequest},
		{name: "unknown secret field", uid: 0, gotUID: true, body: append(bytes.TrimSuffix(validBody, []byte("}")), []byte(`,"client_link":"vless://secret"}`)...), wantCode: http.StatusBadRequest},
		{name: "trailing JSON", uid: 0, gotUID: true, body: append(validBody, []byte(` {}`)...), wantCode: http.StatusBadRequest},
		{name: "root metadata", uid: 0, gotUID: true, body: validBody, wantCode: http.StatusOK, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{
				status:       Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
				updateResult: UpdateResult{FromVersion: "v1", ToVersion: "v2", Phase: PhaseCommitted, CoreActivated: true, ProtectionState: ProtectionProtected},
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(tt.body))
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()

			NewLocalAPI(controller).ServeHTTP(recorder, request)

			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.updateCalls != tt.wantCalls {
				t.Fatalf("Update calls = %d, want %d", controller.updateCalls, tt.wantCalls)
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "vless://") {
				t.Fatalf("response reflected secret metadata: %s", recorder.Body.String())
			}
		})
	}
}

func TestLocalAPIUpdateReturnsResultAndHidesControllerFailure(t *testing.T) {
	body := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz"}`)
	for _, tt := range []struct {
		name       string
		controller *fakeController
		wantCode   int
	}{
		{
			name: "result",
			controller: &fakeController{updateResult: UpdateResult{
				FromVersion: "v1", ToVersion: "v2", Phase: PhaseCommitted,
				CoreActivated: true, ProtectionState: ProtectionProtected,
			}},
			wantCode: http.StatusOK,
		},
		{
			name:       "failure redacted",
			controller: &fakeController{updateErr: errors.New("vless://user:password@example.test?token=secret")},
			wantCode:   http.StatusInternalServerError,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(body))
			request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
			recorder := httptest.NewRecorder()
			NewLocalAPI(tt.controller).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if strings.Contains(strings.ToLower(recorder.Body.String()), "password") || strings.Contains(strings.ToLower(recorder.Body.String()), "token=") {
				t.Fatalf("response leaked controller error: %s", recorder.Body.String())
			}
			if tt.wantCode == http.StatusOK {
				var got UpdateResult
				if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, tt.controller.updateResult) {
					t.Fatalf("result = %+v, want %+v", got, tt.controller.updateResult)
				}
			}
		})
	}
}

func TestLocalAPIUpdateRejectsDataBeyondBodyLimit(t *testing.T) {
	controller := &fakeController{}
	body := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz"}`)
	body = append(body, bytes.Repeat([]byte(" "), (64<<10)-len(body))...)
	body = append(body, []byte(`{"client_link":"vless://secret"}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(body))
	request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
	recorder := httptest.NewRecorder()

	NewLocalAPI(controller).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if controller.updateCalls != 0 {
		t.Fatal("oversized update metadata reached controller")
	}
}

func TestLocalAPIMutationOutlivesClientCancellation(t *testing.T) {
	controller := &fakeController{}
	request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
	requestContext, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(withPeerCredentials(requestContext, 0, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if controller.upContextErr != nil {
		t.Fatalf("accepted mutation inherited client cancellation: %v", controller.upContextErr)
	}
}

func TestRecoveryLocalAPIPostReturnsAcceptedWhileGetRemainsResponsive(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	handler := NewLocalAPI(env.manager, LocalAPIOptions{OwnerUID: 501})

	request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(`{"reason":"underlay_changed","generation":"wifi-b"}`))
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(recorder, request)
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("POST waited for Core work: %s", elapsed)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var accepted RecoverySnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.ID == "" || accepted.State != "accepted" {
		t.Fatalf("accepted snapshot = %+v", accepted)
	}
	core.waitForRequest(t)

	get := httptest.NewRequest(http.MethodGet, "/v1/recoveries/current", nil)
	get = get.WithContext(withPeerCredentials(get.Context(), 501, true))
	currentRecorder := httptest.NewRecorder()
	started = time.Now()
	handler.ServeHTTP(currentRecorder, get)
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("GET blocked behind Core work: %s", elapsed)
	}
	if currentRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", currentRecorder.Code, currentRecorder.Body.String())
	}
	var running RecoverySnapshot
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &running); err != nil {
		t.Fatal(err)
	}
	if running.ID != accepted.ID || running.State != "running" {
		t.Fatalf("running snapshot = %+v, accepted = %+v", running, accepted)
	}

	core.release(corePathResult{snapshot: supervisor.PathRecoverySnapshot{
		State: "succeeded", Stage: "succeeded", Detail: "vless://must-not-escape",
	}})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "succeeded" })
	currentRecorder = httptest.NewRecorder()
	handler.ServeHTTP(currentRecorder, get)
	var succeeded RecoverySnapshot
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &succeeded); err != nil {
		t.Fatal(err)
	}
	if succeeded.ID != accepted.ID || succeeded.State != "succeeded" || succeeded.Detail != "" {
		t.Fatalf("succeeded snapshot = %+v", succeeded)
	}
	if strings.Contains(currentRecorder.Body.String(), "vless://") {
		t.Fatalf("GET leaked Core detail: %s", currentRecorder.Body.String())
	}
}

func TestRecoveryLocalAPIAuthorizesOnlyRootOrConfiguredOwner(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		uid      uint32
		gotUID   bool
		wantCode int
	}{
		{name: "POST missing credentials", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, wantCode: http.StatusForbidden},
		{name: "POST unrelated user", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "POST owner", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, uid: 501, gotUID: true, wantCode: http.StatusAccepted},
		{name: "POST root", method: http.MethodPost, path: "/v1/recoveries", body: `{"reason":"manual"}`, uid: 0, gotUID: true, wantCode: http.StatusAccepted},
		{name: "GET missing credentials", method: http.MethodGet, path: "/v1/recoveries/current", wantCode: http.StatusForbidden},
		{name: "GET unrelated user", method: http.MethodGet, path: "/v1/recoveries/current", uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "GET owner", method: http.MethodGet, path: "/v1/recoveries/current", uid: 501, gotUID: true, wantCode: http.StatusOK},
		{name: "GET root", method: http.MethodGet, path: "/v1/recoveries/current", uid: 0, gotUID: true, wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{
				recoveryResult:  RecoverySnapshot{ID: "recovery-1", State: "accepted", Stage: "queued", Reason: "manual"},
				recoveryCurrent: RecoverySnapshot{ID: "recovery-1", State: "running", Stage: "core_recovery", Reason: "manual"},
			}
			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
		})
	}

	// 此处曾额外钉住「配置了 owner 时 /v1/up 仍 root-only」——那是
	// authorizeOwnerPeer 只覆盖 /v1/recoveries 时的旧策略。本任务把同一判据
	// 推广到 /v1/up、/v1/down(菜单栏日常开关),故该断言已被有意反转,
	// 覆盖范围由 TestLocalAPIUpDownAcceptsConfiguredOwner 承接
	// (root/owner/other-user/no-credentials 四态皆有专门用例)。
}

func TestRecoveryLocalAPIRejectsUnsafeMetadataAndRedactsCoreFailures(t *testing.T) {
	env := newProtectedManagerTestEnv(t)
	core := newFakeCorePathClient(false)
	env.manager.corePath = core
	handler := NewLocalAPI(env.manager, LocalAPIOptions{OwnerUID: 501})
	for _, body := range []string{
		`{"reason":"manual","client_link":"vless://user:password@example.test"}`,
		`{"reason":"vless://user:password@example.test"}`,
		`{"reason":"manual","generation":"token=secret value"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(body))
		request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("unsafe metadata status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "vless://") || strings.Contains(recorder.Body.String(), "token=") {
			t.Fatalf("validation response reflected secret: %s", recorder.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/recoveries", strings.NewReader(`{"reason":"manual"}`))
	request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	core.waitForRequest(t)
	secret := "vless://user:password@example.test?token=secret"
	core.release(corePathResult{
		snapshot: supervisor.PathRecoverySnapshot{State: "blocked", Stage: "blocked", ErrorCode: "transport_unavailable", Detail: secret},
		err:      &supervisor.PathRecoveryError{Code: "transport_unavailable", Detail: secret},
	})
	eventually(t, func() bool { return env.manager.CurrentPathRecovery().State == "failed" })

	get := httptest.NewRequest(http.MethodGet, "/v1/recoveries/current", nil)
	get = get.WithContext(withPeerCredentials(get.Context(), 501, true))
	currentRecorder := httptest.NewRecorder()
	handler.ServeHTTP(currentRecorder, get)
	var failed RecoverySnapshot
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.ErrorCode != "transport_unavailable" || failed.Detail != "" {
		t.Fatalf("failed snapshot = %+v", failed)
	}
	if strings.Contains(currentRecorder.Body.String(), "password") || strings.Contains(currentRecorder.Body.String(), "token=") {
		t.Fatalf("failure response leaked Core detail: %s", currentRecorder.Body.String())
	}
}

func TestRecoveryLocalAPIClientRequiresExactStatuses(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if request.Method == http.MethodGet {
			status = http.StatusAccepted
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"recovery_id":"recovery-1","state":"running","stage":"core_recovery","reason":"manual"}`)),
			Request:    request,
		}, nil
	})}}
	if _, err := client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"}); err == nil {
		t.Fatal("RequestRecovery accepted HTTP 200 instead of requiring 202")
	}
	if _, err := client.CurrentRecovery(context.Background()); err == nil {
		t.Fatal("CurrentRecovery accepted HTTP 202 instead of requiring 200")
	}
}

func TestClientUsesGuardianUnixAPI(t *testing.T) {
	controller := &fakeController{
		status:          Status{SchemaVersion: 1, Desired: DesiredOff, Phase: PhaseIdle, Protection: ProtectionOff},
		recoveryResult:  RecoverySnapshot{ID: "recovery-1", State: "accepted", Stage: "queued", Reason: "manual"},
		recoveryCurrent: RecoverySnapshot{ID: "recovery-1", State: "succeeded", Stage: "succeeded", Reason: "manual"},
	}
	socketDir := filepath.Join("/tmp", fmt.Sprintf("bxg-%d", os.Getpid()))
	_ = os.RemoveAll(socketDir)
	if err := os.Mkdir(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "guard.sock")
	uid := uint32(os.Geteuid())
	daemon, err := StartDaemon(context.Background(), DaemonOptions{
		SocketPath: socketPath,
		Handler:    NewLocalAPI(controller),
		OwnerUID:   uid,
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()

	client := NewClient(socketPath)
	if _, err := client.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Migrate(context.Background(), MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.10/32"}}); err != nil {
		t.Fatal(err)
	}
	updateRequest := UpdateRequest{
		TransactionID: "tx-1", FromVersion: "v1", ToVersion: "v2",
		AssetSHA256: strings.Repeat("a", 64), PackagePath: "/var/lib/bx/update/staging/tx-1/package.tar.gz",
	}
	if _, err := client.Update(context.Background(), updateRequest); err != nil {
		t.Fatal(err)
	}
	accepted, err := client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := client.CurrentRecovery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if controller.upCalls != 1 || controller.downCalls != 1 || controller.migrateCalls != 1 || controller.updateCalls != 1 || controller.recoveryCalls != 1 ||
		accepted.ID != "recovery-1" || current.State != "succeeded" || status.SchemaVersion != 1 {
		t.Fatalf("calls/recovery/status = %d/%d/%d/%d/%d %+v/%+v/%+v", controller.upCalls, controller.downCalls, controller.migrateCalls, controller.updateCalls, controller.recoveryCalls, accepted, current, status)
	}
}

func TestDaemonRefusesToReplaceNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guardian.sock")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := StartDaemon(context.Background(), DaemonOptions{SocketPath: path, Handler: http.NewServeMux(), OwnerUID: uint32(os.Geteuid())})
	if err == nil {
		t.Fatal("daemon replaced a non-socket")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "do not replace" {
		t.Fatalf("existing file changed: %q, %v", got, readErr)
	}
}

func TestDaemonShutdownDrainsAcceptedDetachedMutation(t *testing.T) {
	controller := &blockingController{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		status:  Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected},
	}
	ctx, cancel := context.WithCancel(context.Background())
	socketPath := filepath.Join(shortSocketDir(t), "guard.sock")
	daemon, err := StartDaemon(ctx, DaemonOptions{
		SocketPath: socketPath,
		Handler:    NewLocalAPI(controller),
		OwnerUID:   uint32(os.Geteuid()),
		PeerCredentials: func(net.Conn) (uint32, bool) {
			return 0, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() {
		_, err := NewClient(socketPath).Up(context.Background())
		requestDone <- err
	}()
	select {
	case <-controller.entered:
	case <-time.After(time.Second):
		t.Fatal("mutation was not accepted")
	}

	cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- daemon.Close() }()
	select {
	case err := <-closeDone:
		close(controller.release)
		<-requestDone
		t.Fatalf("daemon returned before accepted mutation drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer probeCancel()
	if _, err := NewClient(socketPath).Status(probeCtx); err == nil {
		close(controller.release)
		<-requestDone
		t.Fatal("daemon accepted a new request after shutdown began")
	}

	close(controller.release)
	if err := <-requestDone; err != nil {
		t.Fatalf("accepted mutation response failed during drain: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("graceful daemon shutdown: %v", err)
	}
}

func TestRemoveStaleSocketOnlyUnlinksOnConnectionRefused(t *testing.T) {
	path := makeOrphanedUnixSocket(t)
	err := removeStaleSocketWithDial(path, uint32(os.Geteuid()), func(context.Context, string) (net.Conn, error) {
		return nil, syscall.ECONNREFUSED
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket still exists: %v", err)
	}
}

func TestRemoveStaleSocketRetainsSocketOnAmbiguousDialErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "resource exhaustion", err: syscall.EMFILE},
		{name: "permission", err: syscall.EACCES},
		{name: "unknown", err: errors.New("unclassified dial failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := makeOrphanedUnixSocket(t)
			err := removeStaleSocketWithDial(path, uint32(os.Geteuid()), func(context.Context, string) (net.Conn, error) {
				return nil, tt.err
			})
			if err == nil {
				t.Fatalf("ambiguous dial error %v treated as stale", tt.err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("socket removed after ambiguous dial error: %v", err)
			}
		})
	}
}

func makeOrphanedUnixSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(shortSocketDir(t), "stale.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		listener.Close()
		t.Fatal("unix listener type unavailable")
	}
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bxg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestGuardianSocketLivesInOwnedRuntimeDir(t *testing.T) {
	if filepath.Dir(SocketPath) != RuntimeDir {
		t.Fatalf("SocketPath %q must live under %q", SocketPath, RuntimeDir)
	}
	if RuntimeDir == "/var/run" {
		t.Fatal("Guardian must not put its socket directly in the shared /var/run")
	}
	if len(SocketPath) >= 104 {
		t.Fatalf("SocketPath %q exceeds sun_path limit", SocketPath)
	}
}

func TestStartDaemonAcceptsGroupWritableParentOfOwnedDir(t *testing.T) {
	// 复刻 macOS:/var/run 组可写,但我们只要求自有子目录本身干净。
	// 用 shortSocketDir 而非 t.TempDir():macOS 上 t.TempDir() 落在
	// /var/folders/.../T/<很长的测试名>/001 下,加上 "run/bx/guardian.sock"
	// 常年会超出 AF_UNIX 的 sun_path 104 字节上限(bind: invalid argument),
	// 与 secdir 的正确性无关,纯粹是路径长度问题——同文件里已有的
	// shortSocketDir(t)(/tmp/bxg-*)正是为此而设的既有约定。
	root := shortSocketDir(t)
	shared := filepath.Join(root, "run")
	if err := os.Mkdir(shared, 0o775); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(shared, "bx")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon, err := StartDaemon(ctx, DaemonOptions{
		SocketPath: filepath.Join(owned, "guardian.sock"),
		Handler:    http.NewServeMux(),
		OwnerUID:   uint32(os.Geteuid()),
	})
	if err != nil {
		t.Fatalf("StartDaemon under group-writable grandparent: %v", err)
	}
	defer daemon.Close()
	if _, err := os.Stat(filepath.Join(owned, "guardian.sock")); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
}

type fakeController struct {
	status       Status
	upCalls      int
	downCalls    int
	migrateCalls int
	updateCalls  int
	migration    MigrationRequest
	update       UpdateRequest
	updateResult UpdateResult
	upErr        error
	downErr      error
	migrateErr   error
	updateErr    error
	upContextErr error
	// migrateSetsLastError, if non-empty and migrateErr != nil, is applied via
	// simulateNeedsAttention before Migrate returns — see recoverySetsLastError.
	migrateSetsLastError string

	recoveryCalls   int
	recoveryRequest RecoveryRequest
	recoveryResult  RecoverySnapshot
	recoveryCurrent RecoverySnapshot
	recoveryErr     error
	// recoverySetsLastError, if non-empty and recoveryErr != nil, is applied via
	// simulateNeedsAttention before RequestPathRecovery returns — simulating a
	// real needsAttention side effect so tests can distinguish "this failure
	// set a fresh code" from "this failure left LastError untouched/stale".
	recoverySetsLastError string
}

// simulateNeedsAttention mimics the side effect of a real Manager calling
// needsAttention during a failing mutation: it sets LastError *and* bumps
// LastErrorGeneration. Tests use this (rather than only setting LastError)
// because failureResponseBody keys off the generation counter, not the
// string value — see the comment on failureResponseBody in localapi.go for
// why a value comparison is insufficient (it breaks on two consecutive
// failures with the same code).
func (c *fakeController) simulateNeedsAttention(code string) {
	c.status.LastError = code
	c.status.LastErrorGeneration++
}

type blockingController struct {
	status  Status
	entered chan struct{}
	release chan struct{}
}

func (c *blockingController) Status() Status { return c.status }

func (c *blockingController) Up(ctx context.Context) error {
	close(c.entered)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingController) Down(context.Context) error { return nil }

func (c *fakeController) Status() Status { return c.status }

func (c *fakeController) Up(ctx context.Context) error {
	c.upContextErr = ctx.Err()
	c.upCalls++
	return c.upErr
}

func (c *fakeController) Down(context.Context) error {
	c.downCalls++
	return c.downErr
}

func (c *fakeController) Migrate(_ context.Context, request MigrationRequest) error {
	c.migrateCalls++
	c.migration = request
	if c.migrateErr != nil && c.migrateSetsLastError != "" {
		c.simulateNeedsAttention(c.migrateSetsLastError)
	}
	return c.migrateErr
}

func (c *fakeController) Update(_ context.Context, request UpdateRequest) (UpdateResult, error) {
	c.updateCalls++
	c.update = request
	return c.updateResult, c.updateErr
}

func (c *fakeController) RequestPathRecovery(request RecoveryRequest) (RecoverySnapshot, error) {
	c.recoveryCalls++
	c.recoveryRequest = request
	if c.recoveryErr != nil && c.recoverySetsLastError != "" {
		c.simulateNeedsAttention(c.recoverySetsLastError)
	}
	return c.recoveryResult, c.recoveryErr
}

func (c *fakeController) CurrentPathRecovery() RecoverySnapshot {
	return c.recoveryCurrent
}

func TestLocalAPIStatusExposesVersions(t *testing.T) {
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: 42, Protection: ProtectionProtected},
	}
	options := LocalAPIOptions{
		OwnerUID:        0,
		GuardianVersion: "9.9.9",
		RuntimeVersion: func() string {
			return "8.8.8"
		},
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, options).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GuardianVersion != "9.9.9" {
		t.Fatalf("guardian_version = %q, want 9.9.9", got.GuardianVersion)
	}
	if got.RuntimeVersion != "8.8.8" {
		t.Fatalf("runtime_version = %q, want 8.8.8", got.RuntimeVersion)
	}
}

// 版本字段必须跟着**每一个**回 Status 的响应走,不只 GET /v1/status。
//
// `bx up` 只看 POST /v1/up 的响应:Up 一报 Protected,waitGuardianProtected 就
// 立刻返回,不会再补一次 GET。只在 GET 上填版本,等于让「Guardian 仍在跑旧版」
// 这条提示在它唯一该出现的场合恒为空(2026-08-08 复审 C2)。/v1/down 与
// /v1/migrate 同理:菜单与 legacy 迁移读的都是 mutation 的响应。
func TestMutationResponsesCarryVersions(t *testing.T) {
	options := LocalAPIOptions{
		OwnerUID:        0,
		GuardianVersion: "9.9.9",
		RuntimeVersion:  func() string { return "8.8.8" },
	}
	for _, tc := range []struct {
		path string
		body string
	}{
		{path: "/v1/up"},
		{path: "/v1/down"},
		{path: "/v1/migrate", body: `{"gateway":"192.0.2.1","server_bypass":["198.51.100.10/32"]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			controller := &fakeController{
				status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: 42, Protection: ProtectionProtected},
			}
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			request := httptest.NewRequest(http.MethodPost, tc.path, body)
			request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, options).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("POST %s = %d, body=%s", tc.path, recorder.Code, recorder.Body.String())
			}
			var got Status
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.GuardianVersion != "9.9.9" || got.RuntimeVersion != "8.8.8" {
				t.Fatalf("POST %s 回的版本 = (%q, %q),want (9.9.9, 8.8.8):客户端只看这一个响应",
					tc.path, got.GuardianVersion, got.RuntimeVersion)
			}
		})
	}
}

func TestLocalAPIStatusOmitsVersionsWhenNotProvided(t *testing.T) {
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: 42, Protection: ProtectionProtected},
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GuardianVersion != "" {
		t.Fatalf("guardian_version = %q, want empty", got.GuardianVersion)
	}
	if got.RuntimeVersion != "" {
		t.Fatalf("runtime_version = %q, want empty", got.RuntimeVersion)
	}
}

func TestLocalAPIStatusHandlesNilRuntimeVersionFunc(t *testing.T) {
	controller := &fakeController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, CorePID: 42, Protection: ProtectionProtected},
	}
	options := LocalAPIOptions{
		OwnerUID:        0,
		GuardianVersion: "9.9.9",
		RuntimeVersion:  nil,
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, options).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GuardianVersion != "9.9.9" {
		t.Fatalf("guardian_version = %q, want 9.9.9", got.GuardianVersion)
	}
	if got.RuntimeVersion != "" {
		t.Fatalf("runtime_version = %q, want empty", got.RuntimeVersion)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// 事故主用例:用户反复 sudo bx up,每次都撞在「启动恢复未完成」或「Guardian
// 正忙」上。这两条短路从不调用 needsAttention,按「宁可无码也不给错码」的
// 规则会被省略 code —— 最需要指引的场景反而什么都没有。它们各自有属于本次
// 失败的真实码,由错误本身识别,与 LastError 的新鲜度无关。
func TestMutationHandlerNamesRecoveryIncompleteAndBusyFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"recoveryBlocked 短路", errRecoveryIncomplete, "recovery_incomplete"},
		{"acquireMutation 排队超时", fmt.Errorf("%w: %w", errMutationBusy, context.DeadlineExceeded), "guardian_busy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := swapGuardianLogOutput(&buf)
			defer restore()

			// LastError 停留在一条更早的、不相关的失败上,且这次调用不会更新它。
			controller := &fakeController{status: Status{LastError: "stale_unrelated_code"}}
			handler := mutationHandler(controller, func(context.Context) error { return tt.err }, newAcceptedMutations(), LocalAPIOptions{}, "/v1/up")

			rec := httptest.NewRecorder()
			handler(rec, rootMutationRequest(t))

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("状态码 = %d, want 500", rec.Code)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != tt.wantCode {
				t.Fatalf("code = %q, want %q(body = %v)", body["code"], tt.wantCode, body)
			}
			// 陈旧码绝不能借这条路径回来。
			if strings.Contains(rec.Body.String(), "stale_unrelated_code") {
				t.Errorf("响应不得夹带陈旧码,实际 = %s", rec.Body.String())
			}
			// 响应仍只带码,原始错误串只进 Guardian 日志。
			if !strings.Contains(buf.String(), tt.err.Error()) {
				t.Errorf("Guardian 日志必须记录完整错误,实际 = %q", buf.String())
			}
		})
	}
}

// 菜单栏以普通用户身份连 Guardian socket 开关保护,不再弹管理员密码框。
// 该判据早已存在并守着 /v1/recoveries(路径恢复会重装路由,同样是改网络的操作),
// 此处把它推广到日常开关。装卸与迁移不在其列,见下一条测试。
func TestLocalAPIUpDownAcceptsConfiguredOwner(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		uid      uint32
		gotUID   bool
		wantCode int
		wantUp   int
		wantDown int
	}{
		{name: "owner up", path: "/v1/up", uid: 501, gotUID: true, wantCode: http.StatusOK, wantUp: 1},
		{name: "owner down", path: "/v1/down", uid: 501, gotUID: true, wantCode: http.StatusOK, wantDown: 1},
		{name: "root up", path: "/v1/up", uid: 0, gotUID: true, wantCode: http.StatusOK, wantUp: 1},
		{name: "other user up", path: "/v1/up", uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "no credentials", path: "/v1/up", wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected}}
			request := httptest.NewRequest(http.MethodPost, tt.path, nil)
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.upCalls != tt.wantUp {
				t.Fatalf("Up calls = %d, want %d", controller.upCalls, tt.wantUp)
			}
			if controller.downCalls != tt.wantDown {
				t.Fatalf("Down calls = %d, want %d", controller.downCalls, tt.wantDown)
			}
		})
	}
}

// 回归守卫:装卸与版本迁移**不**随开关一起放开。
// 没有这条,将来一次「顺手把判据统一一下」就会把 update/migrate 也交给普通用户,
// 而那两个动作会改磁盘上的二进制与服务定义,后果与开关完全不同量级。
func TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured(t *testing.T) {
	for _, path := range []string{"/v1/update", "/v1/migrate"} {
		t.Run(path, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1}}
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("%s status = %d, want 403 (owner 不得装卸/迁移), body=%s",
					path, recorder.Code, recorder.Body.String())
			}
		})
	}
}

// captureGuardianLog 把 log 的输出接到 buffer 上,返回读取器与还原函数。
func captureGuardianLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	output := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0) // 故意抹掉行首时间前缀:审计行必须自带 at=,不能靠前缀。
	t.Cleanup(func() {
		log.SetOutput(output)
		log.SetFlags(flags)
	})
	return &buffer
}

// owner_uid 授权的代价是「以该 uid 运行的任何进程都能悄无声息地开关 bx」,
// 设计文档接受这个风险的唯一条件是:Guardian 日志记下每一次 up/down 的发起
// uid 与时间。这条测试就是那句话的执行副本——写在 spec 里而代码里没有的缓解
// 措施比从未声称的更糟,因为它会被相信。
func TestLocalAPIMutationLogsInitiatorUIDAndTime(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		uid         uint32
		failure     error
		wantOutcome string
	}{
		{name: "owner up", path: "/v1/up", uid: 501, wantOutcome: "outcome=ok"},
		{name: "owner down", path: "/v1/down", uid: 501, wantOutcome: "outcome=ok"},
		{name: "root down", path: "/v1/down", uid: 0, wantOutcome: "outcome=ok"},
		// 失败也必须留下发起者:事故排查里「谁按的」和「为什么失败」一样重要。
		{name: "failed up", path: "/v1/up", uid: 501, failure: errors.New("boom"), wantOutcome: "outcome=failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureGuardianLog(t)
			controller := &fakeController{status: Status{SchemaVersion: 1}, upErr: tt.failure, downErr: tt.failure}
			request := httptest.NewRequest(http.MethodPost, tt.path, nil)
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, true))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)

			text := logs.String()
			wantUID := fmt.Sprintf("uid=%d", tt.uid)
			wantEndpoint := "endpoint=" + tt.path
			if !strings.Contains(text, "guardian_mutation_requested") {
				t.Fatalf("发起未被记录,日志 = %q", text)
			}
			for _, want := range []string{wantUID, wantEndpoint, "at=", tt.wantOutcome} {
				if !strings.Contains(text, want) {
					t.Fatalf("日志缺 %q(uid/端点/时间/成败缺一不可),日志 = %q", want, text)
				}
			}
			// 时间必须可解析,而不是随便一个字符串。
			for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
				index := strings.Index(line, "at=")
				if index < 0 {
					continue
				}
				stamp := strings.TrimSpace(line[index+len("at="):])
				if _, err := time.Parse(time.RFC3339, stamp); err != nil {
					t.Fatalf("at= 不是可解析的时间戳 %q: %v", stamp, err)
				}
			}
			// 审计行只带 uid/端点/时间/成败,绝不外传原始错误串——
			// 那条纪律由 guardian_mutation_failed 一行独占,响应体更是只带码。
			for _, line := range strings.Split(text, "\n") {
				if !strings.HasPrefix(line, "guardian_mutation_requested") && !strings.HasPrefix(line, "guardian_mutation_result") {
					continue
				}
				if strings.Contains(line, "boom") {
					t.Fatalf("审计行不得夹带原始错误:%q", line)
				}
			}
		})
	}
}

// 未授权的请求不该被记成一次「发起」:403 连 mutation 都没进,把它记下来
// 只会让审计日志被扫端口的噪声淹没,而真正的开关记录淹没在里面。
func TestLocalAPIRejectedMutationIsNotLoggedAsInitiated(t *testing.T) {
	logs := captureGuardianLog(t)
	controller := &fakeController{status: Status{SchemaVersion: 1}}
	request := httptest.NewRequest(http.MethodPost, "/v1/up", nil)
	request = request.WithContext(withPeerCredentials(request.Context(), 502, true))
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if strings.Contains(logs.String(), "guardian_mutation_requested") {
		t.Fatalf("403 不该被记成一次发起,日志 = %q", logs.String())
	}
}

// Core 不可达时 /v1/status 必须成功返回并如实说不可达 —— 菜单是它唯一的
// 数据源,让这个端点失败等于让菜单瞎掉。且不得用 tunnel_healthy=false 表示「没
// 问到」。
func TestStatusReportsCoreUnreachableWithoutFailing(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Protection: ProtectionProtected}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{
		CoreRuntime: func(context.Context) (CoreRuntime, error) {
			return CoreRuntime{}, errors.New("dial core: connection refused")
		},
	}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("Core 不可达不得让 status 失败,实际 %d", recorder.Code)
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Core == nil {
		t.Fatal("Core 字段必须在场并说明不可达")
	}
	if got.Core.Reachable {
		t.Fatal("Core 拨不通时 Reachable 必须为 false")
	}
	if got.Core.TunnelHealthy {
		t.Fatal("不得用 tunnel_healthy 表达「没问到」")
	}
}

func TestStatusCarriesCoreRuntimeWhenReachable(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Protection: ProtectionProtected}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{
		CoreRuntime: func(context.Context) (CoreRuntime, error) {
			return CoreRuntime{
				Reachable: true, TunnelHealthy: true, LatencyMS: 390,
				Server: "vps", Transport: "reality@vps", UDPMode: "proxy",
			}, nil
		},
	}).ServeHTTP(recorder, request)

	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Core == nil || !got.Core.Reachable || got.Core.LatencyMS != 390 || got.Core.Transport != "reality@vps" {
		t.Fatalf("Core 运行时字段 = %+v", got.Core)
	}
}

// 没有注入取数函数时(既有调用方全都如此)不得 panic,也不得凭空造 Core 字段。
func TestStatusWithoutCoreRuntimeProviderOmitsIt(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Core != nil {
		t.Fatalf("未注入取数函数时不该有 Core 字段,实际 %+v", got.Core)
	}
}

// CoreRuntime 的三个「必有」键必须真的出现在线上的 JSON 里。
//
// 菜单侧(apps/macos/BxMenu)把 reachable/tunnel_healthy/latency_ms 全按可选解码,
// 依据是 Go 这边没给它们加 omitempty —— 但那个依据**本身没有测试守着**。上面几条
// Task 1 的测试都是 Unmarshal 回同一个 Status 结构体,加了 omitempty 之后它们照样
// 全绿:零值本来就是零值,谁也看不出键少了。而菜单那边少一个键就是整份状态解不动
// (在它把可选化补上之前),即 2026-08-06「bx down 后菜单变黄、连 Start Protection
// 都没有」那个失明 bug 换个层级重演。
//
// 所以这条断言看的是**序列化出来的键本身**,不是反序列化回来的值:三个键在 Core
// 全零值时也必须在场。要给它们加 omitempty 的人,请先改菜单侧的语义,再改这里。
func TestCoreRuntimeAlwaysMarshalsItsThreeAlwaysPresentKeys(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{
		// 全零值的 Core:omitempty 若被加上,恰恰是这种响应会丢掉键。
		CoreRuntime: func(context.Context) (CoreRuntime, error) { return CoreRuntime{}, nil },
	}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var envelope struct {
		Core map[string]json.RawMessage `json:"core"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Core == nil {
		t.Fatal("注入了取数函数,core 对象必须在场")
	}
	for _, key := range []string{"reachable", "tunnel_healthy", "latency_ms"} {
		if _, ok := envelope.Core[key]; !ok {
			t.Errorf("core.%s 必须恒在场(不得加 omitempty):菜单据此区分「没问过」与「问了没答」,"+
				"缺键会让它退回「不知道」而不是一个可信的答案。真要改,先改菜单侧的语义。", key)
		}
	}
}
