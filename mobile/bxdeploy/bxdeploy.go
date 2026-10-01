// Package bxdeploy installs (or reuses) a bx server from the iPhone, for someone whose first bx is
// the phone. It is the phone's transport for internal/deploy — the same judgment the Mac's
// Set Up a New Server window runs, over Go's SSH client instead of the system ssh (an iOS app
// cannot run processes).
//
// Kept apart from bxkit on purpose: bxkit is pure judgment (no network, no files); this package
// opens an SSH connection and keeps a known_hosts file. Both are bound into the same framework.
//
// The password is used for this connection only and never stored: it lives in memory for the
// duration of Deploy (as the SSH password and, for a non-root login, for `sudo -S`).
package bxdeploy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/deploy"
	"github.com/getbx/bx/internal/version"
)

// Listener receives one JSON line per event: {"event":"step","step":…},
// {"event":"done","link":…,"udp":…,"host":…,"reused":…} or {"event":"error","code":…,"detail":…} —
// the same step ids and failure codes as `bx server deploy --json` on the Mac.
type Listener interface {
	Event(line string)
}

type event struct {
	Event  string `json:"event"`
	Step   string `json:"step,omitempty"`
	Link   string `json:"link,omitempty"`
	UDP    string `json:"udp,omitempty"`
	Host   string `json:"host,omitempty"`
	Reused bool   `json:"reused,omitempty"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Deploy blocks until done; call it off the main thread. knownHostsPath is a file in the app's own
// container (created if missing).
func Deploy(address string, sshPort int, user, password string, reinstall, forgetHostKey bool, knownHostsPath string, listener Listener) {
	emit := func(e event) {
		b, _ := json.Marshal(e)
		listener.Event(string(b))
	}
	fail := func(err error) {
		emit(event{Event: "error", Code: deploy.Classify(err.Error()), Detail: err.Error()})
	}
	address = strings.TrimSpace(address)
	user = strings.TrimSpace(user)
	if sshPort <= 0 {
		sshPort = 22
	}
	emit(event{Event: "step", Step: "connect"})
	if forgetHostKey {
		if err := forgetHost(knownHostsPath, address, sshPort); err != nil {
			fail(err)
			return
		}
	}
	callback, err := hostKeyCallback(knownHostsPath)
	if err != nil {
		fail(err)
		return
	}
	asked := false
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			// Some servers ask through keyboard-interactive instead; answer the password once.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i, q := range questions {
					if strings.Contains(strings.ToLower(q), "password") && !asked {
						answers[i] = password
						asked = true
					}
				}
				return answers, nil
			}),
		},
		HostKeyCallback: callback,
		Timeout:         15 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(address, strconv.Itoa(sshPort)), config)
	if err != nil {
		fail(err)
		return
	}
	defer client.Close()

	firstConnect := true
	res, err := deploy.Run(deploy.Options{
		Target: user + "@" + address, Address: address, Force: reinstall, Password: password,
	}, session{client}, deploy.Hooks{
		Step: func(id string) {
			if id == "connect" && firstConnect { // already said before dialing
				firstConnect = false
				return
			}
			emit(event{Event: "step", Step: id})
		},
		CanFeedStdin: true,
		FetchBinary:  deploy.FetchViaServer(version.UpdatePublicKey),
	})
	if err != nil {
		fail(err)
		return
	}
	emit(event{Event: "done", Link: res.Main, UDP: res.UDP, Host: linkHost(res.Main), Reused: res.Reused})
}

// session runs one command per SSH session on the shared connection (one login for the whole deploy).
type session struct{ client *ssh.Client }

func (s session) Run(cmd string, stdin *string, _ bool) (string, error) {
	sess, err := s.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	if stdin != nil {
		sess.Stdin = strings.NewReader(*stdin)
	}
	var out, errOut bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errOut
	if err := sess.Run(cmd); err != nil {
		if tail := strings.TrimSpace(errOut.String()); tail != "" {
			err = fmt.Errorf("%w: %s", err, tail)
		}
		return out.String(), err
	}
	return out.String(), nil
}

// hostKeyCallback remembers a server the first time (accept-new) and refuses one whose key changed.
func hostKeyCallback(path string) (ssh.HostKeyCallback, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &keyErr) && len(keyErr.Want) > 0:
			// Same words as OpenSSH, so the categories (host_key_changed) know it.
			return fmt.Errorf("REMOTE HOST IDENTIFICATION HAS CHANGED for %s — if you reinstalled the server, forget its fingerprint and try again", hostname)
		case errors.As(err, &keyErr):
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			af, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer af.Close()
			_, err = fmt.Fprintln(af, line)
			return err
		default:
			return err
		}
	}, nil
}

// forgetHost drops what was remembered for this server ("I reinstalled it").
func forgetHost(path, address string, port int) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	name := knownhosts.Normalize(net.JoinHostPort(address, strconv.Itoa(port)))
	var kept bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == name {
			continue
		}
		kept.WriteString(line + "\n")
	}
	return os.WriteFile(path, kept.Bytes(), 0o600)
}

// linkHost is the server address inside a bx:// link (for the confirmation the app shows).
func linkHost(link string) string {
	inner, err := blink.Decode(link)
	if err != nil {
		inner = link
	}
	if u, err := url.Parse(inner); err == nil {
		return u.Hostname()
	}
	return ""
}
