//go:build darwin

package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	guardianLaunchdLabel      = "com.getbx.bx.guard"
	guardianLaunchdPlistPath  = "/Library/LaunchDaemons/com.getbx.bx.guard.plist"
	guardianLaunchdStdoutPath = "/var/log/bx-guard.log"
	guardianLaunchdStderrPath = "/var/log/bx-guard.err.log"
)

// GuardianPlistText returns the root LaunchDaemon contract used on macOS.
func GuardianPlistText(configPath string) string {
	args := []string{
		BinPath,
		"guardian",
		"--config",
		configPath,
		"--listen-dns",
		"127.0.0.1:53",
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>`)
	writeXMLEscaped(&b, guardianLaunchdLabel)
	b.WriteString(`</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, arg := range args {
		b.WriteString("    <string>")
		writeXMLEscaped(&b, arg)
		b.WriteString("</string>\n")
	}
	b.WriteString(`  </array>
  <key>UserName</key>
  <string>root</string>
  <key>GroupName</key>
  <string>wheel</string>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>`)
	writeXMLEscaped(&b, guardianLaunchdStdoutPath)
	b.WriteString(`</string>
  <key>StandardErrorPath</key>
  <string>`)
	writeXMLEscaped(&b, guardianLaunchdStderrPath)
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String()
}

func WriteGuardianUnit(configPath string) error {
	return writeGuardianUnitAt(guardianLaunchdPlistPath, configPath, os.Chown)
}

func writeGuardianUnitAt(path, configPath string, chown func(string, int, int) error) error {
	if !filepath.IsAbs(configPath) {
		return fmt.Errorf("Guardian config path must be absolute: %q", configPath)
	}
	if err := writeLaunchdPlist(path, GuardianPlistText(configPath)); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o644); err != nil {
		return fmt.Errorf("restrict Guardian plist %s: %w", path, err)
	}
	if chown == nil {
		return errors.New("Guardian plist ownership function required")
	}
	if err := chown(path, 0, 0); err != nil {
		return fmt.Errorf("set Guardian plist owner root:wheel: %w", err)
	}
	return nil
}

func GuardianInstalled() bool {
	_, err := os.Stat(guardianLaunchdPlistPath)
	return err == nil
}

func GuardianActive() bool {
	active, err := (execGuardianLaunchdControl{}).Loaded(context.Background(), guardianLaunchdLabel)
	return err == nil && active
}

func EnableGuardian() error {
	if !GuardianInstalled() {
		return fmt.Errorf("Guardian launchd service is not installed at %s", guardianLaunchdPlistPath)
	}
	return enableGuardianWithControl(context.Background(), execGuardianLaunchdControl{})
}

func enableGuardianWithControl(ctx context.Context, control guardianLaunchdControl) error {
	active, err := control.Loaded(ctx, guardianLaunchdLabel)
	if err != nil {
		return fmt.Errorf("inspect Guardian launchd service: %w", err)
	}
	for _, args := range guardianEnableCommands(active) {
		if err := control.Run(ctx, args...); err != nil {
			if loaded, statusErr := control.Loaded(ctx, guardianLaunchdLabel); statusErr == nil && loaded {
				return nil
			}
			return fmt.Errorf("launch Guardian with launchctl %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

func LegacyCoreLoaded() (bool, error) {
	return legacyCoreLoadedWithControl(context.Background(), execGuardianLaunchdControl{})
}

func LegacyCoreInstalled() bool {
	return legacyCoreInstalledAt([]string{launchdPlistPath, legacyLaunchdPlistPath})
}

func legacyCoreInstalledAt(paths []string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func legacyCoreLoadedWithControl(ctx context.Context, control guardianLaunchdControl) (bool, error) {
	loaded, err := loadedLegacyCoreLabels(ctx, control)
	if err != nil {
		return false, err
	}
	return anyLaunchdClientServiceLoaded(loaded), nil
}

func BootoutLegacyCoreUnit(ctx context.Context) error {
	return bootoutLegacyCoreWithControl(ctx, execGuardianLaunchdControl{})
}

func bootoutLegacyCoreWithControl(ctx context.Context, control guardianLaunchdControl) error {
	loaded, err := loadedLegacyCoreLabels(ctx, control)
	if err != nil {
		return err
	}
	for _, args := range legacyCoreBootoutCommands(loaded) {
		if err := control.Run(ctx, args...); err != nil {
			label := strings.TrimPrefix(args[1], "system/")
			stillLoaded, statusErr := control.Loaded(ctx, label)
			if statusErr == nil && !stillLoaded {
				continue
			}
			return fmt.Errorf("bootout legacy Core %s: %w", label, errors.Join(err, statusErr))
		}
	}
	return nil
}

func RemoveLegacyCoreUnit() error {
	return removeLegacyCoreUnitWithDeps(
		context.Background(),
		execGuardianLaunchdControl{},
		[]string{launchdPlistPath, legacyLaunchdPlistPath},
		os.Remove,
	)
}

func removeLegacyCoreUnitWithDeps(ctx context.Context, control guardianLaunchdControl, paths []string, remove func(string) error) error {
	loaded, err := loadedLegacyCoreLabels(ctx, control)
	if err != nil {
		return err
	}
	if anyLaunchdClientServiceLoaded(loaded) {
		return errors.New("refusing to delete a loaded direct Core launchd service")
	}
	for _, path := range paths {
		if err := remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy Core plist %s: %w", path, err)
		}
	}
	return nil
}

func loadedLegacyCoreLabels(ctx context.Context, control guardianLaunchdControl) (map[string]bool, error) {
	loaded := make(map[string]bool, len(launchdClientLabels()))
	for _, label := range launchdClientLabels() {
		active, err := control.Loaded(ctx, label)
		if err != nil {
			return nil, fmt.Errorf("inspect legacy Core label %s: %w", label, err)
		}
		loaded[label] = active
	}
	return loaded, nil
}

type guardianLaunchdControl interface {
	Loaded(context.Context, string) (bool, error)
	Run(context.Context, ...string) error
}

type execGuardianLaunchdControl struct{}

func (execGuardianLaunchdControl) Loaded(ctx context.Context, label string) (bool, error) {
	args := []string{"print", "system/" + label}
	output, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if launchdLabelAbsent(output) {
		return false, nil
	}
	return false, fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
}

func (execGuardianLaunchdControl) Run(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "launchctl", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func launchdLabelAbsent(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "could not find service") ||
		strings.Contains(text, "service not found") ||
		strings.Contains(text, "no such process") ||
		strings.Contains(text, "service is disabled")
}

func guardianEnableCommands(active bool) [][]string {
	if active {
		return nil
	}
	domainLabel := "system/" + guardianLaunchdLabel
	return [][]string{
		{"enable", domainLabel},
		{"bootstrap", "system", guardianLaunchdPlistPath},
		{"kickstart", "-k", domainLabel},
	}
}

func legacyCoreBootoutCommands(loaded map[string]bool) [][]string {
	var commands [][]string
	for _, label := range launchdClientLabels() {
		if loaded[label] {
			commands = append(commands, []string{"bootout", "system/" + label})
		}
	}
	return commands
}
