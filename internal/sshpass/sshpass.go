// Package sshpass hands a server password to the system ssh without putting it anywhere else.
//
// Why it exists: the menu's "Set Up a New Server" window takes the SSH password in the window
// (owner's decision, 2026-09-30 — a terminal is too hard for the people this is for, and typing
// into a terminal does not read to them as "bx does not see it" anyway). The password must then
// reach ssh, and ssh reads passwords only from a TTY or an SSH_ASKPASS program.
//
// The shape: the deploy process keeps the password in memory and listens on a unix socket in a
// fresh 0700 directory. ssh runs bx itself as SSH_ASKPASS; that child finds the socket and a
// one-time nonce in its environment, asks, prints the answer to ssh, and exits. So:
//   - the password is never in an environment variable, argv, or a file;
//   - only password prompts are answered (never a key passphrase or a host-key question);
//   - each ssh process gets at most one answer — a second ask from the same process means the
//     password was wrong, and retrying it only feeds lockouts and fail2ban;
//   - Close removes the socket and its directory; nothing is stored.
package sshpass

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	socketEnv = "BX_ASKPASS_SOCKET"
	nonceEnv  = "BX_ASKPASS_NONCE"
)

// Server answers askpass requests for one deploy.
type Server struct {
	password string
	nonce    string
	dir      string
	sock     string
	askpass  string
	ln       net.Listener

	mu       sync.Mutex
	answered map[int]bool
	closed   bool
}

// Serve starts answering. askpassExe is the program ssh should run (this bx binary).
func Serve(password, askpassExe string) (*Server, error) {
	dir, err := os.MkdirTemp("", "bxpw")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s := &Server{
		password: password, nonce: hex.EncodeToString(raw), dir: dir, sock: sock,
		askpass: askpassExe, ln: ln, answered: map[int]bool{},
	}
	go s.loop()
	return s, nil
}

// Env is what the ssh processes need: where to ask, the nonce, and "always use askpass".
func (s *Server) Env() []string {
	return []string{
		"SSH_ASKPASS=" + s.askpass,
		"SSH_ASKPASS_REQUIRE=force",
		// Older OpenSSH only consults SSH_ASKPASS when DISPLAY is set.
		"DISPLAY=bx",
		socketEnv + "=" + s.sock,
		nonceEnv + "=" + s.nonce,
	}
}

// Close stops answering and removes the socket directory. Safe to call twice.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.password = ""
	s.mu.Unlock()
	s.ln.Close()
	os.RemoveAll(s.dir)
}

func (s *Server) loop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

// Request line: nonce \t ppid \t prompt \n. Reply: "OK\t<password>\n" or "NO\n".
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}
	parts := strings.SplitN(strings.TrimRight(line, "\n"), "\t", 3)
	if len(parts) != 3 || parts[0] != s.nonce {
		fmt.Fprint(conn, "NO\n")
		return
	}
	ppid, err := strconv.Atoi(parts[1])
	if err != nil || !IsPasswordPrompt(parts[2]) {
		fmt.Fprint(conn, "NO\n")
		return
	}
	s.mu.Lock()
	if s.closed || s.answered[ppid] {
		s.mu.Unlock()
		fmt.Fprint(conn, "NO\n")
		return
	}
	s.answered[ppid] = true
	pw := s.password
	s.mu.Unlock()
	fmt.Fprintf(conn, "OK\t%s\n", pw)
}

// IsPasswordPrompt accepts what OpenSSH asks for a login password ("user@host's password:",
// keyboard-interactive "Password:") and nothing else — not a key passphrase, not a yes/no.
func IsPasswordPrompt(prompt string) bool {
	p := strings.ToLower(prompt)
	return strings.Contains(p, "password") && !strings.Contains(p, "passphrase") && !strings.Contains(p, "new password")
}

// IsAskpass reports whether this process was started by ssh as our askpass program.
func IsAskpass() bool {
	return os.Getenv(socketEnv) != "" && os.Getenv(nonceEnv) != ""
}

// RunAskpass is the whole askpass program: ask the deploy process, print the answer for ssh.
// Exit status non-zero tells ssh "no answer" (it then fails with Permission denied).
func RunAskpass(args []string) int {
	prompt := ""
	if len(args) > 1 {
		prompt = args[1]
	}
	answer, ok := askVia(os.Getenv(socketEnv), os.Getenv(nonceEnv), os.Getppid(), prompt)
	if !ok {
		return 1
	}
	fmt.Println(answer)
	return 0
}

func askVia(sock, nonce string, ppid int, prompt string) (string, bool) {
	conn, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	prompt = strings.NewReplacer("\t", " ", "\n", " ").Replace(prompt)
	if _, err := fmt.Fprintf(conn, "%s\t%d\t%s\n", nonce, ppid, prompt); err != nil {
		return "", false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", false
	}
	status, answer, _ := strings.Cut(strings.TrimRight(line, "\n"), "\t")
	if status != "OK" {
		return "", false
	}
	return answer, true
}
