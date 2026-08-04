package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

// TestSwiftMenuGuardianSocketPathMatchesGoConstant is a drift guard: the
// macOS menu bar app (apps/macos/BxMenu) hardcodes the Guardian control
// socket path as a Swift string literal — it cannot import the Go
// guardian.SocketPath constant across the language boundary. If the Go
// side ever moves the socket (as it once did, from /run/bx to
// /var/run/bx — see CLAUDE.md), a silently-stale Swift literal would make
// the menu bar app fail to talk to Guardian with no compiler or CI signal.
// This test greps the Swift source for the exact Go constant value so any
// future rename trips CI instead of being caught by a user's crash report.
func TestSwiftMenuGuardianSocketPathMatchesGoConstant(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path via runtime.Caller")
	}
	// internal/cli/<this file> -> repo root is two levels up.
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	swiftPath := filepath.Join(repoRoot, "apps", "macos", "BxMenu", "Sources", "BxMenu", "GuardianClient.swift")

	data, err := os.ReadFile(swiftPath)
	if err != nil {
		t.Fatalf("read %s: %v", swiftPath, err)
	}

	want := strconv.Quote(guardian.SocketPath)
	if !strings.Contains(string(data), want) {
		t.Fatalf(
			"GuardianClient.swift does not contain the string literal %s (guardian.SocketPath = %q); "+
				"update the Swift socket path constant to match",
			want, guardian.SocketPath,
		)
	}
}
