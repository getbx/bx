package cli

import "testing"

func TestBuildExecStart(t *testing.T) {
	got := buildExecStart("/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx up -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("ExecStart 应收敛为仅 -c, got %q", got)
	}
}
