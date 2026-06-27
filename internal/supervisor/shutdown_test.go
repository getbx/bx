package supervisor

import (
	"testing"
	"time"
)

func TestArmShutdownWatchdogFires(t *testing.T) {
	fired := make(chan struct{})
	armShutdownWatchdog(5*time.Millisecond, func() { close(fired) })
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog 应在 grace 后触发 onTimeout")
	}
}

func TestArmShutdownWatchdogStopCancels(t *testing.T) {
	fired := make(chan struct{})
	tm := armShutdownWatchdog(50*time.Millisecond, func() { close(fired) })
	if !tm.Stop() {
		t.Fatal("Stop 应在触发前成功取消")
	}
	select {
	case <-fired:
		t.Fatal("Stop 后不应触发 onTimeout")
	case <-time.After(120 * time.Millisecond):
	}
}
