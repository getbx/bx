package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/install"
)

func TestPreparedInstallRestoresCLIAndAppAfterActivationFailure(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	env.ops.FailRenameTo(env.appDestination)
	if err := prepared.Activate(); err == nil {
		t.Fatal("activation unexpectedly succeeded")
	}
	if err := prepared.Restore(); err != nil {
		t.Fatal(err)
	}
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
}

func TestPreparedInstallRestoreIsIdempotent(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Restore(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Restore(); err != nil {
		t.Fatalf("second restore: %v", err)
	}
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
}

func TestPreparedInstallCommitDeletesSnapshotAndStaging(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{env.snapshotPath, env.stagingPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("transaction path still exists %q: %v", path, err)
		}
	}
}

func TestPreparedInstallNeverCopiesConfigOrState(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	writeTestFile(t, filepath.Join(env.root, "etc/bx/config.yaml"), "server: secret-link", 0o600)
	prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := listRelativeFiles(env.ops.translate(prepared.SnapshotPath()))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Bx.app/Contents/Info.plist", "Bx.app/Contents/MacOS/BxMenu", "bx"}
	if !reflect.DeepEqual(want, entries) {
		t.Fatalf("entries = %#v, want %#v", entries, want)
	}
}

func TestPreparedInstallActivatesStagedCLIAndApp(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Activate(); err != nil {
		t.Fatal(err)
	}
	requireFileContents(t, env.cliPath, "new-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "new-menu")
	if mode := requireFileMode(t, env.cliPath); mode != 0o755 {
		t.Fatalf("CLI mode = %o", mode)
	}
	if mode := requireFileMode(t, filepath.Join(env.appPath, "Contents/Info.plist")); mode != 0o644 {
		t.Fatalf("Info.plist mode = %o", mode)
	}
}

func TestPreparedInstallEnforcesModesUnderRestrictiveUmask(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	env.ops.writeModeMask = 0o600
	prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Activate(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]fs.FileMode{
		env.cliPath: 0o755,
		filepath.Join(env.appPath, "Contents/MacOS/BxMenu"): 0o755,
		filepath.Join(env.appPath, "Contents/Info.plist"):   0o644,
	} {
		if got := requireFileMode(t, path); got != want {
			t.Fatalf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestPrepareMacOSInstallRejectsInvalidDestinationsAndOwnership(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	tests := []struct {
		name   string
		mutate func(*InstallOptions)
	}{
		{name: "CLI", mutate: func(options *InstallOptions) { options.CLIDestination = "/tmp/bx" }},
		{name: "relative app", mutate: func(options *InstallOptions) { options.AppDestination = "Applications/Bx.app" }},
		{name: "wrong app name", mutate: func(options *InstallOptions) { options.AppDestination = "/Applications/Other.app" }},
		{name: "system app owner", mutate: func(options *InstallOptions) { options.AppUID = 501 }},
		{name: "negative owner", mutate: func(options *InstallOptions) { options.AppGID = -1 }},
		{name: "snapshot outside state root", mutate: func(options *InstallOptions) { options.SnapshotDir = "/etc/bx-snapshot" }},
		{name: "staging outside state root", mutate: func(options *InstallOptions) { options.StagingDir = "/tmp/bx-staging" }},
		{name: "transaction token mismatch", mutate: func(options *InstallOptions) { options.StagingDir = filepath.Join(macOSStagingRoot, "tx-2") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := env.options()
			tt.mutate(&options)
			if _, err := PrepareMacOSInstall(options, testPayload("new-cli", "new-menu")); err == nil {
				t.Fatal("invalid install options were accepted")
			}
		})
	}
}

func TestValidateInstallOptionsAcceptsMatchingConsoleUserApp(t *testing.T) {
	options := consoleUserInstallOptions()
	if err := validateInstallOptions(options); err != nil {
		t.Fatalf("matching console user app was rejected: %v", err)
	}
}

func TestValidateInstallOptionsRejectsAnotherLocalUser(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InstallOptions)
	}{
		{name: "UID", mutate: func(options *InstallOptions) { options.AppUID = 502 }},
		{name: "GID", mutate: func(options *InstallOptions) { options.AppGID = 80 }},
		{name: "home", mutate: func(options *InstallOptions) {
			options.AppDestination = "/Users/another/Applications/Bx.app"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := consoleUserInstallOptions()
			tt.mutate(&options)
			if err := validateInstallOptions(options); err == nil {
				t.Fatal("another local user's app destination or ownership was accepted")
			}
		})
	}
}

func consoleUserInstallOptions() InstallOptions {
	return InstallOptions{
		CLIDestination: install.BinPath,
		AppDestination: "/Users/console/Applications/Bx.app",
		AppUID:         501,
		AppGID:         20,
		SnapshotDir:    filepath.Join(macOSSnapshotRoot, "tx-console"),
		StagingDir:     filepath.Join(macOSStagingRoot, "tx-console"),
		consoleUser: func() (macOSConsoleUser, error) {
			return macOSConsoleUser{uid: 501, gid: 20, home: "/Users/console"}, nil
		},
	}
}

func TestPrepareMacOSInstallRejectsIncompletePayloadBeforeMutation(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	if _, err := PrepareMacOSInstall(env.options(), MacOSPayload{CLI: []byte("new-cli")}); err == nil {
		t.Fatal("incomplete payload was accepted")
	}
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
}

func TestPrepareMacOSInstallRejectsOverlappingTransactionPathsWithoutMutation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*InstallOptions)
	}{
		{name: "snapshot is CLI", mutate: func(options *InstallOptions) { options.SnapshotDir = options.CLIDestination }},
		{name: "staging is app", mutate: func(options *InstallOptions) { options.StagingDir = options.AppDestination }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newInstallTestEnv(t, "old-cli", "old-menu")
			options := env.options()
			tt.mutate(&options)
			if _, err := PrepareMacOSInstall(options, testPayload("new-cli", "new-menu")); err == nil {
				t.Fatal("overlapping transaction path was accepted")
			}
			requireFileContents(t, env.cliPath, "old-cli")
			requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
		})
	}
}

func TestPrepareMacOSInstallPreservesExistingTransactionForRecovery(t *testing.T) {
	env := newInstallTestEnv(t, "old-cli", "old-menu")
	marker := filepath.Join(env.snapshotPath, "recovery-marker")
	writeTestFile(t, marker, "keep", 0o600)

	if _, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu")); err == nil {
		t.Fatal("existing transaction was silently replaced")
	}
	requireFileContents(t, marker, "keep")
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
}

type installTestEnv struct {
	root           string
	cliPath        string
	appPath        string
	snapshotPath   string
	stagingPath    string
	snapshotDir    string
	stagingDir     string
	appDestination string
	ops            *installTestFileOps
}

func newInstallTestEnv(t *testing.T, cli, menu string) *installTestEnv {
	t.Helper()
	root := t.TempDir()
	env := &installTestEnv{
		root:           root,
		cliPath:        filepath.Join(root, "usr/local/bin/bx"),
		appPath:        filepath.Join(root, "Applications/Bx.app"),
		snapshotPath:   filepath.Join(root, "var/lib/bx/update/snapshots/tx-1"),
		stagingPath:    filepath.Join(root, "var/lib/bx/update/staging/tx-1"),
		snapshotDir:    filepath.Join(macOSSnapshotRoot, "tx-1"),
		stagingDir:     filepath.Join(macOSStagingRoot, "tx-1"),
		appDestination: "/Applications/Bx.app",
	}
	env.ops = &installTestFileOps{root: root}
	writeTestFile(t, env.cliPath, cli, 0o755)
	writeTestFile(t, filepath.Join(env.appPath, "Contents/Info.plist"), "old-plist", 0o644)
	writeTestFile(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), menu, 0o755)
	return env
}

func (e *installTestEnv) options() InstallOptions {
	return InstallOptions{
		CLIDestination: install.BinPath,
		AppDestination: e.appDestination,
		AppUID:         0,
		AppGID:         0,
		SnapshotDir:    e.snapshotDir,
		StagingDir:     e.stagingDir,
		fileOps:        e.ops,
	}
}

func testPayload(cli, menu string) MacOSPayload {
	return MacOSPayload{
		CLI: []byte(cli),
		Menu: map[string][]byte{
			"Contents/Info.plist":   []byte("new-plist"),
			"Contents/MacOS/BxMenu": []byte(menu),
		},
	}
}

func writeTestFile(t *testing.T, path, contents string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func requireFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func requireFileMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func listRelativeFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(files)
	return files, err
}

type installTestFileOps struct {
	root          string
	failRenameTo  string
	writeModeMask fs.FileMode
}

func (o *installTestFileOps) FailRenameTo(path string) {
	o.failRenameTo = filepath.Clean(path)
}

func (o *installTestFileOps) translate(path string) string {
	path = filepath.Clean(path)
	for _, prefix := range []string{filepath.Dir(install.BinPath), "/Applications", macOSSnapshotRoot, macOSStagingRoot} {
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return filepath.Join(o.root, strings.TrimPrefix(path, string(filepath.Separator)))
		}
	}
	return path
}

func (o *installTestFileOps) Lstat(path string) (fs.FileInfo, error) {
	return os.Lstat(o.translate(path))
}

func (o *installTestFileOps) ReadDir(path string) ([]fs.DirEntry, error) {
	return os.ReadDir(o.translate(path))
}

func (o *installTestFileOps) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(o.translate(path))
}

func (o *installTestFileOps) MkdirAll(path string, mode fs.FileMode) error {
	return os.MkdirAll(o.translate(path), mode)
}

func (o *installTestFileOps) WriteFile(path string, data []byte, mode fs.FileMode) error {
	if o.writeModeMask != 0 {
		mode &= o.writeModeMask
	}
	return os.WriteFile(o.translate(path), data, mode)
}

func (o *installTestFileOps) Chmod(path string, mode fs.FileMode) error {
	return os.Chmod(o.translate(path), mode)
}

func (o *installTestFileOps) Chown(path string, uid, gid int) error {
	return nil
}

func (o *installTestFileOps) Rename(oldPath, newPath string) error {
	if filepath.Clean(newPath) == o.failRenameTo {
		o.failRenameTo = ""
		return errors.New("injected rename failure")
	}
	return os.Rename(o.translate(oldPath), o.translate(newPath))
}

func (o *installTestFileOps) RemoveAll(path string) error {
	return os.RemoveAll(o.translate(path))
}
