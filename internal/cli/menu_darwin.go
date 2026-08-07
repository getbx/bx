//go:build darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	menuLaunchdLabel       = "com.getbx.bx.menu"
	legacyMenuLaunchdLabel = "com.ggshr9.bx.menu"

	// menuBootstrapTimeout bounds every launchctl exec issued while
	// (re)loading the menu-bar LaunchAgent. Menu bootstrap is best-effort at
	// both call sites (app-install prints a warning, `bx up` stores it as a
	// non-fatal MenuWarning), so a stuck launchctl call (e.g. `kickstart -k`
	// waiting on a spawn that can never succeed) must never be able to hang
	// the parent command.
	menuBootstrapTimeout = 20 * time.Second
)

type menuLaunchdControl interface {
	Loaded(context.Context, string) (bool, error)
	Run(context.Context, ...string) error
}

type menuBootstrapDeps struct {
	homeDir    func(int) (string, error)
	fileExists func(string) (bool, error)
	remove     func(string) error
	control    menuLaunchdControl
}

// ensureMacOSMenuRunning 确保菜单栏在跑,但不重载它(不闪烁)。
func ensureMacOSMenuRunning(uid int) error {
	return ensureMacOSMenuReloaded(uid, false)
}

// ensureMacOSMenuReloaded 在 forceReload 时完整 bootout+bootstrap,让改过的
// plist 生效——只有安装/更新路径需要它。
func ensureMacOSMenuReloaded(uid int, forceReload bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), menuBootstrapTimeout)
	defer cancel()
	return ensureMacOSMenuRunningWithDeps(ctx, uid, forceReload, menuBootstrapDeps{
		homeDir: func(uid int) (string, error) {
			account, err := user.LookupId(strconv.Itoa(uid))
			if err != nil {
				return "", err
			}
			return account.HomeDir, nil
		},
		fileExists: func(path string) (bool, error) {
			_, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return err == nil, err
		},
		remove:  os.Remove,
		control: execMenuLaunchdControl{},
	})
}

func ensureMacOSMenuRunningWithDeps(ctx context.Context, uid int, forceReload bool, deps menuBootstrapDeps) error {
	if uid <= 0 {
		return fmt.Errorf("no logged-in console user")
	}
	if deps.homeDir == nil || deps.fileExists == nil || deps.remove == nil || deps.control == nil {
		return fmt.Errorf("menu bootstrap dependencies unavailable")
	}
	home, err := deps.homeDir(uid)
	if err != nil || home == "" {
		return fmt.Errorf("find console user home: %w", err)
	}
	agentDir := filepath.Join(home, "Library", "LaunchAgents")
	currentPlist := filepath.Join(agentDir, menuLaunchdLabel+".plist")
	legacyPlist := filepath.Join(agentDir, legacyMenuLaunchdLabel+".plist")
	installed, err := deps.fileExists(currentPlist)
	if err != nil {
		return fmt.Errorf("inspect menu LaunchAgent: %w", err)
	}
	if !installed {
		return fmt.Errorf("Bx menu LaunchAgent is not installed")
	}
	domain := fmt.Sprintf("gui/%d", uid)
	currentDomainLabel := domain + "/" + menuLaunchdLabel
	legacyDomainLabel := domain + "/" + legacyMenuLaunchdLabel

	// actionable wraps a launchctl-related error with a manual recovery hint
	// (a command the console user can run themselves, no root needed)
	// instead of letting the caller see a bare error with no next step. This
	// is deliberately unconditional, not just for a timed-out context: the
	// production failure that motivated this hint was a normal (non-timeout)
	// error from `bx up` (root) trying to bootstrap the console user's GUI
	// domain, and the fix that unblocked the user was exactly this command
	// run manually as themselves.
	actionable := func(err error) error {
		if err == nil {
			return nil
		}
		return fmt.Errorf("%w (reload manually as the console user (not root): launchctl bootout %s; launchctl bootstrap %s %s)",
			err, currentDomainLabel, domain, currentPlist)
	}

	currentLoaded, err := deps.control.Loaded(ctx, currentDomainLabel)
	if err != nil {
		return actionable(fmt.Errorf("inspect current menu label: %w", err))
	}
	legacyLoaded, err := deps.control.Loaded(ctx, legacyDomainLabel)
	if err != nil {
		return actionable(fmt.Errorf("inspect legacy menu label: %w", err))
	}

	commands := menuLaunchdCommands(uid, currentLoaded, legacyLoaded, forceReload, currentPlist)
	for _, args := range commands {
		if isMenuBootstrapCommand(args) {
			if err := deps.remove(legacyPlist); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove legacy menu LaunchAgent: %w", err)
			}
		}
		if err := deps.control.Run(ctx, args...); err != nil {
			return actionable(fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err))
		}
	}
	currentLoaded, err = deps.control.Loaded(ctx, currentDomainLabel)
	if err != nil {
		return actionable(fmt.Errorf("verify current menu label: %w", err))
	}
	if !currentLoaded {
		return fmt.Errorf("canonical menu label is not running")
	}
	legacyLoaded, err = deps.control.Loaded(ctx, legacyDomainLabel)
	if err != nil {
		return actionable(fmt.Errorf("verify legacy menu label: %w", err))
	}
	if legacyLoaded {
		return fmt.Errorf("legacy menu label is still running")
	}
	return nil
}

// menuLaunchdCommands returns the launchctl invocations needed to make the
// menu-bar LaunchAgent match currentPlist on disk. launchd only re-reads a
// plist on bootstrap, not on kickstart, so whenever the canonical label is
// already loaded it must be booted out and rebootstrapped first — otherwise
// a changed plist (e.g. the app relocating from ~/Applications to
// /Applications) never takes effect and a plain `kickstart -k` blocks
// forever trying to respawn a program that may no longer exist at its old
// path. bootstrap is therefore unconditional: it always appears, so a
// changed plist always takes effect.
func menuLaunchdCommands(uid int, currentLoaded, legacyLoaded, forceReload bool, currentPlist string) [][]string {
	domain := fmt.Sprintf("gui/%d", uid)
	var commands [][]string
	if legacyLoaded {
		commands = append(commands, []string{"bootout", domain + "/" + legacyMenuLaunchdLabel})
	}
	if currentLoaded && !forceReload {
		// 已经加载:不重载。此前无条件 bootout + bootstrap + kickstart -k,而这个
		// 函数挂在 bx up 的生命周期钩子上,于是**每次启动保护都把菜单栏杀掉重启**
		// ——用户看到图标消失一会儿又回来,不知道发生了什么(真机 2026-08-06)。
		// 不带 -k 的 kickstart:跑着就是 no-op,没跑才启动,因此既不闪烁,也能修好
		// 「已加载但没在跑」(干净退出时 KeepAlive{SuccessfulExit:false} 不会拉起)。
		commands = append(commands, []string{"kickstart", domain + "/" + menuLaunchdLabel})
		return commands
	}
	if currentLoaded {
		commands = append(commands, []string{"bootout", domain + "/" + menuLaunchdLabel})
	}
	commands = append(commands, menuBootstrapCommand(uid, currentPlist))
	commands = append(commands, []string{"kickstart", "-k", domain + "/" + menuLaunchdLabel})
	return commands
}

// menuBootstrapCommand builds the launchctl invocation that (re)loads the
// menu-bar LaunchAgent into the console user's GUI domain.
//
// `bx up` runs as root (it manages routing and TUN devices), and a root
// process bootstrapping gui/<uid> directly fails every time with EIO(5):
// "Bootstrap failed: 5: Input/output error" — root carries no audit session
// token for the target user's GUI domain, and bootstrap (unlike bootout or
// kickstart) requires actually being inside that session to register the
// new job. `launchctl asuser <uid>` re-execs launchctl with that user's
// session token before issuing the real bootstrap.
//
// Confirmed on real hardware (2026-08-05): `bx up` (root) hit exactly this
// EIO on every run, while running the identical bootstrap manually as the
// console user (no asuser, no root) succeeded immediately — i.e. the
// failure is root's context, not the command itself.
func menuBootstrapCommand(uid int, plist string) []string {
	domain := fmt.Sprintf("gui/%d", uid)
	return []string{"asuser", strconv.Itoa(uid), "launchctl", "bootstrap", domain, plist}
}

// isMenuBootstrapCommand reports whether args is the launchctl invocation
// built by menuBootstrapCommand. It no longer starts with "bootstrap"
// directly (that got pushed past the asuser/uid/launchctl prefix), so the
// caller that needs to single out the bootstrap step (to remove the legacy
// plist right before launchd re-reads the directory) checks for the
// "bootstrap" token anywhere in the args instead of at args[0].
func isMenuBootstrapCommand(args []string) bool {
	for _, arg := range args {
		if arg == "bootstrap" {
			return true
		}
	}
	return false
}

func consoleUserUID() (int, error) {
	output, err := exec.Command("/usr/bin/stat", "-f", "%u", "/dev/console").Output()
	if err != nil {
		return 0, fmt.Errorf("find console user: %w", err)
	}
	return parseConsoleUID(output)
}

func parseConsoleUID(output []byte) (int, error) {
	uid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || uid <= 0 {
		return 0, fmt.Errorf("no logged-in console user")
	}
	return uid, nil
}

type execMenuLaunchdControl struct{}

func (execMenuLaunchdControl) Loaded(ctx context.Context, domainLabel string) (bool, error) {
	output, err := exec.CommandContext(ctx, "launchctl", "print", domainLabel).CombinedOutput()
	if err == nil {
		return true, nil
	}
	text := strings.ToLower(string(output))
	if strings.Contains(text, "could not find service") || strings.Contains(text, "service not found") ||
		strings.Contains(text, "no such process") || strings.Contains(text, "service is disabled") {
		return false, nil
	}
	return false, fmt.Errorf("launchctl print %s: %w: %s", domainLabel, err, strings.TrimSpace(string(output)))
}

func (execMenuLaunchdControl) Run(ctx context.Context, args ...string) error {
	output, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
