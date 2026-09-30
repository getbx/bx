package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/srvgen"
)

// bx server harden:给已经装好的服务器补上 srvgen.HardenedRoute —— 不许经隧道去这台 VPS 的
// 回环、内网与云元数据地址。新装的服务器从一开始就带;这条给 2026-09-29 之前装的那些。
// 只动路由,钥匙与用户一个字节不碰;已经加固过时什么都不写、不重启。
func serverHardenCommand() *cli.Command {
	return &cli.Command{
		Name:   "harden",
		Usage:  "stop people who have your link from reaching this server's own local services and network",
		Action: serverHardenAction,
		Description: "Adds a rule to the server's sing-box config: connections through the tunnel may not go to this\n" +
			"server's loopback address, its private network, or the cloud metadata address (169.254.169.254).\n" +
			"Anyone holding a link to this server (including links made with `bx server share`) could reach those\n" +
			"before. Keys and users are not changed. Servers installed after this version already have the rule.",
	}
}

func serverHardenAction(_ *cli.Context) error {
	msg, err := hardenServerConfig(serverSingboxPath, systemServerHost{})
	if msg != "" {
		fmt.Println(msg)
	}
	return err
}

// portHolder 是一个在听某个端口的进程,以及它属于哪个 systemd 单元("" = 不属于任何服务)。
type portHolder struct {
	PID     int
	Process string
	Unit    string
}

func (h portHolder) String() string {
	s := fmt.Sprintf("%s (pid %d", h.Process, h.PID)
	if h.Unit != "" {
		s += ", " + h.Unit
	}
	return s + ")"
}

// serverHost 是重启 bx server 时要问这台机器的几件事。
//
// 2026-09-30 所有者的 VPS:一个手写的 sing-box.service(Restart=always)一直在崩溃重试,
// 在 bx-server 重启的那一瞬间抢走了 bx 的端口,于是 bx-server 反过来崩溃循环 744 次、
// 整条隧道断掉 —— 而命令打印的是 ✓。所以重启之前先看有没有人在抢,重启之后确认端口
// 确实回到 bx 手里,不是就把原配置放回去。
type serverHost interface {
	Holders(srvgen.Listener) ([]portHolder, error)
	// RetryingSingboxUnits 是 bx-server 之外、正处于启动/自动重启中的 sing-box 单元 ——
	// 它们随时可能抢端口。已经稳定运行而没占我们端口的不算(那是用户自己的另一份配置)。
	RetryingSingboxUnits() ([]string, error)
	Restart() error
	Sleep(time.Duration)
}

const (
	serverSettleTimeout = 15 * time.Second
	serverSettlePoll    = time.Second
	serverSettleStable  = 3 // 连续几次都对才算稳 —— 抢端口的那个是在崩溃重试,抢一次要几秒
)

// hardenServerConfig is the testable half: read, patch, write only if changed, restart only if written,
// and never leave the server down: refuse before, verify after, restore on failure.
func hardenServerConfig(path string, host serverHost) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no sing-box server config at %s: this command is for reality / hysteria2 servers installed with bx server install (a brook server is not covered)", path)
	}
	if err != nil {
		return "", err
	}
	patched, changed, err := srvgen.Harden(raw)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if !changed {
		return "✓ This server is already hardened; nothing changed.", nil
	}
	listeners, err := srvgen.Listeners(raw)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	if err := checkBeforeServerRestart(listeners, host); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		return "", err
	}
	if err := restartAndConfirm(listeners, host); err != nil {
		return "", restorePreviousServerConfig(path, raw, listeners, host, err)
	}
	return "✓ Hardened: connections through the tunnel can no longer reach this server's local services, private network or cloud metadata address. Keys and users are unchanged.", nil
}

func checkBeforeServerRestart(listeners []srvgen.Listener, host serverHost) error {
	units, err := host.RetryingSingboxUnits()
	if err != nil {
		return fmt.Errorf("could not check for other sing-box services before restarting (nothing was changed): %w", err)
	}
	if len(units) > 0 {
		return fmt.Errorf("another sing-box service on this machine is starting or retrying: %s. It can take bx's ports while the bx server restarts and leave it down. Stop it first (sudo systemctl disable --now %s) and run this again. Nothing was changed",
			strings.Join(units, ", "), strings.Join(units, " "))
	}
	for _, l := range listeners {
		holders, err := host.Holders(l)
		if err != nil {
			return fmt.Errorf("could not check who listens on %s before restarting (nothing was changed): %w", l, err)
		}
		for _, h := range holders {
			if h.Unit != install.ServerServiceName {
				return fmt.Errorf("%s is held by %s, not by the bx server. Stop it first and run this again. Nothing was changed", l, h)
			}
		}
	}
	return nil
}

// restartAndConfirm restarts and waits until every port is held only by bx-server, several polls in a row.
func restartAndConfirm(listeners []srvgen.Listener, host serverHost) error {
	if err := host.Restart(); err != nil {
		return fmt.Errorf("restarting the bx server failed: %w", err)
	}
	stable := 0
	var last string
	for waited := time.Duration(0); waited <= serverSettleTimeout; waited += serverSettlePoll {
		host.Sleep(serverSettlePoll)
		problem := serverPortProblem(listeners, host)
		if problem == "" {
			stable++
			if stable >= serverSettleStable {
				return nil
			}
			continue
		}
		stable, last = 0, problem
	}
	return errors.New("the bx server did not come back: " + last)
}

func serverPortProblem(listeners []srvgen.Listener, host serverHost) string {
	for _, l := range listeners {
		holders, err := host.Holders(l)
		if err != nil {
			return fmt.Sprintf("could not check %s: %v", l, err)
		}
		if len(holders) == 0 {
			return fmt.Sprintf("nothing is listening on %s", l)
		}
		for _, h := range holders {
			if h.Unit != install.ServerServiceName {
				return fmt.Sprintf("%s is held by %s", l, h)
			}
		}
	}
	return ""
}

func restorePreviousServerConfig(path string, previous []byte, listeners []srvgen.Listener, host serverHost, cause error) error {
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		return fmt.Errorf("%v. Restoring the previous config at %s also failed: %v", cause, path, err)
	}
	if err := restartAndConfirm(listeners, host); err != nil {
		return fmt.Errorf("%v. The previous config was restored, but the server is still not back (%v); check sudo systemctl status %s", cause, err, install.ServerServiceName)
	}
	return fmt.Errorf("%v. The previous config was restored and the server is running with it; nothing else changed", cause)
}

// systemServerHost 是真的那一台:ss 看谁在听,/proc/<pid>/cgroup 看它属于哪个单元,systemctl 看别的 sing-box。
type systemServerHost struct{}

func (systemServerHost) Holders(l srvgen.Listener) ([]portHolder, error) {
	flag := "-Hltnp"
	if l.Proto == "udp" {
		flag = "-Hlunp"
	}
	out, err := exec.Command("ss", flag).Output()
	if err != nil {
		return nil, fmt.Errorf("ss %s: %w (ss comes with iproute2)", flag, err)
	}
	holders := parseSSListeners(string(out), l.Port)
	for i := range holders {
		if cg, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", holders[i].PID)); err == nil {
			holders[i].Unit = unitFromCgroup(string(cg))
		}
	}
	return holders, nil
}

func (systemServerHost) RetryingSingboxUnits() ([]string, error) {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--state=activating,reloading", "--no-legend", "--plain").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	var candidates []string
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] != install.ServerServiceName {
			candidates = append(candidates, f[0])
		}
	}
	var units []string
	for _, u := range candidates {
		exe, err := exec.Command("systemctl", "show", "-p", "ExecStart", "--value", u).Output()
		if err == nil && strings.Contains(string(exe), "sing-box") {
			units = append(units, u)
		}
	}
	return units, nil
}

func (systemServerHost) Restart() error        { return install.RestartServer() }
func (systemServerHost) Sleep(d time.Duration) { time.Sleep(d) }

var ssUserRe = regexp.MustCompile(`\("([^"]*)",pid=(\d+)`)

// parseSSListeners 从 `ss -H -l -n -p` 的输出里挑出本地端口恰好是 port 的那几行的进程。
func parseSSListeners(out string, port int) []portHolder {
	var holders []portHolder
	for _, line := range strings.Split(out, "\n") {
		var local string
		for _, f := range strings.Fields(line) {
			if strings.Contains(f, ":") {
				local = f
				break
			}
		}
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		if p, err := strconv.Atoi(local[i+1:]); err != nil || p != port {
			continue
		}
		for _, m := range ssUserRe.FindAllStringSubmatch(line, -1) {
			pid, _ := strconv.Atoi(m[2])
			holders = append(holders, portHolder{PID: pid, Process: m[1]})
		}
	}
	return holders
}

// unitFromCgroup 取 cgroup 路径里的 systemd 服务单元;登录会话之类不是服务的返回 ""。
func unitFromCgroup(cgroup string) string {
	for _, line := range strings.Split(cgroup, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "/")
		for j := len(parts) - 1; j >= 0; j-- {
			if strings.HasSuffix(parts[j], ".service") {
				return parts[j]
			}
		}
	}
	return ""
}
