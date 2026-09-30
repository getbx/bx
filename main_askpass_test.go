package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/sshpass"
)

// The askpass half only works if the real binary's main() hands off to it before cli parsing —
// ssh runs `bx "<prompt>"`, which the CLI would reject as an unknown command. A unit test of the
// sshpass package alone would stay green with the hook deleted (guard antipattern ⑥), so this
// builds the binary and runs it exactly the way ssh does.
func TestTheBinaryAnswersSSHAskpassFromMain(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bx")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	s, err := sshpass.Serve("s3cret-pw", bin)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cmd := exec.Command(bin, "root@203.0.113.9's password: ")
	cmd.Env = append(os.Environ(), s.Env()...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("askpass run failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "s3cret-pw" {
		t.Fatalf("askpass printed %q", out)
	}
}
