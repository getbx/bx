package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/setup"
)

// 菜单「Set Up a New Server」窗口那条路(`bx server deploy --json --password-stdin`)。
//
// 所有者 2026-09-30 定的:**小白要能用,就不能让他碰终端** —— 而「在终端里输入」在他们的
// 理解里也并不等于「bx 不知道密码」。所以密码在窗口里填、经 stdin 交给这条命令,只在内存里
// 交给 ssh(internal/sshpass),用完即丢,绝不写盘。装好之后**只加进清单、不切换**:换出口
// 是用户在清单里显式的一下。

// sshOptionParams 决定每一条 ssh/scp 带什么连接参数。
type sshOptionParams struct {
	Port       int    // 0 = 不指定(用 ssh 自己的默认与 ssh_config)
	ControlDir string // 连接复用的控制 socket 所在的私有目录
	KnownHosts string // 非空 = 用 bx 自己的 known_hosts(菜单那条路)
	Password   bool   // 用户在窗口里填了密码
	Menu       bool   // 没有终端:任何交互式提问都没人能答
}

// deploySSHOptions 是所有 ssh/scp 共用的 `-o` 参数。
//
//   - 连接复用:第一条连接登录一次,后面每一步都走它 —— 此前密码要问至少两遍。
//   - accept-new:第一次见的服务器直接记下指纹(一台刚买的 VPS 本来就是第一次见),
//     **变了的指纹照样拒绝** —— 那一条才是防中间人的,不能为了省事放掉。
//   - 填了密码:关掉公钥登录、只试一次密码。agent 里一串钥匙先试会撞上 MaxAuthTries,
//     而错的密码重试只会喂给 fail2ban。
//   - 菜单那条路没填密码(用钥匙):BatchMode,绝不挂在一个没人能答的提问上。
func deploySSHOptions(p sshOptionParams) []string {
	var o []string
	add := func(kv string) { o = append(o, "-o", kv) }
	add("ControlMaster=auto")
	// **路径一律加双引号。** ssh 把这两个值按空白拆开(UserKnownHostsFile 收的是一串文件),
	// 而 bx 自己的 known_hosts 在「~/Library/Application Support/」下 —— 不加引号时指纹被写进
	// 一个叫 ~/Library/Application 的野文件(2026-09-30 端到端测试撞到)。
	add(`ControlPath="` + filepath.Join(p.ControlDir, "c") + `"`)
	add("ControlPersist=120")
	add("ConnectTimeout=15")
	add("ServerAliveInterval=15")
	add("StrictHostKeyChecking=accept-new")
	if p.KnownHosts != "" {
		add(`UserKnownHostsFile="` + p.KnownHosts + `"`)
	}
	if p.Port > 0 {
		add(fmt.Sprintf("Port=%d", p.Port))
	}
	if p.Password {
		add("PubkeyAuthentication=no")
		add("PreferredAuthentications=keyboard-interactive,password")
		add("NumberOfPasswordPrompts=1")
	} else if p.Menu {
		add("BatchMode=yes")
	}
	return o
}

// deployKnownHostsPath 是菜单那条路自己的 known_hosts —— 不往用户的 ~/.ssh 里写,
// 「我重装过这台服务器」时删的也只是 bx 记下的那一条。
func deployKnownHostsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "bx")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "known_hosts"), nil
}

// forgetDeployHostKey 删掉 bx 记下的这台机器的指纹(用户确认重装过之后)。
func forgetDeployHostKey(knownHosts, target string, sshPort int) {
	host := target
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	names := []string{host}
	if sshPort > 0 && sshPort != 22 {
		names = append(names, fmt.Sprintf("[%s]:%d", host, sshPort))
	}
	for _, n := range names {
		_ = exec.Command("ssh-keygen", "-R", n, "-f", knownHosts).Run()
	}
	_ = os.Remove(knownHosts + ".old")
}

// deployRunner 执行 ssh/scp。
type deployRunner struct {
	env []string
	// interactive = 终端那条路:stdin/stderr 接到终端,ssh 要问什么照常问。
	interactive bool
}

func (r *deployRunner) run(name string, args ...string) (string, error) {
	return r.exec(nil, name, args...)
}

func (r *deployRunner) runInput(stdin, name string, args ...string) (string, error) {
	return r.exec(&stdin, name, args...)
}

func (r *deployRunner) exec(stdin *string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), r.env...)
	var stderr bytes.Buffer
	switch {
	case stdin != nil:
		cmd.Stdin = strings.NewReader(*stdin)
	case r.interactive:
		cmd.Stdin = os.Stdin
	}
	if r.interactive {
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	} else {
		cmd.Stderr = &stderr
	}
	out, err := cmd.Output()
	if err != nil && !r.interactive {
		// 菜单那条路看不到 ssh 的原话:把它带在错误里,判类(classifyDeployFailure)要靠它。
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			err = fmt.Errorf("%w: %s", err, tail)
		}
	}
	return string(out), err
}

// classifyDeployFailure 把一次失败归到用户能据以行动的那一类。菜单按类出话,原文放进「详情」。
func classifyDeployFailure(text string) string {
	t := strings.ToLower(text)
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(t, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("remote host identification has changed", "host key verification failed"):
		return "host_key_changed"
	case has("password has expired", "password change required", "you are required to change your password"):
		return "password_change_required"
	case has("permission denied", "too many authentication failures"):
		return "auth_failed"
	case has("timed out", "connection refused", "no route to host", "could not resolve hostname", "network is unreachable", "connection closed by", "connection reset"):
		return "unreachable"
	case has("sudo: a password is required", "sudo: a terminal is required", "incorrect password attempt"):
		return "sudo_password"
	case has("architecture was not recognized", "systemctl: command not found", "systemctl: not found"):
		return "unsupported_system"
	case has("checksum mismatch", "checksum of"):
		return "checksum"
	case has("already exists (pass --force"):
		// 这台上已经装过 bx server。菜单据此问「重装(换新钥匙)吗」,而不是报一句失败。
		return "already_installed"
	}
	return "install_failed"
}

// deployEvent 是 --json 模式下一行标准输出。菜单逐行读。
type deployEvent struct {
	Event string `json:"event"` // step | done | error
	Step  string `json:"step,omitempty"`
	// done
	Name     string                `json:"name,omitempty"`
	Host     string                `json:"host,omitempty"`
	Added    bool                  `json:"added"`
	Replaced bool                  `json:"replaced,omitempty"`
	Current  bool                  `json:"current,omitempty"`
	Probe    *guardian.ProbeReport `json:"probe,omitempty"`
	// NotSetUp:服务器装好了,但这台 Mac 上 bx 还没配过 —— 链接交回菜单去走首次设置。
	NotSetUp bool   `json:"not_set_up,omitempty"`
	Link     string `json:"link,omitempty"`
	UDP      string `json:"udp,omitempty"`
	// error
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// deployLister 是装好之后要问 Guardian 的四件事。**里面没有「切换」** —— 这条路在构造上
// 换不了出口。
type deployLister interface {
	ListServers(ctx context.Context) (guardian.ServerListResponse, error)
	AddServer(ctx context.Context, name, link, udp string) error
	ReplaceServer(ctx context.Context, name, link, udp string) error
	ProbeServers(ctx context.Context) (guardian.ServerListResponse, error)
}

var errDeployNotSetUp = errors.New("bx is not set up on this machine yet")

type recordedServer struct {
	Name     string
	Host     string
	Replaced bool
	Current  bool
	Probe    *guardian.ProbeReport
}

// recordDeployedServer 把刚装好的这台记进清单,并从这台 Mac 测一次能不能连上。
//
//   - 清单里已经有一台指向同一个地址:**换掉它的链接**(重装过的 VPS),不加重复项 ——
//     重复项会把旧的、已经失效的钥匙留在清单里。
//   - 否则按名字加(没给名字就用地址);名字被**另一台**占着就加后缀,绝不覆盖别人。
//   - 从不切换。
func recordDeployedServer(ctx context.Context, c deployLister, want, main, udp string) (recordedServer, error) {
	host, ok := setup.LinkHost(main)
	if !ok || host == "" {
		return recordedServer{}, fmt.Errorf("the server's link does not name a host")
	}
	list, err := c.ListServers(ctx)
	if err != nil {
		return recordedServer{}, fmt.Errorf("%w: %v", errDeployNotSetUp, err)
	}
	rec := recordedServer{Host: host}
	for _, e := range list.Servers {
		if e.Host == host && (want == "" || want == e.Name) {
			if err := c.ReplaceServer(ctx, e.Name, main, udp); err != nil {
				return recordedServer{}, err
			}
			rec.Name, rec.Replaced, rec.Current = e.Name, true, e.Current
			break
		}
	}
	if rec.Name == "" {
		base := strings.TrimSpace(want)
		if base == "" {
			base = host
		}
		if err := config.ValidateServerName(base); err != nil {
			return recordedServer{}, err
		}
		for i := 1; i <= 9 && rec.Name == ""; i++ {
			name := base
			if i > 1 {
				name = fmt.Sprintf("%s-%d", base, i)
			}
			err := c.AddServer(ctx, name, main, udp)
			var he *guardian.HTTPError
			switch {
			case err == nil:
				rec.Name = name
			case errors.As(err, &he) && he.Code == "servers_name_exists":
				continue
			default:
				return recordedServer{}, err
			}
		}
		if rec.Name == "" {
			return recordedServer{}, fmt.Errorf("the names %s … %s-9 are all taken", base, base)
		}
	}
	if probed, err := c.ProbeServers(ctx); err == nil {
		for _, e := range probed.Servers {
			if e.Name == rec.Name {
				rec.Probe = e.Probe
			}
		}
	}
	return rec, nil
}

// runDeployForMenu 是 --json 模式:逐步报进度,最后一行 done 或 error。
func runDeployForMenu(opts deployOptions, deps deployDeps, lister deployLister, emit func(deployEvent)) error {
	var mu sync.Mutex
	send := func(e deployEvent) {
		mu.Lock()
		defer mu.Unlock()
		emit(e)
	}
	deps.out = io.Discard
	deps.step = func(id string) { send(deployEvent{Event: "step", Step: id}) }
	var done deployEvent
	deps.writeLocalConfig = func(link string) error {
		main, udp := splitDeployedLink(link)
		send(deployEvent{Event: "step", Step: "add"})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		rec, err := recordDeployedServer(ctx, lister, opts.Name, main, udp)
		switch {
		case errors.Is(err, errDeployNotSetUp):
			// 服务器已经装好了 —— 链接交回菜单,让它接着走首次设置,别让这台白装。
			host, _ := setup.LinkHost(main)
			done = deployEvent{Event: "done", Host: host, NotSetUp: true, Link: main, UDP: udp}
			return nil
		case err != nil:
			return fmt.Errorf("adding it to your server list: %w", err)
		}
		send(deployEvent{Event: "step", Step: "test"})
		done = deployEvent{
			Event: "done", Name: rec.Name, Host: rec.Host, Added: true,
			Replaced: rec.Replaced, Current: rec.Current, Probe: rec.Probe,
		}
		return nil
	}
	if err := runServerDeploy(opts, deps); err != nil {
		send(deployEvent{Event: "error", Code: classifyDeployFailure(err.Error()), Detail: err.Error()})
		return err
	}
	send(done)
	return nil
}
