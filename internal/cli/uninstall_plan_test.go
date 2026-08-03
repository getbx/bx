package cli

import (
	"reflect"
	"slices"
	"testing"
)

func TestBuildDarwinUninstallPlanUnified(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", true)
	wantCommands := [][]string{
		{"launchctl", "bootout", "system/com.getbx.bx.guard"},
		{"launchctl", "bootout", "gui/501/com.getbx.bx.menu"},
	}
	if !reflect.DeepEqual(plan.LaunchctlCommands, wantCommands) {
		t.Fatalf("commands = %v", plan.LaunchctlCommands)
	}
	for _, want := range []string{
		"/Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"/usr/local/bin/bx",
		"/Library/Application Support/bx/runtime",
		"/Applications/Bx.app",
		"/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist",
		"/Users/alice/Library/LaunchAgents/com.ggshr9.bx.menu.plist",
	} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
	for _, keep := range []string{"/etc/bx", "/var/lib/bx"} {
		if slices.Contains(plan.RemovePaths, keep) {
			t.Fatalf("must keep %s", keep)
		}
		if !slices.Contains(plan.KeepPaths, keep) {
			t.Fatalf("KeepPaths missing %s", keep)
		}
	}
}

func TestBuildDarwinUninstallPlanLegacySkipsRuntime(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", false)
	if slices.Contains(plan.RemovePaths, "/Library/Application Support/bx/runtime") {
		t.Fatal("legacy layout must not remove runtime root")
	}
	if slices.Contains(plan.RemovePaths, "/Applications/Bx.app") {
		t.Fatal("legacy layout must not remove /Applications/Bx.app")
	}
	for _, want := range []string{
		"/Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"/usr/local/bin/bx",
		"/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist",
		"/Users/alice/Library/LaunchAgents/com.ggshr9.bx.menu.plist",
	} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
}

func TestBuildDarwinUninstallPlanNoConsoleUserSkipsUserScope(t *testing.T) {
	plan := buildDarwinUninstallPlan(0, "", true)
	if len(plan.LaunchctlCommands) != 1 {
		t.Fatalf("expected only guard bootout, got %v", plan.LaunchctlCommands)
	}
	if plan.LaunchctlCommands[0][2] != "system/com.getbx.bx.guard" {
		t.Fatalf("unexpected command: %v", plan.LaunchctlCommands[0])
	}
	for _, path := range plan.RemovePaths {
		if slices.Contains([]string{
			"Library/LaunchAgents/com.getbx.bx.menu.plist",
			"Library/LaunchAgents/com.ggshr9.bx.menu.plist",
		}, path) {
			t.Fatalf("must not remove user-scoped path without console user: %v", plan.RemovePaths)
		}
	}
	// runtime/app 仍应清理:统一布局与「是否有控制台用户」无关。
	for _, want := range []string{"/Library/Application Support/bx/runtime", "/Applications/Bx.app"} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
}
