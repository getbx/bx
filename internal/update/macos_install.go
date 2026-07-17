package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getbx/bx/internal/install"
)

type InstallOptions struct {
	CLIDestination string
	AppDestination string
	AppUID         int
	AppGID         int
	SnapshotDir    string
	StagingDir     string

	fileOps FileOps
}

type FileOps interface {
	Lstat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	ReadFile(string) ([]byte, error)
	MkdirAll(string, fs.FileMode) error
	WriteFile(string, []byte, fs.FileMode) error
	Chown(string, int, int) error
	Rename(string, string) error
	RemoveAll(string) error
}

type PreparedInstall struct {
	options     InstallOptions
	ops         FileOps
	hadCLI      bool
	hadApp      bool
	cliStage    string
	appStage    string
	appPrevious string
	appRestore  string
	appDiscard  string
}

func PrepareMacOSInstall(options InstallOptions, payload MacOSPayload) (*PreparedInstall, error) {
	if err := validateInstallOptions(options); err != nil {
		return nil, err
	}
	if err := validateMacOSPayload(payload); err != nil {
		return nil, err
	}
	ops := options.fileOps
	if ops == nil {
		ops = osFileOps{}
	}
	token := filepath.Base(filepath.Clean(options.StagingDir))
	prepared := &PreparedInstall{
		options:     options,
		ops:         ops,
		cliStage:    filepath.Join(filepath.Dir(options.CLIDestination), ".bx-update-"+token),
		appStage:    filepath.Join(filepath.Dir(options.AppDestination), ".Bx.app.update-"+token),
		appPrevious: filepath.Join(filepath.Dir(options.AppDestination), ".Bx.app.previous-"+token),
		appRestore:  filepath.Join(filepath.Dir(options.AppDestination), ".Bx.app.restore-"+token),
		appDiscard:  filepath.Join(filepath.Dir(options.AppDestination), ".Bx.app.discard-"+token),
	}

	if err := prepared.prepare(payload); err != nil {
		_ = prepared.cleanup()
		return nil, err
	}
	return prepared, nil
}

func (p *PreparedInstall) SnapshotPath() string {
	return p.options.SnapshotDir
}

func (p *PreparedInstall) Activate() error {
	if err := p.ops.Rename(p.cliStage, p.options.CLIDestination); err != nil {
		return fmt.Errorf("activate updated CLI: %w", err)
	}

	if _, err := p.ops.Lstat(p.options.AppDestination); err == nil {
		if err := p.ops.RemoveAll(p.appPrevious); err != nil {
			return fmt.Errorf("clear previous app staging: %w", err)
		}
		if err := p.ops.Rename(p.options.AppDestination, p.appPrevious); err != nil {
			return fmt.Errorf("stage existing app: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect app destination: %w", err)
	}
	if err := p.ops.Rename(p.appStage, p.options.AppDestination); err != nil {
		return fmt.Errorf("activate updated app: %w", err)
	}
	return nil
}

func (p *PreparedInstall) Restore() error {
	if err := p.restoreCLI(); err != nil {
		return err
	}
	if err := p.restoreApp(); err != nil {
		return err
	}
	return nil
}

func (p *PreparedInstall) Commit() error {
	return p.cleanup()
}

func (p *PreparedInstall) prepare(payload MacOSPayload) error {
	if err := p.cleanup(); err != nil {
		return fmt.Errorf("clear stale install transaction: %w", err)
	}
	if err := p.ops.MkdirAll(p.options.SnapshotDir, 0o700); err != nil {
		return fmt.Errorf("create install snapshot: %w", err)
	}
	if err := p.ops.MkdirAll(p.options.StagingDir, 0o700); err != nil {
		return fmt.Errorf("create install staging: %w", err)
	}

	cliInfo, err := p.ops.Lstat(p.options.CLIDestination)
	if err == nil {
		if !cliInfo.Mode().IsRegular() {
			return fmt.Errorf("existing CLI is not a regular file")
		}
		p.hadCLI = true
		if err := copyRegularFile(p.ops, p.options.CLIDestination, filepath.Join(p.options.SnapshotDir, "bx"), cliInfo.Mode().Perm()); err != nil {
			return fmt.Errorf("snapshot CLI: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing CLI: %w", err)
	}

	appInfo, err := p.ops.Lstat(p.options.AppDestination)
	if err == nil {
		if !appInfo.IsDir() {
			return fmt.Errorf("existing app is not a directory")
		}
		p.hadApp = true
		if err := copyTree(p.ops, p.options.AppDestination, filepath.Join(p.options.SnapshotDir, "Bx.app")); err != nil {
			return fmt.Errorf("snapshot app: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing app: %w", err)
	}

	if err := p.ops.MkdirAll(filepath.Dir(p.cliStage), 0o755); err != nil {
		return fmt.Errorf("create CLI staging parent: %w", err)
	}
	if err := p.ops.WriteFile(p.cliStage, payload.CLI, 0o755); err != nil {
		return fmt.Errorf("stage updated CLI: %w", err)
	}
	if err := stageApp(p.ops, p.appStage, payload, p.options.AppUID, p.options.AppGID); err != nil {
		return err
	}
	return nil
}

func (p *PreparedInstall) restoreCLI() error {
	if !p.hadCLI {
		if err := p.ops.RemoveAll(p.options.CLIDestination); err != nil {
			return fmt.Errorf("remove newly installed CLI: %w", err)
		}
		return nil
	}
	info, err := p.ops.Lstat(filepath.Join(p.options.SnapshotDir, "bx"))
	if err != nil {
		return fmt.Errorf("inspect CLI snapshot: %w", err)
	}
	restorePath := p.cliStage + ".restore"
	if err := p.ops.RemoveAll(restorePath); err != nil {
		return fmt.Errorf("clear CLI restore staging: %w", err)
	}
	if err := copyRegularFile(p.ops, filepath.Join(p.options.SnapshotDir, "bx"), restorePath, info.Mode().Perm()); err != nil {
		return fmt.Errorf("stage CLI restore: %w", err)
	}
	if err := p.ops.Rename(restorePath, p.options.CLIDestination); err != nil {
		return fmt.Errorf("restore CLI: %w", err)
	}
	return nil
}

func (p *PreparedInstall) restoreApp() error {
	if err := p.ops.RemoveAll(p.appRestore); err != nil {
		return fmt.Errorf("clear app restore staging: %w", err)
	}
	if !p.hadApp {
		if err := p.ops.RemoveAll(p.options.AppDestination); err != nil {
			return fmt.Errorf("remove newly installed app: %w", err)
		}
		return p.removeAppTransactionPaths()
	}
	if err := copyTree(p.ops, filepath.Join(p.options.SnapshotDir, "Bx.app"), p.appRestore); err != nil {
		return fmt.Errorf("stage app restore: %w", err)
	}
	if err := chownTree(p.ops, p.appRestore, p.options.AppUID, p.options.AppGID); err != nil {
		return fmt.Errorf("restore app ownership: %w", err)
	}
	if err := p.ops.RemoveAll(p.appDiscard); err != nil {
		return fmt.Errorf("clear app discard staging: %w", err)
	}
	if _, err := p.ops.Lstat(p.options.AppDestination); err == nil {
		if err := p.ops.Rename(p.options.AppDestination, p.appDiscard); err != nil {
			return fmt.Errorf("stage replaced app for removal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect app before restore: %w", err)
	}
	if err := p.ops.Rename(p.appRestore, p.options.AppDestination); err != nil {
		_ = p.ops.Rename(p.appDiscard, p.options.AppDestination)
		return fmt.Errorf("restore app: %w", err)
	}
	return p.removeAppTransactionPaths()
}

func (p *PreparedInstall) removeAppTransactionPaths() error {
	return errors.Join(
		p.ops.RemoveAll(p.appStage),
		p.ops.RemoveAll(p.appPrevious),
		p.ops.RemoveAll(p.appRestore),
		p.ops.RemoveAll(p.appDiscard),
	)
}

func (p *PreparedInstall) cleanup() error {
	return errors.Join(
		p.ops.RemoveAll(p.options.SnapshotDir),
		p.ops.RemoveAll(p.options.StagingDir),
		p.ops.RemoveAll(p.cliStage),
		p.ops.RemoveAll(p.cliStage+".restore"),
		p.removeAppTransactionPaths(),
	)
}

func validateInstallOptions(options InstallOptions) error {
	if options.CLIDestination != install.BinPath {
		return fmt.Errorf("invalid macOS CLI destination %q", options.CLIDestination)
	}
	if options.AppUID < 0 || options.AppGID < 0 {
		return fmt.Errorf("invalid macOS app owner %d:%d", options.AppUID, options.AppGID)
	}
	app := filepath.Clean(options.AppDestination)
	if !filepath.IsAbs(options.AppDestination) || filepath.Base(app) != "Bx.app" {
		return fmt.Errorf("invalid macOS app destination %q", options.AppDestination)
	}
	if app == "/Applications/Bx.app" {
		if options.AppUID != 0 || options.AppGID != 0 {
			return fmt.Errorf("system macOS app must be root-owned")
		}
	} else if err := validateUserAppDestination(app, options.AppUID, options.AppGID); err != nil {
		return err
	}
	for label, path := range map[string]string{"snapshot": options.SnapshotDir, "staging": options.StagingDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
			return fmt.Errorf("invalid macOS install %s directory %q", label, path)
		}
	}
	if options.SnapshotDir == options.StagingDir {
		return fmt.Errorf("snapshot and staging directories must differ")
	}
	return nil
}

func validateUserAppDestination(app string, uid, gid int) error {
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return fmt.Errorf("look up macOS app owner %d: %w", uid, err)
	}
	accountGID, err := strconv.Atoi(account.Gid)
	if err != nil || accountGID != gid {
		return fmt.Errorf("macOS app owner does not match user %d", uid)
	}
	want := filepath.Join(account.HomeDir, "Applications", "Bx.app")
	if app != want {
		return fmt.Errorf("macOS app destination %q does not belong to user %d", app, uid)
	}
	return nil
}

func validateMacOSPayload(payload MacOSPayload) error {
	if len(payload.CLI) == 0 {
		return fmt.Errorf("macOS package payload is missing bx executable")
	}
	if len(payload.Menu["Contents/MacOS/BxMenu"]) == 0 || len(payload.Menu["Contents/Info.plist"]) == 0 {
		return fmt.Errorf("macOS package payload is incomplete")
	}
	for name := range payload.Menu {
		if name == "" || strings.HasSuffix(name, "/") {
			return fmt.Errorf("macOS package payload contains invalid app path %q", name)
		}
		if err := validateMacOSPackagePath("Bx.app/" + name); err != nil {
			return err
		}
	}
	return nil
}

func stageApp(ops FileOps, destination string, payload MacOSPayload, uid, gid int) error {
	if err := ops.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create app staging directory: %w", err)
	}
	paths := make([]string, 0, len(payload.Menu))
	for name := range payload.Menu {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := ops.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create app path %q: %w", name, err)
		}
		mode := fs.FileMode(0o644)
		if name == "Contents/MacOS/BxMenu" {
			mode = 0o755
		}
		if err := ops.WriteFile(target, payload.Menu[name], mode); err != nil {
			return fmt.Errorf("write app file %q: %w", name, err)
		}
	}
	if err := chownTree(ops, destination, uid, gid); err != nil {
		return fmt.Errorf("set app ownership: %w", err)
	}
	return nil
}

func copyTree(ops FileOps, source, destination string) error {
	info, err := ops.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", source)
	}
	if err := ops.MkdirAll(destination, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := ops.ReadDir(source)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		destinationPath := filepath.Join(destination, entry.Name())
		entryInfo, err := ops.Lstat(sourcePath)
		if err != nil {
			return err
		}
		switch {
		case entryInfo.IsDir():
			if err := copyTree(ops, sourcePath, destinationPath); err != nil {
				return err
			}
		case entryInfo.Mode().IsRegular():
			if err := copyRegularFile(ops, sourcePath, destinationPath, entryInfo.Mode().Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("source contains non-regular file %q", sourcePath)
		}
	}
	return nil
}

func copyRegularFile(ops FileOps, source, destination string, mode fs.FileMode) error {
	data, err := ops.ReadFile(source)
	if err != nil {
		return err
	}
	if err := ops.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return ops.WriteFile(destination, data, mode)
}

func chownTree(ops FileOps, root string, uid, gid int) error {
	if err := ops.Chown(root, uid, gid); err != nil {
		return err
	}
	entries, err := ops.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			if err := chownTree(ops, path, uid, gid); err != nil {
				return err
			}
			continue
		}
		if err := ops.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

type osFileOps struct{}

func (osFileOps) Lstat(path string) (fs.FileInfo, error)       { return os.Lstat(path) }
func (osFileOps) ReadDir(path string) ([]fs.DirEntry, error)   { return os.ReadDir(path) }
func (osFileOps) ReadFile(path string) ([]byte, error)         { return os.ReadFile(path) }
func (osFileOps) MkdirAll(path string, mode fs.FileMode) error { return os.MkdirAll(path, mode) }
func (osFileOps) WriteFile(path string, data []byte, mode fs.FileMode) error {
	return os.WriteFile(path, data, mode)
}
func (osFileOps) Chown(path string, uid, gid int) error { return os.Chown(path, uid, gid) }
func (osFileOps) Rename(oldPath, newPath string) error  { return os.Rename(oldPath, newPath) }
func (osFileOps) RemoveAll(path string) error           { return os.RemoveAll(path) }
