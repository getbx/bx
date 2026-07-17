package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getbx/bx/internal/install"
	updatepkg "github.com/getbx/bx/internal/update"
)

type macOSPackagePayload = updatepkg.MacOSPayload

type macOSAppOwner struct {
	uid int
	gid int
}

func parseMacOSAppOwner(raw string) (macOSAppOwner, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return macOSAppOwner{}, fmt.Errorf("invalid macOS app owner %q", raw)
	}
	uid, err := strconv.Atoi(parts[0])
	if err != nil || uid < 0 {
		return macOSAppOwner{}, fmt.Errorf("invalid macOS app owner %q", raw)
	}
	gid, err := strconv.Atoi(parts[1])
	if err != nil || gid < 0 {
		return macOSAppOwner{}, fmt.Errorf("invalid macOS app owner %q", raw)
	}
	return macOSAppOwner{uid: uid, gid: gid}, nil
}

func extractMacOSPackage(data []byte, arch string) (macOSPackagePayload, error) {
	return updatepkg.ExtractMacOSPackage(data, arch)
}

func validateMacOSPackagePath(name string) error {
	canonical := strings.TrimSuffix(name, "/")
	if canonical == "" || strings.HasPrefix(canonical, "/") || strings.Contains(canonical, "\\") || path.Clean(canonical) != canonical || strings.HasPrefix(canonical, "../") || strings.Contains(canonical, "/../") {
		return fmt.Errorf("macOS package contains unsafe path %q", name)
	}
	return nil
}

// replaceMacOSMenuApp stages the app beside its destination, then swaps the
// directory with rename. A running BxMenu keeps its old executable until the
// menu LaunchAgent is explicitly restarted after the full update succeeds.
func replaceMacOSMenuApp(destination string, payload macOSPackagePayload) error {
	return replaceMacOSMenuAppForOwner(destination, payload, nil)
}

func replaceMacOSMenuAppForOwner(destination string, payload macOSPackagePayload, owner *macOSAppOwner) error {
	if len(payload.Menu["Contents/MacOS/BxMenu"]) == 0 || len(payload.Menu["Contents/Info.plist"]) == 0 {
		return fmt.Errorf("macOS package payload is incomplete")
	}
	if filepath.Base(destination) != "Bx.app" || !filepath.IsAbs(destination) {
		return fmt.Errorf("invalid macOS app destination %q", destination)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create app parent: %w", err)
	}
	stageDir, err := os.MkdirTemp(parent, ".bx-update-")
	if err != nil {
		return fmt.Errorf("create app staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	stagedApp := filepath.Join(stageDir, "Bx.app")

	paths := make([]string, 0, len(payload.Menu))
	for name := range payload.Menu {
		if err := validateMacOSPackagePath("Bx.app/" + name); err != nil {
			return err
		}
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		target := filepath.Join(stagedApp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create app path %q: %w", name, err)
		}
		mode := os.FileMode(0o644)
		if name == "Contents/MacOS/BxMenu" {
			mode = 0o755
		}
		if err := os.WriteFile(target, payload.Menu[name], mode); err != nil {
			return fmt.Errorf("write app file %q: %w", name, err)
		}
	}
	if owner != nil {
		if err := chownTree(stagedApp, *owner); err != nil {
			return err
		}
	}

	backup := ""
	if _, err := os.Lstat(destination); err == nil {
		backup = filepath.Join(parent, ".Bx.app.previous-"+filepath.Base(stageDir))
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("stage existing app: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect existing app: %w", err)
	}
	if err := os.Rename(stagedApp, destination); err != nil {
		if backup != "" {
			_ = os.Rename(backup, destination)
		}
		return fmt.Errorf("activate updated app: %w", err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func chownTree(root string, owner macOSAppOwner) error {
	return filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := os.Chown(name, owner.uid, owner.gid); err != nil {
			return fmt.Errorf("restore app ownership for %q: %w", name, err)
		}
		return nil
	})
}

func applyMacOSPackage(cliDestination, appDestination string, payload macOSPackagePayload, owner *macOSAppOwner) error {
	if len(payload.CLI) == 0 {
		return fmt.Errorf("macOS package payload is missing bx executable")
	}
	if err := replaceMacOSMenuAppForOwner(appDestination, payload, owner); err != nil {
		return err
	}
	if err := install.ReplaceBinary(cliDestination, payload.CLI); err != nil {
		return fmt.Errorf("replace bx CLI: %w", err)
	}
	return nil
}

func restartMacOSMenu(owner macOSAppOwner) error {
	domain := fmt.Sprintf("gui/%d/com.getbx.bx.menu", owner.uid)
	return exec.Command("/bin/launchctl", "asuser", strconv.Itoa(owner.uid), "/bin/launchctl", "kickstart", "-k", domain).Run()
}
