package policy

import (
	"strings"
	"testing"
)

func TestApplyMakesModesExclusive(t *testing.T) {
	in := []byte("server: brook://x\nrules:\n  - direct: [example.com]\n")
	out, changed, err := Apply(in, Request{Mode: "proxy", Add: []string{"example.com"}})
	if err != nil || !changed {
		t.Fatalf("Apply changed=%v err=%v", changed, err)
	}
	text := string(out)
	if !strings.Contains(text, "proxy:") || strings.Contains(text, "direct:") {
		t.Fatalf("modes not exclusive:\n%s", text)
	}
}

func TestApplyRejectsRiskyDirect(t *testing.T) {
	_, _, err := Apply([]byte("server: brook://x\n"), Request{Mode: "direct", Add: []string{"evil.workers.dev"}})
	if err == nil || !strings.Contains(err.Error(), "allow_risk") {
		t.Fatalf("err=%v", err)
	}
}

func TestApplyRemoveDoesNotLeaveEmptyPolicy(t *testing.T) {
	in := []byte("server: brook://x\nrules:\n  - direct: [example.com]\n")
	out, changed, err := Apply(in, Request{Mode: "direct", Remove: []string{"example.com"}})
	if err != nil || !changed {
		t.Fatalf("Apply changed=%v err=%v", changed, err)
	}
	if strings.Contains(string(out), "direct:") || strings.Contains(string(out), "- {}") {
		t.Fatalf("remove left empty policy:\n%s", out)
	}
}
