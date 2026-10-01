package deploy

import (
	"errors"
	"strings"
	"testing"
)

// fakeServer answers like a real one: the probe, the survey, and the install / link commands.
type fakeServer struct {
	survey  string
	linkErr bool
	calls   []string
}

const printed = "  sudo bx setup --udp 'bx://UDP' 'bx://MAIN'"

func (f *fakeServer) Run(cmd string, _ *string, _ bool) (string, error) {
	f.calls = append(f.calls, cmd)
	switch {
	case strings.Contains(cmd, "id -u"):
		return "0\nx86_64\n", nil
	case strings.Contains(cmd, "/etc/bx/server.yaml"):
		return f.survey, nil
	case strings.Contains(cmd, "server link"):
		if f.linkErr {
			return "open /etc/bx/server.yaml: permission denied", errors.New("exit status 1")
		}
		return printed, nil
	case strings.Contains(cmd, "server install"):
		return printed, nil
	}
	return "", nil
}

func (f *fakeServer) ran(part string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}

func hooks() Hooks {
	return Hooks{FetchBinary: func(string, func(string) (string, error)) error { return nil }}
}

// A bx server that already works is kept: its keys, users and every link already handed out
// (reinstalling regenerates the keys and silently breaks all of them).
func TestAWorkingServerIsReusedKeepingItsKeys(t *testing.T) {
	f := &fakeServer{survey: "existing=reality\ntcp=sing-box\nudp=sing-box\n"}
	res, err := Run(Options{Target: "root@h", Address: "h"}, f, hooks())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reused || res.Main != "bx://MAIN" || res.UDP != "bx://UDP" {
		t.Fatalf("result %+v", res)
	}
	if f.ran("server install") {
		t.Fatal("reinstalled a working server — every link already handed out would stop working")
	}
	if !f.ran("server link --host 'h'") {
		t.Fatalf("did not read the existing link: %v", f.calls)
	}
}

// Someone's website or another proxy on the port: say who, and touch nothing.
func TestAPortTakenBySomethingElseStopsBeforeChangingAnything(t *testing.T) {
	f := &fakeServer{survey: "existing=\ntcp=nginx\nudp=\n"}
	fetched := false
	h := Hooks{FetchBinary: func(string, func(string) (string, error)) error { fetched = true; return nil }}
	_, err := Run(Options{Target: "root@h"}, f, h)
	if err == nil || !strings.Contains(err.Error(), "nginx") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if fetched || f.ran("mv ") || f.ran("server install") {
		t.Fatalf("changed the server although the port was taken: %v", f.calls)
	}
}

func TestReinstallIsOnlyOnRequest(t *testing.T) {
	f := &fakeServer{survey: "existing=reality\ntcp=sing-box\n"}
	res, err := Run(Options{Target: "root@h", Force: true}, f, hooks())
	if err != nil || res.Reused || !f.ran("server install") || !f.ran("--force") {
		t.Fatalf("forced reinstall: res=%+v err=%v calls=%v", res, err, f.calls)
	}
}

func TestAnExistingInstallThatCannotBeReadPointsAtReinstalling(t *testing.T) {
	f := &fakeServer{survey: "existing=reality\n", linkErr: true}
	_, err := Run(Options{Target: "root@h"}, f, hooks())
	if err == nil || !strings.Contains(err.Error(), "already has bx") || !strings.Contains(err.Error(), "reinstall") {
		t.Fatalf("err = %v", err)
	}
}

func TestSurveyParsing(t *testing.T) {
	s := ParseSurvey("existing=hysteria2\ntcp=nginx,caddy\nudp=caddy,sing-box\n")
	if s.Existing != "hysteria2" || strings.Join(s.PortHolders, ",") != "nginx,caddy,sing-box" {
		t.Fatalf("%+v", s)
	}
	if s := ParseSurvey(""); s.Existing != "" || len(s.PortHolders) != 0 {
		t.Fatalf("empty output must mean nothing found: %+v", s)
	}
}
