package cli

import (
	"strings"
	"testing"
)

func TestDarwinUpdateGuardMessage(t *testing.T) {
	err := unifiedUpdateGuard(true, false)
	if err == nil || !strings.Contains(err.Error(), "Bx.app") {
		t.Fatalf("want guidance error, got %v", err)
	}
	if err := unifiedUpdateGuard(true, true); err != nil {
		t.Fatalf("--check must pass: %v", err)
	}
	if err := unifiedUpdateGuard(false, false); err != nil {
		t.Fatalf("legacy layout must pass: %v", err)
	}
}
