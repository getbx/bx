//go:build darwin

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

func useTempSwitchHandoff(t *testing.T) {
	t.Helper()
	old := switchHandoffPath
	switchHandoffPath = filepath.Join(t.TempDir(), "switch-handoff.json")
	t.Cleanup(func() { switchHandoffPath = old })
}

var testHandoff = guardian.MigrationRequest{Gateway: "192.0.2.1", ServerBypass: []string{"203.0.113.92/32"}}

func TestSwitchHandoffRoundTripsAndRefusesAGarbledRecord(t *testing.T) {
	useTempSwitchHandoff(t)
	if _, ok, err := loadSwitchHandoff(); ok || err != nil {
		t.Fatalf("no record = (%v, %v), want (false, nil)", ok, err)
	}
	if err := saveSwitchHandoff(testHandoff); err != nil {
		t.Fatal(err)
	}
	got, ok, err := loadSwitchHandoff()
	if err != nil || !ok || !reflect.DeepEqual(got, testHandoff) {
		t.Fatalf("load = (%+v, %v, %v), want the saved request", got, ok, err)
	}
	// 读得到而读不懂:如实报错,绝不当成「没有」(那会让 bx up 在屏障后盲起 Core)。
	if err := os.WriteFile(switchHandoffPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSwitchHandoff(); err == nil {
		t.Fatal("a garbled record was read as no record")
	}
	if err := clearSwitchHandoff(); err != nil {
		t.Fatal(err)
	}
	if err := clearSwitchHandoff(); err != nil {
		t.Fatalf("clearing twice must be harmless: %v", err)
	}
}

// 2026-09-25 真机:切换停在屏障后面,用户敲了 `sudo bx up`,那条路不知道有个切换没
// 做完,新 Guardian 在一道它不拥有的屏障后面起 Core,连不上服务器。现在 bx up 用记下
// 的那份请求走 migrate 把它做完,成功才销记录。
func TestUpFinishesAnUnfinishedSwitchThroughMigrate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		migrateErr error
		wantClear  bool
	}{
		{"migrate succeeds", nil, true},
		{"migrate fails", errors.New("boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			client := &recordingGuardianClient{events: &events, migrateErr: tc.migrateErr, migrateStatus: guardian.Status{Protection: guardian.ProtectionProtected}}
			deps := testMacOSLifecycleDeps(&events, client)
			cleared := false
			deps.pendingSwitch = func() (guardian.MigrationRequest, bool, error) { return testHandoff, true, nil }
			deps.clearPendingSwitch = func() error { cleared = true; return nil }

			_, _, err := ensureGuardianOwnership(context.Background(), "/etc/bx/config.yaml", deps)
			if (err == nil) != (tc.migrateErr == nil) {
				t.Fatalf("err = %v", err)
			}
			if client.upCalls != 0 || client.migrateCalls != 1 {
				t.Fatalf("an unfinished switch must be finished by migrate, not up: up=%d migrate=%d", client.upCalls, client.migrateCalls)
			}
			if !reflect.DeepEqual(client.lastMigrate, testHandoff) {
				t.Fatalf("migrate got %+v, want the recorded request", client.lastMigrate)
			}
			if cleared != tc.wantClear {
				t.Fatalf("record cleared = %v, want %v", cleared, tc.wantClear)
			}
		})
	}
}

// 读不出记录:不猜,bx up 停在这里。
func TestUpRefusesWhenTheSwitchRecordIsUnreadable(t *testing.T) {
	var events []string
	client := &recordingGuardianClient{events: &events}
	deps := testMacOSLifecycleDeps(&events, client)
	deps.pendingSwitch = func() (guardian.MigrationRequest, bool, error) {
		return guardian.MigrationRequest{}, false, errors.New("garbled")
	}
	if _, _, err := ensureGuardianOwnership(context.Background(), "/etc/bx/config.yaml", deps); err == nil {
		t.Fatal("an unreadable switch record was ignored")
	}
	if client.upCalls != 0 || client.migrateCalls != 0 {
		t.Fatalf("mutated despite an unreadable record: up=%d migrate=%d", client.upCalls, client.migrateCalls)
	}
}

// 用户 `bx down`:切换留下的屏障 Guardian 不拥有,干净的 Down 不会拆 —— 那就是
// 「关了保护还断着网」。拆掉并销记录;升级自己的停机不许这么做。
func TestUserDownRemovesTheBarrierOfAnUnfinishedSwitch(t *testing.T) {
	for _, tc := range []struct {
		purpose   downPurpose
		wantClear bool
	}{{downPurposeUser, true}, {downPurposeUpgradeUnprotected, false}} {
		deps := newFakeMacOSLifecycleDeps()
		barrierCleared, recordCleared := false, false
		deps.pendingSwitch = func() (guardian.MigrationRequest, bool, error) { return testHandoff, true, nil }
		deps.clearPendingSwitch = func() error { recordCleared = true; return nil }
		deps.clearBarrierRoutes = func(context.Context) error { barrierCleared = true; return nil }
		if _, err := macOSDownLifecycleFor(context.Background(), tc.purpose, "/etc/bx/config.yaml", deps.macOSLifecycleDeps); err != nil {
			t.Fatalf("purpose %v: %v", tc.purpose, err)
		}
		if barrierCleared != tc.wantClear || recordCleared != tc.wantClear {
			t.Fatalf("purpose %v: barrier cleared=%v record cleared=%v, want %v", tc.purpose, barrierCleared, recordCleared, tc.wantClear)
		}
	}
}
