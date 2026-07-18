//go:build darwin

package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type darwinFileIdentity struct {
	dev  int32
	ino  uint64
	kind uint16
}

type darwinRetainedDir struct {
	fd       int
	path     string
	identity darwinFileIdentity
}

type darwinPreparedInstall struct {
	options InstallOptions

	cliParent      darwinRetainedDir
	appParent      darwinRetainedDir
	snapshotParent darwinRetainedDir
	stagingParent  darwinRetainedDir

	snapshotFD int
	stagingFD  int
	appStageFD int

	transactionName string
	cliName         string
	appName         string
	cliStageName    string
	cliRestoreName  string
	appStageName    string
	appPreviousName string
	appRestoreName  string
	appDiscardName  string

	hadCLI            bool
	hadApp            bool
	cliOriginal       darwinFileIdentity
	appOriginal       darwinFileIdentity
	cliSnapshot       darwinFileIdentity
	appSnapshot       darwinFileIdentity
	snapshotDir       darwinFileIdentity
	stagingDir        darwinFileIdentity
	cliStage          darwinFileIdentity
	appStage          darwinFileIdentity
	cliRestored       darwinFileIdentity
	appRestored       darwinFileIdentity
	appRestore        darwinFileIdentity
	appDiscard        darwinFileIdentity
	appPreviousAtName bool
	appRestoreAtName  bool
	appDiscardAtName  bool
	cliStageAtName    bool
	cliRestoreAtName  bool
	appStageAtName    bool
	snapshotAtName    bool
	stagingAtName     bool
	activatedCLI      bool
	activatedApp      bool
	closed            bool
}

func newPlatformPreparedInstall(options InstallOptions, payload MacOSPayload) (platformPreparedInstall, error) {
	return newDarwinPreparedInstall(options, payload)
}

func newDarwinPreparedInstall(options InstallOptions, payload MacOSPayload) (_ *darwinPreparedInstall, returnErr error) {
	if err := validateMacOSPayload(payload); err != nil {
		return nil, err
	}
	transactionName := filepath.Base(options.SnapshotDir)
	if transactionName == "." || transactionName != filepath.Base(options.StagingDir) || !macOSTransactionIDPattern.MatchString(transactionName) {
		return nil, fmt.Errorf("invalid Darwin install transaction paths")
	}
	p := &darwinPreparedInstall{
		options:         options,
		cliParent:       darwinRetainedDir{fd: -1},
		appParent:       darwinRetainedDir{fd: -1},
		snapshotParent:  darwinRetainedDir{fd: -1},
		stagingParent:   darwinRetainedDir{fd: -1},
		snapshotFD:      -1,
		stagingFD:       -1,
		appStageFD:      -1,
		transactionName: transactionName,
		cliName:         filepath.Base(options.CLIDestination),
		appName:         filepath.Base(options.AppDestination),
		cliStageName:    ".bx-update-" + transactionName,
		cliRestoreName:  ".bx-update-" + transactionName + ".restore",
		appStageName:    ".Bx.app.update-" + transactionName,
		appPreviousName: ".Bx.app.previous-" + transactionName,
		appRestoreName:  ".Bx.app.restore-" + transactionName,
		appDiscardName:  ".Bx.app.discard-" + transactionName,
	}
	if p.cliName == "." || p.appName == "." {
		return nil, fmt.Errorf("invalid Darwin install destination")
	}

	defer func() {
		if returnErr != nil {
			_ = p.cleanup()
		}
	}()
	var err error
	if p.cliParent, err = openDarwinRetainedDir(filepath.Dir(options.CLIDestination)); err != nil {
		return nil, fmt.Errorf("open CLI parent: %w", err)
	}
	if p.appParent, err = openDarwinRetainedDir(filepath.Dir(options.AppDestination)); err != nil {
		return nil, fmt.Errorf("open app parent: %w", err)
	}
	if p.snapshotParent, err = openDarwinRetainedDir(filepath.Dir(options.SnapshotDir)); err != nil {
		return nil, fmt.Errorf("open snapshot parent: %w", err)
	}
	if p.stagingParent, err = openDarwinRetainedDir(filepath.Dir(options.StagingDir)); err != nil {
		return nil, fmt.Errorf("open staging parent: %w", err)
	}
	if err := p.requireFreshTransaction(); err != nil {
		return nil, err
	}
	if p.snapshotFD, p.snapshotDir, err = createDarwinDirAt(p.snapshotParent.fd, p.transactionName, 0o700); err != nil {
		return nil, fmt.Errorf("create install snapshot: %w", err)
	}
	p.snapshotAtName = true
	if p.stagingFD, p.stagingDir, err = createDarwinDirAt(p.stagingParent.fd, p.transactionName, 0o700); err != nil {
		return nil, fmt.Errorf("create install staging: %w", err)
	}
	p.stagingAtName = true
	if err := p.snapshotExistingInstall(); err != nil {
		return nil, err
	}
	if p.cliStage, err = writeDarwinFileAt(p.cliParent.fd, p.cliStageName, payload.CLI, 0o755); err != nil {
		return nil, fmt.Errorf("stage updated CLI: %w", err)
	}
	p.cliStageAtName = true
	if p.appStageFD, p.appStage, err = stageDarwinAppAt(p.appParent.fd, p.appStageName, payload); err != nil {
		return nil, err
	}
	p.appStageAtName = true
	return p, nil
}

func (p *darwinPreparedInstall) requireFreshTransaction() error {
	checks := []struct {
		fd   int
		name string
	}{
		{p.snapshotParent.fd, p.transactionName},
		{p.stagingParent.fd, p.transactionName},
		{p.cliParent.fd, p.cliStageName},
		{p.cliParent.fd, p.cliRestoreName},
		{p.appParent.fd, p.appStageName},
		{p.appParent.fd, p.appPreviousName},
		{p.appParent.fd, p.appRestoreName},
		{p.appParent.fd, p.appDiscardName},
	}
	for _, check := range checks {
		if err := requireDarwinEntryAbsent(check.fd, check.name); err != nil {
			return fmt.Errorf("macOS install transaction path already exists %q: %w", check.name, err)
		}
	}
	return nil
}

func (p *darwinPreparedInstall) snapshotExistingInstall() error {
	cliFD, cliStat, err := openDarwinFileAt(p.cliParent.fd, p.cliName)
	if err == nil {
		defer unix.Close(cliFD)
		p.hadCLI = true
		p.cliOriginal = darwinIdentity(cliStat)
		if p.cliSnapshot, err = copyDarwinFileFDAt(cliFD, p.snapshotFD, "bx", uint32(cliStat.Mode)&0o777); err != nil {
			return fmt.Errorf("snapshot CLI: %w", err)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect existing CLI: %w", err)
	}

	appFD, appStat, err := openDarwinDirAt(p.appParent.fd, p.appName)
	if err == nil {
		defer unix.Close(appFD)
		p.hadApp = true
		p.appOriginal = darwinIdentity(appStat)
		var snapshotFD int
		snapshotFD, p.appSnapshot, err = copyDarwinTreeAt(appFD, p.snapshotFD, "Bx.app")
		if snapshotFD >= 0 {
			_ = unix.Close(snapshotFD)
		}
		if err != nil {
			return fmt.Errorf("snapshot app: %w", err)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect existing app: %w", err)
	}
	return nil
}

func (p *darwinPreparedInstall) Activate() error {
	if p.closed {
		return fmt.Errorf("macOS install transaction is closed")
	}
	if err := p.verifyBeforeActivation(); err != nil {
		return err
	}
	if err := unix.Renameat(p.cliParent.fd, p.cliStageName, p.cliParent.fd, p.cliName); err != nil {
		return fmt.Errorf("activate updated CLI: %w", err)
	}
	p.cliStageAtName = false
	if err := requireDarwinIdentityAt(p.cliParent.fd, p.cliName, p.cliStage); err != nil {
		return fmt.Errorf("verify activated CLI: %w", err)
	}
	p.activatedCLI = true

	if p.hadApp {
		if err := unix.RenameatxNp(p.appParent.fd, p.appName, p.appParent.fd, p.appPreviousName, unix.RENAME_EXCL); err != nil {
			return fmt.Errorf("stage existing app: %w", err)
		}
		p.appPreviousAtName = true
		if err := requireDarwinIdentityAt(p.appParent.fd, p.appPreviousName, p.appOriginal); err != nil {
			return fmt.Errorf("verify staged existing app: %w", err)
		}
	}
	if err := unix.RenameatxNp(p.appParent.fd, p.appStageName, p.appParent.fd, p.appName, unix.RENAME_EXCL); err != nil {
		return fmt.Errorf("activate updated app: %w", err)
	}
	p.appStageAtName = false
	if err := requireDarwinIdentityAt(p.appParent.fd, p.appName, p.appStage); err != nil {
		return fmt.Errorf("verify activated app: %w", err)
	}
	p.activatedApp = true
	if err := chownDarwinTreeFD(p.appStageFD, p.options.AppUID, p.options.AppGID); err != nil {
		return fmt.Errorf("set activated app ownership: %w", err)
	}
	return nil
}

func (p *darwinPreparedInstall) verifyBeforeActivation() error {
	for label, retained := range map[string]darwinRetainedDir{
		"CLI parent": p.cliParent,
		"app parent": p.appParent,
	} {
		if err := retained.verifyPathIdentity(); err != nil {
			return fmt.Errorf("verify %s before activation: %w", label, err)
		}
	}
	if err := requireDarwinIdentityAt(p.cliParent.fd, p.cliStageName, p.cliStage); err != nil {
		return fmt.Errorf("verify staged CLI before activation: %w", err)
	}
	if err := requireDarwinIdentityAt(p.appParent.fd, p.appStageName, p.appStage); err != nil {
		return fmt.Errorf("verify staged app before activation: %w", err)
	}
	if err := requireDarwinOriginalState(p.cliParent.fd, p.cliName, p.hadCLI, p.cliOriginal); err != nil {
		return fmt.Errorf("verify CLI destination before activation: %w", err)
	}
	if err := requireDarwinOriginalState(p.appParent.fd, p.appName, p.hadApp, p.appOriginal); err != nil {
		return fmt.Errorf("verify app destination before activation: %w", err)
	}
	if err := requireDarwinEntryAbsent(p.appParent.fd, p.appPreviousName); err != nil {
		return fmt.Errorf("verify previous app staging before activation: %w", err)
	}
	return nil
}

func (p *darwinPreparedInstall) Restore() error {
	if p.closed {
		return fmt.Errorf("macOS install transaction is closed")
	}
	if err := p.restoreCLI(); err != nil {
		return err
	}
	return p.restoreApp()
}

func (p *darwinPreparedInstall) restoreCLI() error {
	current, exists, err := darwinIdentityAt(p.cliParent.fd, p.cliName)
	if err != nil {
		return fmt.Errorf("inspect CLI before restore: %w", err)
	}
	if !p.hadCLI {
		if !exists {
			return nil
		}
		if !p.activatedCLI || !current.same(p.cliStage) {
			return fmt.Errorf("refuse to remove substituted CLI during restore")
		}
		if err := unix.Unlinkat(p.cliParent.fd, p.cliName, 0); err != nil {
			return fmt.Errorf("remove newly installed CLI: %w", err)
		}
		p.activatedCLI = false
		return nil
	}
	if exists && (current.same(p.cliOriginal) || (!p.cliRestored.zero() && current.same(p.cliRestored))) {
		return nil
	}
	if !exists || !p.activatedCLI || !current.same(p.cliStage) {
		return fmt.Errorf("refuse to replace substituted CLI during restore")
	}
	if err := p.removeTrackedEntry(p.cliParent.fd, p.cliRestoreName, p.cliRestored, &p.cliRestoreAtName); err != nil {
		return fmt.Errorf("clear CLI restore staging: %w", err)
	}
	snapshotFD, snapshotStat, err := openDarwinFileAt(p.snapshotFD, "bx")
	if err != nil {
		return fmt.Errorf("open CLI snapshot: %w", err)
	}
	defer unix.Close(snapshotFD)
	if !darwinIdentity(snapshotStat).same(p.cliSnapshot) {
		return fmt.Errorf("CLI snapshot identity changed")
	}
	p.cliRestored, err = copyDarwinFileFDAt(snapshotFD, p.cliParent.fd, p.cliRestoreName, uint32(snapshotStat.Mode)&0o777)
	if err != nil {
		return fmt.Errorf("stage CLI restore: %w", err)
	}
	p.cliRestoreAtName = true
	if err := unix.Renameat(p.cliParent.fd, p.cliRestoreName, p.cliParent.fd, p.cliName); err != nil {
		return fmt.Errorf("restore CLI: %w", err)
	}
	p.cliRestoreAtName = false
	if err := requireDarwinIdentityAt(p.cliParent.fd, p.cliName, p.cliRestored); err != nil {
		return fmt.Errorf("verify restored CLI: %w", err)
	}
	p.activatedCLI = false
	return nil
}

func (p *darwinPreparedInstall) restoreApp() error {
	current, exists, err := darwinIdentityAt(p.appParent.fd, p.appName)
	if err != nil {
		return fmt.Errorf("inspect app before restore: %w", err)
	}
	if !p.hadApp {
		if exists {
			if !p.activatedApp || !current.same(p.appStage) {
				return fmt.Errorf("refuse to remove substituted app during restore")
			}
			if err := removeDarwinEntryAt(p.appParent.fd, p.appName); err != nil {
				return fmt.Errorf("remove newly installed app: %w", err)
			}
		}
		p.activatedApp = false
		return p.cleanupAppPaths()
	}
	if exists && (current.same(p.appOriginal) || (!p.appRestored.zero() && current.same(p.appRestored))) {
		return p.cleanupAppPaths()
	}
	if exists && (!p.activatedApp || !current.same(p.appStage)) {
		return fmt.Errorf("refuse to replace substituted app during restore")
	}
	if err := p.removeTrackedEntry(p.appParent.fd, p.appRestoreName, p.appRestore, &p.appRestoreAtName); err != nil {
		return fmt.Errorf("clear app restore staging: %w", err)
	}
	snapshotFD, snapshotStat, err := openDarwinDirAt(p.snapshotFD, "Bx.app")
	if err != nil {
		return fmt.Errorf("open app snapshot: %w", err)
	}
	defer unix.Close(snapshotFD)
	if !darwinIdentity(snapshotStat).same(p.appSnapshot) {
		return fmt.Errorf("app snapshot identity changed")
	}
	var restoreFD int
	restoreFD, p.appRestore, err = copyDarwinTreeAt(snapshotFD, p.appParent.fd, p.appRestoreName)
	if err != nil {
		return fmt.Errorf("stage app restore: %w", err)
	}
	defer unix.Close(restoreFD)
	p.appRestoreAtName = true
	if exists {
		if err := requireDarwinEntryAbsent(p.appParent.fd, p.appDiscardName); err != nil {
			return fmt.Errorf("app discard staging is not empty: %w", err)
		}
		if err := unix.RenameatxNp(p.appParent.fd, p.appName, p.appParent.fd, p.appDiscardName, unix.RENAME_EXCL); err != nil {
			return fmt.Errorf("stage replaced app for removal: %w", err)
		}
		p.appDiscard = current
		p.appDiscardAtName = true
	}
	if err := unix.RenameatxNp(p.appParent.fd, p.appRestoreName, p.appParent.fd, p.appName, unix.RENAME_EXCL); err != nil {
		return fmt.Errorf("restore app: %w", err)
	}
	p.appRestoreAtName = false
	if err := requireDarwinIdentityAt(p.appParent.fd, p.appName, p.appRestore); err != nil {
		return fmt.Errorf("verify restored app: %w", err)
	}
	if err := chownDarwinTreeFD(restoreFD, p.options.AppUID, p.options.AppGID); err != nil {
		return fmt.Errorf("restore app ownership: %w", err)
	}
	p.appRestored = p.appRestore
	p.activatedApp = false
	return p.cleanupAppPaths()
}

func (p *darwinPreparedInstall) Commit() error {
	return p.cleanup()
}

func (p *darwinPreparedInstall) cleanupAppPaths() error {
	return errors.Join(
		p.removeTrackedEntry(p.appParent.fd, p.appStageName, p.appStage, &p.appStageAtName),
		p.removeTrackedEntry(p.appParent.fd, p.appPreviousName, p.appOriginal, &p.appPreviousAtName),
		p.removeTrackedEntry(p.appParent.fd, p.appRestoreName, p.appRestore, &p.appRestoreAtName),
		p.removeTrackedEntry(p.appParent.fd, p.appDiscardName, p.appDiscard, &p.appDiscardAtName),
	)
}

func (p *darwinPreparedInstall) cleanup() error {
	if p.closed {
		return nil
	}
	err := errors.Join(
		p.removeTrackedEntry(p.cliParent.fd, p.cliStageName, p.cliStage, &p.cliStageAtName),
		p.removeTrackedEntry(p.cliParent.fd, p.cliRestoreName, p.cliRestored, &p.cliRestoreAtName),
		p.cleanupAppPaths(),
		p.removeTrackedEntry(p.snapshotParent.fd, p.transactionName, p.snapshotDir, &p.snapshotAtName),
		p.removeTrackedEntry(p.stagingParent.fd, p.transactionName, p.stagingDir, &p.stagingAtName),
	)
	p.closeFDs()
	return err
}

func (p *darwinPreparedInstall) removeTrackedEntry(parentFD int, name string, identity darwinFileIdentity, atName *bool) error {
	if !*atName {
		return nil
	}
	if !identity.zero() {
		if err := requireDarwinIdentityAt(parentFD, name, identity); err != nil {
			return fmt.Errorf("refuse to remove substituted transaction path %q: %w", name, err)
		}
	}
	if err := removeDarwinEntryAt(parentFD, name); err != nil {
		return err
	}
	*atName = false
	return nil
}

func (p *darwinPreparedInstall) closeFDs() {
	if p.closed {
		return
	}
	seen := make(map[int]struct{})
	for _, fd := range []int{
		p.appStageFD,
		p.snapshotFD,
		p.stagingFD,
		p.cliParent.fd,
		p.appParent.fd,
		p.snapshotParent.fd,
		p.stagingParent.fd,
	} {
		if fd < 0 {
			continue
		}
		if _, ok := seen[fd]; ok {
			continue
		}
		seen[fd] = struct{}{}
		_ = unix.Close(fd)
	}
	p.closed = true
}

func (identity darwinFileIdentity) same(other darwinFileIdentity) bool {
	return identity.dev == other.dev && identity.ino == other.ino && identity.kind == other.kind
}

func (identity darwinFileIdentity) zero() bool {
	return identity.dev == 0 && identity.ino == 0 && identity.kind == 0
}

func darwinIdentity(stat *unix.Stat_t) darwinFileIdentity {
	return darwinFileIdentity{dev: stat.Dev, ino: stat.Ino, kind: stat.Mode & unix.S_IFMT}
}

func openDarwinRetainedDir(path string) (darwinRetainedDir, error) {
	fd, stat, err := openDarwinAbsoluteDir(path)
	if err != nil {
		return darwinRetainedDir{fd: -1}, err
	}
	return darwinRetainedDir{fd: fd, path: path, identity: darwinIdentity(stat)}, nil
}

func (dir darwinRetainedDir) verifyPathIdentity() error {
	var retained unix.Stat_t
	if err := unix.Fstat(dir.fd, &retained); err != nil {
		return err
	}
	if !darwinIdentity(&retained).same(dir.identity) {
		return fmt.Errorf("retained directory descriptor identity changed")
	}
	fd, stat, err := openDarwinAbsoluteDir(dir.path)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if !darwinIdentity(stat).same(dir.identity) {
		return fmt.Errorf("directory path identity changed")
	}
	return nil
}

func openDarwinAbsoluteDir(path string) (int, *unix.Stat_t, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || clean != path {
		return -1, nil, fmt.Errorf("directory path must be clean and absolute: %q", path)
	}
	if clean == "/var" || strings.HasPrefix(clean, "/var/") {
		clean = "/private" + clean
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if component == "" {
			continue
		}
		childFD, childStat, childErr := openDarwinDirAt(fd, component)
		_ = unix.Close(fd)
		if childErr != nil {
			return -1, nil, childErr
		}
		fd = childFD
		_ = childStat
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, nil, fmt.Errorf("path is not a directory")
	}
	return fd, &stat, nil
}

func openDarwinDirAt(parentFD int, name string) (int, *unix.Stat_t, error) {
	if err := validateDarwinName(name); err != nil {
		return -1, nil, err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, nil, fmt.Errorf("entry %q is not a directory", name)
	}
	return fd, &stat, nil
}

func openDarwinFileAt(parentFD int, name string) (int, *unix.Stat_t, error) {
	if err := validateDarwinName(name); err != nil {
		return -1, nil, err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return -1, nil, fmt.Errorf("entry %q is not a regular file", name)
	}
	return fd, &stat, nil
}

func createDarwinDirAt(parentFD int, name string, mode uint32) (int, darwinFileIdentity, error) {
	if err := validateDarwinName(name); err != nil {
		return -1, darwinFileIdentity{}, err
	}
	if err := unix.Mkdirat(parentFD, name, mode); err != nil {
		return -1, darwinFileIdentity{}, err
	}
	created := true
	defer func() {
		if created {
			_ = removeDarwinEntryAt(parentFD, name)
		}
	}()
	var pathStat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, darwinFileIdentity{}, err
	}
	if pathStat.Mode&unix.S_IFMT != unix.S_IFDIR || int(pathStat.Uid) != os.Geteuid() {
		return -1, darwinFileIdentity{}, fmt.Errorf("new directory %q was substituted", name)
	}
	fd, fdStat, err := openDarwinDirAt(parentFD, name)
	if err != nil {
		return -1, darwinFileIdentity{}, err
	}
	if !darwinIdentity(&pathStat).same(darwinIdentity(fdStat)) {
		_ = unix.Close(fd)
		return -1, darwinFileIdentity{}, fmt.Errorf("new directory %q changed during open", name)
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		_ = unix.Close(fd)
		return -1, darwinFileIdentity{}, err
	}
	created = false
	return fd, darwinIdentity(fdStat), nil
}

func writeDarwinFileAt(parentFD int, name string, data []byte, mode uint32) (darwinFileIdentity, error) {
	if err := validateDarwinName(name); err != nil {
		return darwinFileIdentity{}, err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return darwinFileIdentity{}, err
	}
	created := true
	defer func() {
		if created {
			_ = unix.Unlinkat(parentFD, name, 0)
		}
	}()
	defer unix.Close(fd)
	if err := writeDarwinFD(fd, data); err != nil {
		return darwinFileIdentity{}, err
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return darwinFileIdentity{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return darwinFileIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return darwinFileIdentity{}, fmt.Errorf("new file %q is not regular", name)
	}
	created = false
	return darwinIdentity(&stat), nil
}

func writeDarwinFD(fd int, data []byte) error {
	for len(data) > 0 {
		written, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func copyDarwinFileFDAt(sourceFD, destinationParentFD int, destinationName string, mode uint32) (darwinFileIdentity, error) {
	if err := validateDarwinName(destinationName); err != nil {
		return darwinFileIdentity{}, err
	}
	destinationFD, err := unix.Openat(destinationParentFD, destinationName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return darwinFileIdentity{}, err
	}
	created := true
	defer func() {
		if created {
			_ = unix.Unlinkat(destinationParentFD, destinationName, 0)
		}
	}()
	defer unix.Close(destinationFD)
	sourceCopy, err := unix.Dup(sourceFD)
	if err != nil {
		return darwinFileIdentity{}, err
	}
	sourceFile := os.NewFile(uintptr(sourceCopy), "source")
	destinationCopy, err := unix.Dup(destinationFD)
	if err != nil {
		_ = sourceFile.Close()
		return darwinFileIdentity{}, err
	}
	destinationFile := os.NewFile(uintptr(destinationCopy), "destination")
	_, copyErr := io.Copy(destinationFile, sourceFile)
	closeErr := errors.Join(sourceFile.Close(), destinationFile.Close())
	if copyErr != nil {
		return darwinFileIdentity{}, copyErr
	}
	if closeErr != nil {
		return darwinFileIdentity{}, closeErr
	}
	if err := unix.Fchmod(destinationFD, mode); err != nil {
		return darwinFileIdentity{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(destinationFD, &stat); err != nil {
		return darwinFileIdentity{}, err
	}
	created = false
	return darwinIdentity(&stat), nil
}

func copyDarwinTreeAt(sourceFD, destinationParentFD int, destinationName string) (int, darwinFileIdentity, error) {
	var sourceStat unix.Stat_t
	if err := unix.Fstat(sourceFD, &sourceStat); err != nil {
		return -1, darwinFileIdentity{}, err
	}
	destinationFD, identity, err := createDarwinDirAt(destinationParentFD, destinationName, uint32(sourceStat.Mode)&0o777)
	if err != nil {
		return -1, darwinFileIdentity{}, err
	}
	if err := copyDarwinTreeContents(sourceFD, destinationFD); err != nil {
		_ = unix.Close(destinationFD)
		_ = removeDarwinEntryAt(destinationParentFD, destinationName)
		return -1, darwinFileIdentity{}, err
	}
	return destinationFD, identity, nil
}

func copyDarwinTreeContents(sourceFD, destinationFD int) error {
	entries, err := readDarwinDir(sourceFD)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		var before unix.Stat_t
		if err := unix.Fstatat(sourceFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			childSourceFD, childStat, err := openDarwinDirAt(sourceFD, name)
			if err != nil {
				return err
			}
			if !darwinIdentity(&before).same(darwinIdentity(childStat)) {
				_ = unix.Close(childSourceFD)
				return fmt.Errorf("source directory %q changed during snapshot", name)
			}
			childDestinationFD, _, err := createDarwinDirAt(destinationFD, name, uint32(before.Mode)&0o777)
			if err == nil {
				err = copyDarwinTreeContents(childSourceFD, childDestinationFD)
			}
			_ = unix.Close(childSourceFD)
			if childDestinationFD >= 0 {
				_ = unix.Close(childDestinationFD)
			}
			if err != nil {
				return err
			}
		case unix.S_IFREG:
			childSourceFD, childStat, err := openDarwinFileAt(sourceFD, name)
			if err != nil {
				return err
			}
			if !darwinIdentity(&before).same(darwinIdentity(childStat)) {
				_ = unix.Close(childSourceFD)
				return fmt.Errorf("source file %q changed during snapshot", name)
			}
			_, err = copyDarwinFileFDAt(childSourceFD, destinationFD, name, uint32(before.Mode)&0o777)
			_ = unix.Close(childSourceFD)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("source contains non-regular file %q", name)
		}
	}
	return nil
}

func stageDarwinAppAt(parentFD int, name string, payload MacOSPayload) (int, darwinFileIdentity, error) {
	rootFD, identity, err := createDarwinDirAt(parentFD, name, 0o755)
	if err != nil {
		return -1, darwinFileIdentity{}, fmt.Errorf("create app staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = removeDarwinEntryAt(parentFD, name)
		}
	}()
	paths := make([]string, 0, len(payload.Menu))
	for path := range payload.Menu {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		parts := strings.Split(path, "/")
		directoryFD := rootFD
		var opened []int
		for _, component := range parts[:len(parts)-1] {
			childFD, _, openErr := openDarwinDirAt(directoryFD, component)
			if errors.Is(openErr, unix.ENOENT) {
				childFD, _, openErr = createDarwinDirAt(directoryFD, component, 0o755)
			}
			if openErr != nil {
				for _, fd := range opened {
					_ = unix.Close(fd)
				}
				_ = unix.Close(rootFD)
				return -1, darwinFileIdentity{}, fmt.Errorf("create app path %q: %w", path, openErr)
			}
			opened = append(opened, childFD)
			directoryFD = childFD
		}
		mode := uint32(0o644)
		if path == "Contents/MacOS/BxMenu" {
			mode = 0o755
		}
		_, writeErr := writeDarwinFileAt(directoryFD, parts[len(parts)-1], payload.Menu[path], mode)
		for _, fd := range opened {
			_ = unix.Close(fd)
		}
		if writeErr != nil {
			_ = unix.Close(rootFD)
			return -1, darwinFileIdentity{}, fmt.Errorf("write app file %q: %w", path, writeErr)
		}
	}
	complete = true
	return rootFD, identity, nil
}

func chownDarwinTreeFD(rootFD, uid, gid int) error {
	entries, err := readDarwinDir(rootFD)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		var before unix.Stat_t
		if err := unix.Fstatat(rootFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		var childFD int
		var childStat *unix.Stat_t
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			childFD, childStat, err = openDarwinDirAt(rootFD, name)
		case unix.S_IFREG:
			childFD, childStat, err = openDarwinFileAt(rootFD, name)
		default:
			return fmt.Errorf("app contains non-regular file %q", name)
		}
		if err != nil {
			return err
		}
		if !darwinIdentity(&before).same(darwinIdentity(childStat)) {
			_ = unix.Close(childFD)
			return fmt.Errorf("app entry %q changed during ownership update", name)
		}
		if before.Mode&unix.S_IFMT == unix.S_IFDIR {
			err = chownDarwinTreeFD(childFD, uid, gid)
		} else {
			err = unix.Fchown(childFD, uid, gid)
		}
		_ = unix.Close(childFD)
		if err != nil {
			return err
		}
	}
	return unix.Fchown(rootFD, uid, gid)
}

func readDarwinDir(fd int) ([]os.DirEntry, error) {
	copyFD, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(copyFD), "directory")
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func removeDarwinEntryAt(parentFD int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(parentFD, name, 0)
	}
	directoryFD, opened, err := openDarwinDirAt(parentFD, name)
	if err != nil {
		return err
	}
	if !darwinIdentity(&stat).same(darwinIdentity(opened)) {
		_ = unix.Close(directoryFD)
		return fmt.Errorf("directory %q changed during removal", name)
	}
	entries, err := readDarwinDir(directoryFD)
	if err == nil {
		for _, entry := range entries {
			if err = removeDarwinEntryAt(directoryFD, entry.Name()); err != nil {
				break
			}
		}
	}
	_ = unix.Close(directoryFD)
	if err != nil {
		return err
	}
	if err := requireDarwinIdentityAt(parentFD, name, darwinIdentity(&stat)); err != nil {
		return err
	}
	return unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
}

func requireDarwinEntryAbsent(parentFD int, name string) error {
	if err := validateDarwinName(name); err != nil {
		return err
	}
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("entry exists")
}

func requireDarwinOriginalState(parentFD int, name string, existed bool, identity darwinFileIdentity) error {
	current, exists, err := darwinIdentityAt(parentFD, name)
	if err != nil {
		return err
	}
	if !existed {
		if exists {
			return fmt.Errorf("destination appeared")
		}
		return nil
	}
	if !exists || !current.same(identity) {
		return fmt.Errorf("destination identity changed")
	}
	return nil
}

func requireDarwinIdentityAt(parentFD int, name string, want darwinFileIdentity) error {
	current, exists, err := darwinIdentityAt(parentFD, name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("entry is missing")
	}
	if !current.same(want) {
		return fmt.Errorf("entry identity changed")
	}
	return nil
}

func darwinIdentityAt(parentFD int, name string) (darwinFileIdentity, bool, error) {
	if err := validateDarwinName(name); err != nil {
		return darwinFileIdentity{}, false, err
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return darwinFileIdentity{}, false, nil
		}
		return darwinFileIdentity{}, false, err
	}
	return darwinIdentity(&stat), true, nil
}

func validateDarwinName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return fmt.Errorf("invalid directory entry name %q", name)
	}
	return nil
}
