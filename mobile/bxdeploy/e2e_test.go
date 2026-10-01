package bxdeploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type collect struct{ lines []string }

func (c *collect) Event(line string) { c.lines = append(c.lines, line) }

func (c *collect) last(t *testing.T) event {
	t.Helper()
	var e event
	if len(c.lines) == 0 {
		t.Fatal("no events")
	}
	if err := json.Unmarshal([]byte(c.lines[len(c.lines)-1]), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// End to end against a real sshd (a throwaway container: Ubuntu + sshd + root password + a
// systemctl stub). Skipped unless BX_E2E_SSH=host:port and BX_E2E_PASSWORD are set. It never talks
// to a Guardian, so it is safe to run on the owner's Mac.
func TestDeployAgainstARealSSHServer(t *testing.T) {
	target, password := os.Getenv("BX_E2E_SSH"), os.Getenv("BX_E2E_PASSWORD")
	if target == "" || password == "" {
		t.Skip("set BX_E2E_SSH=host:port and BX_E2E_PASSWORD to run against a test sshd")
	}
	host, portText, _ := strings.Cut(target, ":")
	port, _ := strconv.Atoi(portText)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	run := func(pw string, reinstall, forget bool) event {
		c := &collect{}
		Deploy(host, port, "root", pw, reinstall, forget, kh, c)
		return c.last(t)
	}
	if e := run("wrong-password", false, false); e.Event != "error" || e.Code != "auth_failed" {
		t.Fatalf("wrong password: %+v", e)
	}
	first := run(password, false, false)
	if first.Event != "done" || first.Reused || !strings.HasPrefix(first.Link, "bx://") {
		t.Fatalf("first deploy: %+v", first)
	}
	again := run(password, false, false)
	if again.Event != "done" || !again.Reused || again.Link != first.Link {
		t.Fatalf("second deploy must reuse with the same link: reused=%v same=%v", again.Reused, again.Link == first.Link)
	}
	if hook := os.Getenv("BX_E2E_ROTATE_HOSTKEY"); hook != "" {
		if out, err := runHook(hook); err != nil {
			t.Fatalf("rotating the host key: %v %s", err, out)
		}
		if e := run(password, false, false); e.Event != "error" || e.Code != "host_key_changed" {
			t.Fatalf("changed fingerprint: %+v", e)
		}
		if e := run(password, false, true); e.Event != "done" || !e.Reused {
			t.Fatalf("after forgetting: %+v", e)
		}
	}
}

// Against a server whose port 443 is held by another program (BX_E2E_BUSY_SSH=host:port).
func TestDeployRefusesATakenPort(t *testing.T) {
	target, password := os.Getenv("BX_E2E_BUSY_SSH"), os.Getenv("BX_E2E_PASSWORD")
	if target == "" || password == "" {
		t.Skip("set BX_E2E_BUSY_SSH=host:port and BX_E2E_PASSWORD")
	}
	host, portText, _ := strings.Cut(target, ":")
	port, _ := strconv.Atoi(portText)
	c := &collect{}
	Deploy(host, port, "root", password, false, false, filepath.Join(t.TempDir(), "kh"), c)
	if e := c.last(t); e.Event != "error" || e.Code != "port_in_use" {
		t.Fatalf("taken port: %+v", e)
	}
}
