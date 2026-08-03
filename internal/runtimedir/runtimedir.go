// Package runtimedir 管理 root-owned 的版本化 bx runtime 目录
// (/Library/Application Support/bx/runtime/<version> + current 符号链)。
package runtimedir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getbx/bx/internal/release"
)

const Root = "/Library/Application Support/bx/runtime"

var ErrNotInstalled = errors.New("bx runtime not installed")

func CurrentLink(root string) string    { return filepath.Join(root, "current") }
func ExecutablePath(root string) string { return filepath.Join(root, "current", "bx") }

func Installed(root string) bool {
	_, _, err := Current(root)
	return err == nil
}

func Current(root string) (release.Info, string, error) {
	target, err := os.Readlink(CurrentLink(root))
	if err != nil {
		return release.Info{}, "", ErrNotInstalled
	}
	if err := validVersionName(target); err != nil {
		return release.Info{}, "", fmt.Errorf("runtime current link invalid: %w", err)
	}
	versionDir := filepath.Join(root, target)
	info, err := release.Load(filepath.Join(versionDir, release.FileName))
	if err != nil {
		return release.Info{}, "", fmt.Errorf("runtime metadata unreadable: %w", err)
	}
	if _, err := os.Stat(filepath.Join(versionDir, "bx")); err != nil {
		return release.Info{}, "", fmt.Errorf("runtime executable missing: %w", err)
	}
	return info, versionDir, nil
}

type Payload struct {
	CLI  []byte
	Info release.Info
}

func InstallVersion(root string, payload Payload) (string, error) {
	if err := validVersionName(payload.Info.Version); err != nil {
		return "", err
	}
	if err := payload.Info.VerifyAsset("bx-cli", payload.CLI); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// MkdirAll/WriteFile 的 mode 参数会被 umask 二次过滤:sudo 环境常见 umask 077
	// 会把这里的 0o755/0o644 实际落成 0o700/0o600,导致非 root 的 bridge
	// (/usr/local/bin/bx)与菜单栏进程读不到 runtime——故创建后显式 Chmod 一遍,
	// 不依赖调用方 umask。
	if err := os.Chmod(root, 0o755); err != nil {
		return "", err
	}
	versionDir := filepath.Join(root, payload.Info.Version)
	staging := filepath.Join(root, ".staging-"+payload.Info.Version)
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, "bx"), payload.CLI, 0o755); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(staging, "bx"), 0o755); err != nil {
		return "", err
	}
	if err := release.Write(filepath.Join(staging, release.FileName), payload.Info); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(staging, release.FileName), 0o644); err != nil {
		return "", err
	}
	discard := filepath.Join(root, ".discard-"+payload.Info.Version)
	if err := os.RemoveAll(discard); err != nil {
		return "", err
	}
	if _, err := os.Stat(versionDir); err == nil {
		if err := os.Rename(versionDir, discard); err != nil {
			return "", err
		}
	}
	if err := os.Rename(staging, versionDir); err != nil {
		return "", err
	}
	if err := os.Chmod(versionDir, 0o755); err != nil {
		return "", err
	}
	_ = os.RemoveAll(discard)
	return versionDir, nil
}

func SwitchCurrent(root, version string) error {
	if err := validVersionName(version); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, version, "bx")); err != nil {
		return fmt.Errorf("runtime version %q not installed: %w", version, err)
	}
	next := filepath.Join(root, ".current-next")
	if err := os.Remove(next); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(version, next); err != nil {
		return err
	}
	return os.Rename(next, CurrentLink(root))
}

func validVersionName(version string) error {
	if version == "" || version == "current" ||
		strings.ContainsAny(version, "/\\") || strings.Contains(version, "..") ||
		strings.HasPrefix(version, ".") {
		return fmt.Errorf("runtime version name %q invalid", version)
	}
	return nil
}
