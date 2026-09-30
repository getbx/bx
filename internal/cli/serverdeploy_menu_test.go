package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/guardian"
)

func hasOpt(args []string, want string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && args[i+1] == want {
			return true
		}
	}
	return false
}

func TestDeploySSHOptionsForTheMenu(t *testing.T) {
	withPassword := deploySSHOptions(sshOptionParams{Port: 2222, ControlDir: "/tmp/cm", KnownHosts: "/k", Password: true, Menu: true})
	for _, want := range []string{
		"ControlMaster=auto", `ControlPath="/tmp/cm/c"`, // one login for every step: the password is asked once
		"Port=2222", "StrictHostKeyChecking=accept-new", `UserKnownHostsFile="/k"`,
		"PubkeyAuthentication=no",   // someone typing a password means it; agent keys first can exhaust MaxAuthTries
		"NumberOfPasswordPrompts=1", // a wrong password is not retried
	} {
		if !hasOpt(withPassword, want) {
			t.Errorf("menu+password options lack %s: %v", want, withPassword)
		}
	}
	if hasOpt(withPassword, "BatchMode=yes") {
		t.Error("BatchMode would also switch off the askpass that supplies the password")
	}
	keyOnly := deploySSHOptions(sshOptionParams{ControlDir: "/tmp/cm", KnownHosts: "/k", Menu: true})
	if !hasOpt(keyOnly, "BatchMode=yes") {
		t.Error("menu without a password must never sit waiting for a prompt nobody can answer")
	}
	if hasOpt(keyOnly, "Port=22") || hasOpt(keyOnly, "Port=0") {
		t.Error("no port given: leave ssh's own default (and ssh_config) alone")
	}
	terminal := deploySSHOptions(sshOptionParams{ControlDir: "/tmp/cm"})
	if hasOpt(terminal, "BatchMode=yes") || hasOpt(terminal, `UserKnownHostsFile="/k"`) {
		t.Error("the terminal path keeps the user's own ssh behaviour")
	}
}

// Every ssh and scp the deploy makes must ride the shared connection — one call without the options
// asks for the password again (or dials port 22), while every step before it succeeded.
func TestEveryDeployCallCarriesTheConnectionOptions(t *testing.T) {
	opts := deployOptions{
		Host: "root@h", Protocol: "reality",
		SSHOptions: deploySSHOptions(sshOptionParams{Port: 2222, ControlDir: "/tmp/cm", Menu: true, Password: true}),
	}
	var bare []string
	run := func(name string, args ...string) (string, error) {
		if !hasOpt(args, `ControlPath="/tmp/cm/c"`) || !hasOpt(args, "Port=2222") {
			bare = append(bare, name+" "+strings.Join(args, " "))
		}
		switch joined := strings.Join(args, " "); {
		case strings.Contains(joined, "id -u"):
			return "0\nx86_64\n", nil
		case strings.Contains(joined, "server install"):
			return "sudo bx setup 'bx://MAIN'", nil
		}
		return "", nil
	}
	err := runServerDeploy(opts, deployDeps{
		run:              run,
		remoteFetch:      func(string, func(string) (string, error)) error { return errors.New("no curl") },
		fetchBinary:      func(string) (string, error) { return "/tmp/bx", nil },
		writeLocalConfig: func(string) error { return nil },
		out:              ioDiscard{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) > 0 {
		t.Fatalf("calls without the shared connection options:\n%s", strings.Join(bare, "\n"))
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

// A non-root login from the window: sudo gets the same password on stdin, for every remote command.
func TestNonRootLoginFromTheWindowFeedsSudoThePassword(t *testing.T) {
	var fed, unfed []string
	err := runServerDeploy(deployOptions{Host: "ubuntu@h", Protocol: "reality", Password: "pw"}, deployDeps{
		run: func(name string, args ...string) (string, error) {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "id -u") {
				return "1000\nx86_64\n", nil
			}
			if name == "ssh" {
				unfed = append(unfed, joined)
			}
			return "", nil
		},
		runInput: func(stdin, name string, args ...string) (string, error) {
			joined := strings.Join(args, " ")
			if stdin != "pw\n" || !strings.Contains(joined, "sudo -S -p '' sh -c") {
				t.Errorf("sudo call = %q with stdin %q", joined, stdin)
			}
			fed = append(fed, joined)
			if strings.Contains(joined, "server install") {
				return "sudo bx setup 'bx://MAIN'", nil
			}
			return "", nil
		},
		remoteFetch:      func(_ string, runRemote func(string) (string, error)) error { _, err := runRemote("fetch"); return err },
		fetchBinary:      func(string) (string, error) { return "/tmp/bx", nil },
		writeLocalConfig: func(string) error { return nil },
		out:              ioDiscard{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(unfed) > 0 || len(fed) < 4 {
		t.Fatalf("remote commands without the password: %v (fed %d)", unfed, len(fed))
	}
}

func TestDeployFailuresBecomePlainCategories(t *testing.T) {
	for text, want := range map[string]string{
		"ssh: connect to host 203.0.113.9 port 22: Operation timed out":                       "unreachable",
		"ssh: connect to host 203.0.113.9 port 22: Connection refused":                        "unreachable",
		"ssh: Could not resolve hostname nope.example: nodename nor servname":                 "unreachable",
		"root@203.0.113.9: Permission denied (publickey,password).":                           "auth_failed",
		"@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @\nHost key verification failed.": "host_key_changed",
		"WARNING: Your password has expired.\nPassword change required but no TTY available.": "password_change_required",
		"sudo: a password is required":                                                        "sudo_password",
		"sudo: a terminal is required to read the password":                                   "sudo_password",
		"the remote architecture was not recognized (uname -m says \"mips\")":                 "unsupported_system",
		"the remote verification failed: bx: checksum mismatch":                               "checksum",
		"the remote installation failed: exit status 1":                                       "install_failed",
	} {
		if got := classifyDeployFailure(text); got != want {
			t.Errorf("%q → %q, want %q", text, got, want)
		}
	}
}

type fakeLister struct {
	list      guardian.ServerListResponse
	listErr   error
	added     []string
	replaced  []string
	addErrFor map[string]error
}

func (f *fakeLister) ListServers(context.Context) (guardian.ServerListResponse, error) {
	return f.list, f.listErr
}

func (f *fakeLister) AddServer(_ context.Context, name, link, udp string) error {
	if err := f.addErrFor[name]; err != nil {
		return err
	}
	f.added = append(f.added, name)
	f.list.Servers = append(f.list.Servers, guardian.ServerEntry{Name: name, Host: "203.0.113.9"})
	return nil
}

func (f *fakeLister) ReplaceServer(_ context.Context, name, link, udp string) error {
	f.replaced = append(f.replaced, name)
	return nil
}

func (f *fakeLister) ProbeServers(context.Context) (guardian.ServerListResponse, error) {
	out := f.list
	out.Servers = append([]guardian.ServerEntry{}, f.list.Servers...)
	for i := range out.Servers {
		out.Servers[i].Probe = &guardian.ProbeReport{Measured: true, Reachable: true, RTTMS: 180}
	}
	return out, nil
}

const deployTestLink = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd"

func TestANewServerIsAddedUnderItsAddressAndTested(t *testing.T) {
	f := &fakeLister{}
	got, err := recordDeployedServer(context.Background(), f, "", deployTestLink, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "203.0.113.9" || len(f.added) != 1 || len(f.replaced) != 0 {
		t.Fatalf("recorded %+v, added %v replaced %v", got, f.added, f.replaced)
	}
	if got.Probe == nil || !got.Probe.Reachable {
		t.Fatalf("no test result: %+v", got.Probe)
	}
}

// Redeploying a reinstalled VPS: the entry already pointing at this machine gets the new link —
// a second entry with the same address would leave the old, dead keys in the list.
func TestRedeployingTheSameMachineUpdatesItsEntry(t *testing.T) {
	f := &fakeLister{list: guardian.ServerListResponse{Servers: []guardian.ServerEntry{{Name: "tokyo", Host: "203.0.113.9", Current: true}}}}
	got, err := recordDeployedServer(context.Background(), f, "", deployTestLink, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "tokyo" || !got.Replaced || !got.Current || len(f.added) != 0 {
		t.Fatalf("recorded %+v, added %v", got, f.added)
	}
}

func TestATakenNameGetsASuffixInsteadOfOverwritingAnotherServer(t *testing.T) {
	taken := &guardian.HTTPError{Message: "409", Code: "servers_name_exists"}
	f := &fakeLister{
		list:      guardian.ServerListResponse{Servers: []guardian.ServerEntry{{Name: "home", Host: "198.51.100.7"}}},
		addErrFor: map[string]error{"home": taken},
	}
	got, err := recordDeployedServer(context.Background(), f, "home", deployTestLink, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "home-2" || len(f.replaced) != 0 {
		t.Fatalf("recorded %+v, replaced %v", got, f.replaced)
	}
}

func TestWithoutBxSetUpTheLinkComesBackInsteadOfBeingLost(t *testing.T) {
	f := &fakeLister{listErr: &guardian.UnavailableError{Err: errors.New("no socket")}}
	if _, err := recordDeployedServer(context.Background(), f, "", deployTestLink, ""); !errors.Is(err, errDeployNotSetUp) {
		t.Fatalf("err = %v, want errDeployNotSetUp", err)
	}
}

// The window's whole contract: steps in order, then one done line; the password never appears.
func TestMenuDeployReportsStepsThenDoneAndNeverThePassword(t *testing.T) {
	var lines []string
	emit := func(e deployEvent) {
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
	}
	err := runDeployForMenu(deployOptions{Host: "root@h", Protocol: "reality", Password: "pw-XYZ"}, deployDeps{
		run: func(name string, args ...string) (string, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "id -u"):
				return "0\nx86_64\n", nil
			case strings.Contains(joined, "server install"):
				return "sudo bx setup '" + blink.Encode(deployTestLink) + "'", nil
			}
			return "", nil
		},
		remoteFetch: func(string, func(string) (string, error)) error { return nil },
		fetchBinary: func(string) (string, error) { return "/tmp/bx", nil },
	}, &fakeLister{}, emit)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "pw-XYZ") {
		t.Fatal("the password reached the output")
	}
	var steps []string
	for _, l := range lines {
		var e deployEvent
		json.Unmarshal([]byte(l), &e)
		if e.Event == "step" {
			steps = append(steps, e.Step)
		}
	}
	if strings.Join(steps, " ") != "connect download install firewall start add test" {
		t.Fatalf("steps = %v", steps)
	}
	var last deployEvent
	json.Unmarshal([]byte(lines[len(lines)-1]), &last)
	if last.Event != "done" || !last.Added || last.Name == "" || last.Probe == nil {
		t.Fatalf("last event = %s", lines[len(lines)-1])
	}
}

func TestMenuDeployFailureIsOneErrorLineWithACategory(t *testing.T) {
	var last deployEvent
	err := runDeployForMenu(deployOptions{Host: "root@h"}, deployDeps{
		run: func(string, ...string) (string, error) {
			return "", errors.New("exit status 255: root@h: Permission denied (password).")
		},
	}, &fakeLister{}, func(e deployEvent) { last = e })
	if err == nil || last.Event != "error" || last.Code != "auth_failed" {
		t.Fatalf("err=%v last=%+v", err, last)
	}
}

// ssh splits a UserKnownHostsFile value on whitespace (it takes a list of files), and bx's own file
// lives under "~/Library/Application Support/". Unquoted, accept-new wrote the fingerprint to a stray
// file named ~/Library/Application, and "forget this fingerprint" deleted from the right file while
// ssh kept reading the wrong one (2026-09-30, caught by the end-to-end run against a test container).
func TestPathsWithSpacesAreQuotedForSSH(t *testing.T) {
	o := deploySSHOptions(sshOptionParams{ControlDir: "/tmp/with space", KnownHosts: "/Users/x/Library/Application Support/bx/known_hosts", Menu: true})
	if !hasOpt(o, `UserKnownHostsFile="/Users/x/Library/Application Support/bx/known_hosts"`) {
		t.Fatalf("known_hosts path not quoted: %v", o)
	}
	if !hasOpt(o, `ControlPath="/tmp/with space/c"`) {
		t.Fatalf("control path not quoted: %v", o)
	}
}
