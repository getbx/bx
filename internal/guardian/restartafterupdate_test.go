package guardian

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// D2:升级提交之后,只有「退出时 Core 不会跟着死」且「盘上装的版本与正在跑的不同」
// 时 Guardian 才退出让 launchd 以新二进制重启。少了第一条,退出就是让 launchd 收掉
// Core —— 那几秒流量直连、泄漏真实 IP。
func TestRestartAfterCommittedUpdateOnlyWhenCoreOutlivesGuardian(t *testing.T) {
	for _, tc := range []struct {
		outlives           bool
		running, installed string
		want               bool
	}{
		{true, "v0.4.5", "v0.4.6", true},
		{false, "v0.4.5", "v0.4.6", false}, // 旧 plist:退出会杀掉 Core
		{true, "v0.4.6", "v0.4.6", false},  // 没有东西要换
		{true, "v0.4.5", "", false},        // 读不出来就不动
		{true, "v0.4.5", "  ", false},
	} {
		if got := restartAfterCommittedUpdate(tc.outlives, tc.running, tc.installed); got != tc.want {
			t.Errorf("restartAfterCommittedUpdate(%v, %q, %q) = %v, want %v", tc.outlives, tc.running, tc.installed, got, tc.want)
		}
	}
}

// 接线:/v1/update 成功之后才触发,失败绝不触发;触发之前应答已经写出。
func TestUpdateHandlerRestartsGuardianOnlyAfterACommittedUpdate(t *testing.T) {
	body := []byte(`{"transaction_id":"tx-1","from_version":"v1","to_version":"v2","asset_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","package_path":"/var/lib/bx/update/staging/tx-1/package.tar.gz"}`)
	for _, tc := range []struct {
		name       string
		outlives   bool
		updateErr  error
		wantCalled bool
	}{
		{"committed, core outlives", true, nil, true},
		{"committed, old plist", false, nil, false},
		{"failed update", true, errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restarted := make(chan struct{}, 1)
			controller := &fakeController{updateErr: tc.updateErr, updateResult: UpdateResult{FromVersion: "v1", ToVersion: "v2", Phase: PhaseCommitted}}
			handler := NewLocalAPI(controller, LocalAPIOptions{
				GuardianVersion:      "v1",
				RuntimeVersion:       func() string { return "v2" },
				CoreOutlivesGuardian: tc.outlives,
				RestartAfterUpdate:   func() { restarted <- struct{}{} },
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/update", bytes.NewReader(body))
			request = request.WithContext(withPeerCredentials(request.Context(), 0, true))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			select {
			case <-restarted:
				if !tc.wantCalled {
					t.Fatal("Guardian restarted after an update it must not restart for")
				}
				if recorder.Code != http.StatusOK {
					t.Fatalf("restarted without having answered 200 (status %d)", recorder.Code)
				}
			case <-time.After(200 * time.Millisecond):
				if tc.wantCalled {
					t.Fatal("committed update with Core outliving Guardian did not restart Guardian")
				}
			}
		})
	}
}

// 能力只在真的成立时声明:客户端据它选「提交后自己重启」还是「在屏障下切换」。
func TestCoreOutlivesGuardianCapabilityIsDeclaredOnlyWhenTrue(t *testing.T) {
	var on, off Status
	applyVersionFields(&on, LocalAPIOptions{CoreOutlivesGuardian: true})
	applyVersionFields(&off, LocalAPIOptions{})
	if !slices.Contains(on.Capabilities, CapabilityCoreOutlivesGuardian) {
		t.Fatalf("capabilities %v miss %q", on.Capabilities, CapabilityCoreOutlivesGuardian)
	}
	if slices.Contains(off.Capabilities, CapabilityCoreOutlivesGuardian) {
		t.Fatalf("capability %q declared by a Guardian whose Core dies with it", CapabilityCoreOutlivesGuardian)
	}
	if slices.Contains(GuardianCapabilities(), CapabilityCoreOutlivesGuardian) {
		t.Fatal("the capability must not sit in the static list: it is a fact about the loaded job")
	}
}

// 组装根:DaemonOptions 里 RunDaemon 填的两样必须真的到达 LocalAPIOptions。
func TestLocalAPIOptionsCarryTheRestartWiring(t *testing.T) {
	called := false
	got := localAPIOptionsFor(DaemonOptions{coreOutlivesGuardian: true, restartForUpdate: func() { called = true }})
	if !got.CoreOutlivesGuardian {
		t.Fatal("CoreOutlivesGuardian did not reach the local API")
	}
	if got.RestartAfterUpdate == nil {
		t.Fatal("RestartAfterUpdate did not reach the local API")
	}
	got.RestartAfterUpdate()
	if !called {
		t.Fatal("RestartAfterUpdate is not the daemon's restart function")
	}
}
