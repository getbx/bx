//go:build linux

package procredact

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestParseRange(t *testing.T) {
	got, err := parseRange("7fff0000-7fff1000")
	if err != nil {
		t.Fatal(err)
	}
	if got.start != 0x7fff0000 || got.end != 0x7fff1000 {
		t.Fatalf("range = %+v", got)
	}
}

func TestParseRangeRejectsBadInput(t *testing.T) {
	if _, err := parseRange("not-a-range"); err == nil {
		t.Fatal("bad range should fail")
	}
}

func TestRedactArgIntegration(t *testing.T) {
	if os.Getenv("BX_REDACT_HELPER") == "1" {
		time.Sleep(5 * time.Second)
		return
	}
	secret := "bx-redact-secret-for-test"
	cmd := exec.Command(os.Args[0], "-test.run=TestRedactArgIntegration", "--", secret)
	cmd.Env = append(os.Environ(), "BX_REDACT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	if err := RedactArg(cmd.Process.Pid, secret); err != nil {
		t.Fatal(err)
	}
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cmdline")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cmdline, []byte(secret)) {
		t.Fatalf("secret still present in cmdline: %q", cmdline)
	}
}
