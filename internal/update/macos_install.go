package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/getbx/bx/internal/install"
)

const (
	macOSSnapshotRoot = "/var/lib/bx/update/snapshots"
	macOSStagingRoot  = "/var/lib/bx/update/staging"
)

var macOSTransactionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type InstallOptions struct {
	CLIDestination string
	AppDestination string
	AppUID         int
	AppGID         int
	SnapshotDir    string
	StagingDir     string

	fileOps     FileOps
	consoleUser func() (macOSConsoleUser, error)
}

type macOSConsoleUser struct {
	uid  int
	gid  int
	home string
}

type FileOps interface {
	Lstat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	ReadFile(string) ([]byte, error)
	MkdirAll(string, fs.FileMode) error
	WriteFile(string, []byte, fs.FileMode) error
	Chmod(string, fs.FileMode) error
	Chown(string, int, int) error
	Rename(string, string) error
	RemoveAll(string) error
	SyncDir(string) error
}

type PreparedInstall struct {
	options     InstallOptions
	ops         FileOps
	platform    platformPreparedInstall
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
		platform, err := newPlatformPreparedInstall(options, payload)
		if err != nil {
			return nil, err
		}
		if platform != nil {
			return &PreparedInstall{options: options, platform: platform}, nil
		}
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
	if err := prepared.requireFreshTransaction(); err != nil {
		return nil, err
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
	if p.platform != nil {
		return p.platform.Activate()
	}
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
	if p.platform != nil {
		return p.platform.Restore()
	}
	if err := p.restoreCLI(); err != nil {
		return err
	}
	if err := p.restoreApp(); err != nil {
		return err
	}
	return nil
}

func (p *PreparedInstall) Commit() error {
	if p.platform != nil {
		return p.platform.Commit()
	}
	return p.cleanup()
}

type platformPreparedInstall interface {
	Activate() error
	Restore() error
	Commit() error
}

func (p *PreparedInstall) prepare(payload MacOSPayload) error {
	if err := p.ops.MkdirAll(p.options.SnapshotDir, 0o700); err != nil {
		return fmt.Errorf("create install snapshot: %w", err)
	}
	if err := p.ops.Chmod(p.options.SnapshotDir, 0o700); err != nil {
		return fmt.Errorf("restrict install snapshot: %w", err)
	}
	if err := p.ops.MkdirAll(p.options.StagingDir, 0o700); err != nil {
		return fmt.Errorf("create install staging: %w", err)
	}
	if err := p.ops.Chmod(p.options.StagingDir, 0o700); err != nil {
		return fmt.Errorf("restrict install staging: %w", err)
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
	if err := p.ops.Chmod(p.cliStage, 0o755); err != nil {
		return fmt.Errorf("set updated CLI mode: %w", err)
	}
	if err := stageApp(p.ops, p.appStage, payload, p.options.AppUID, p.options.AppGID); err != nil {
		return err
	}
	return nil
}

func (p *PreparedInstall) requireFreshTransaction() error {
	for _, path := range p.transactionPaths() {
		if _, err := p.ops.Lstat(path); err == nil {
			return fmt.Errorf("macOS install transaction path already exists %q", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect macOS install transaction path %q: %w", path, err)
		}
	}
	return nil
}

func (p *PreparedInstall) transactionPaths() []string {
	return []string{
		p.options.SnapshotDir,
		p.options.StagingDir,
		p.cliStage,
		p.cliStage + ".restore",
		p.appStage,
		p.appPrevious,
		p.appRestore,
		p.appDiscard,
	}
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
	for _, path := range []string{p.appStage, p.appPrevious, p.appRestore, p.appDiscard} {
		if err := p.removeDurably(path); err != nil {
			return err
		}
	}
	return nil
}

func (p *PreparedInstall) cleanup() error {
	for _, path := range []string{p.cliStage, p.cliStage + ".restore"} {
		if err := p.removeDurably(path); err != nil {
			return err
		}
	}
	if err := p.removeAppTransactionPaths(); err != nil {
		return err
	}
	if err := p.removeDurably(p.options.SnapshotDir); err != nil {
		return err
	}
	return p.removeDurably(p.options.StagingDir)
}

func (p *PreparedInstall) removeDurably(path string) error {
	if err := p.ops.RemoveAll(path); err != nil {
		return err
	}
	return p.ops.SyncDir(filepath.Dir(path))
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
	} else if err := validateUserAppDestination(app, options.AppUID, options.AppGID, options.consoleUser); err != nil {
		return err
	}
	for label, path := range map[string]string{"snapshot": options.SnapshotDir, "staging": options.StagingDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
			return fmt.Errorf("invalid macOS install %s directory %q", label, path)
		}
	}
	snapshotID := filepath.Base(options.SnapshotDir)
	stagingID := filepath.Base(options.StagingDir)
	if filepath.Dir(options.SnapshotDir) != macOSSnapshotRoot || filepath.Dir(options.StagingDir) != macOSStagingRoot ||
		snapshotID != stagingID || !macOSTransactionIDPattern.MatchString(snapshotID) {
		return fmt.Errorf("macOS install transaction directories must use matching safe IDs under %s and %s", macOSSnapshotRoot, macOSStagingRoot)
	}
	transactionPaths := installTransactionPaths(options)
	destinations := []string{options.CLIDestination, app}
	for _, transactionPath := range transactionPaths {
		for _, destination := range destinations {
			if pathsOverlap(transactionPath, destination) {
				return fmt.Errorf("macOS install transaction path %q overlaps destination %q", transactionPath, destination)
			}
		}
	}
	for i := range transactionPaths {
		for j := i + 1; j < len(transactionPaths); j++ {
			if pathsOverlap(transactionPaths[i], transactionPaths[j]) {
				return fmt.Errorf("macOS install transaction paths overlap: %q and %q", transactionPaths[i], transactionPaths[j])
			}
		}
	}
	return nil
}

func installTransactionPaths(options InstallOptions) []string {
	token := filepath.Base(filepath.Clean(options.StagingDir))
	cliParent := filepath.Dir(options.CLIDestination)
	appParent := filepath.Dir(options.AppDestination)
	cliStage := filepath.Join(cliParent, ".bx-update-"+token)
	return []string{
		options.SnapshotDir,
		options.StagingDir,
		cliStage,
		cliStage + ".restore",
		filepath.Join(appParent, ".Bx.app.update-"+token),
		filepath.Join(appParent, ".Bx.app.previous-"+token),
		filepath.Join(appParent, ".Bx.app.restore-"+token),
		filepath.Join(appParent, ".Bx.app.discard-"+token),
	}
}

func pathsOverlap(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func validateUserAppDestination(app string, uid, gid int, lookup func() (macOSConsoleUser, error)) error {
	if lookup == nil {
		lookup = discoverMacOSConsoleUser
	}
	console, err := lookup()
	if err != nil {
		return fmt.Errorf("discover macOS console user: %w", err)
	}
	if console.uid != uid || console.gid != gid {
		return fmt.Errorf("macOS app owner %d:%d is not the console user", uid, gid)
	}
	want := filepath.Join(console.home, "Applications", "Bx.app")
	if app != want {
		return fmt.Errorf("macOS app destination %q does not belong to console user %d", app, uid)
	}
	return nil
}

func discoverMacOSConsoleUser() (macOSConsoleUser, error) {
	output, err := exec.Command("/usr/bin/stat", "-f", "%u", "/dev/console").Output()
	if err != nil {
		return macOSConsoleUser{}, err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || uid <= 0 {
		return macOSConsoleUser{}, fmt.Errorf("invalid console UID")
	}
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return macOSConsoleUser{}, fmt.Errorf("look up macOS app owner %d: %w", uid, err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid < 0 || account.HomeDir == "" {
		return macOSConsoleUser{}, fmt.Errorf("invalid console account metadata")
	}
	return macOSConsoleUser{uid: uid, gid: gid, home: account.HomeDir}, nil
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
	if err := ops.Chmod(destination, 0o755); err != nil {
		return fmt.Errorf("set app staging directory mode: %w", err)
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
		if err := chmodDirectoryChain(ops, destination, filepath.Dir(target)); err != nil {
			return fmt.Errorf("set app directory mode for %q: %w", name, err)
		}
		mode := fs.FileMode(0o644)
		if name == "Contents/MacOS/BxMenu" {
			mode = 0o755
		}
		if err := ops.WriteFile(target, payload.Menu[name], mode); err != nil {
			return fmt.Errorf("write app file %q: %w", name, err)
		}
		if err := ops.Chmod(target, mode); err != nil {
			return fmt.Errorf("set app file mode %q: %w", name, err)
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
	if err := ops.Chmod(destination, info.Mode().Perm()); err != nil {
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
	if err := ops.WriteFile(destination, data, mode); err != nil {
		return err
	}
	return ops.Chmod(destination, mode)
}

func chmodDirectoryChain(ops FileOps, root, directory string) error {
	for current := directory; ; current = filepath.Dir(current) {
		if err := ops.Chmod(current, 0o755); err != nil {
			return err
		}
		if current == root {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current || !pathContains(root, current) {
			return fmt.Errorf("directory %q escapes app staging root", directory)
		}
	}
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
func (osFileOps) Chmod(path string, mode fs.FileMode) error { return os.Chmod(path, mode) }
func (osFileOps) Chown(path string, uid, gid int) error     { return os.Chown(path, uid, gid) }
func (osFileOps) Rename(oldPath, newPath string) error      { return os.Rename(oldPath, newPath) }
func (osFileOps) RemoveAll(path string) error               { return os.RemoveAll(path) }
func (osFileOps) SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
