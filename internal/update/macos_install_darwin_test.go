//go:build darwin

package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinPreparedInstallRejectsSymlinkedAppParent(t *testing.T) {
	env := newDarwinInstallTestEnv(t)
	attackerParent := filepath.Join(env.root, "attacker", "Applications")
	if err := os.MkdirAll(attackerParent, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(attackerParent, "Bx.app", "Contents", "MacOS", "BxMenu"), "attacker-menu", 0o755)
	if err := os.RemoveAll(env.appParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attackerParent, env.appParent); err != nil {
		t.Fatal(err)
	}

	if _, err := newDarwinPreparedInstall(env.options, testPayload("new-cli", "new-menu")); err == nil {
		t.Fatal("symlinked app parent was accepted")
	}
	requireFileContents(t, filepath.Join(attackerParent, "Bx.app", "Contents", "MacOS", "BxMenu"), "attacker-menu")
}

func TestDarwinPreparedInstallRejectsAppParentSubstitutionBeforeActivation(t *testing.T) {
	env := newDarwinInstallTestEnv(t)
	prepared, err := newDarwinPreparedInstall(env.options, testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Commit() })

	retainedParent := env.appParent + ".retained"
	if err := os.Rename(env.appParent, retainedParent); err != nil {
		t.Fatal(err)
	}
	attackerParent := filepath.Join(env.root, "attacker", "Applications")
	writeTestFile(t, filepath.Join(attackerParent, "Bx.app", "Contents", "MacOS", "BxMenu"), "attacker-menu", 0o755)
	if err := os.Symlink(attackerParent, env.appParent); err != nil {
		t.Fatal(err)
	}

	if err := prepared.Activate(); err == nil {
		t.Fatal("substituted app parent was accepted")
	}
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(attackerParent, "Bx.app", "Contents", "MacOS", "BxMenu"), "attacker-menu")
	requireFileContents(t, filepath.Join(retainedParent, "Bx.app", "Contents", "MacOS", "BxMenu"), "old-menu")
}

func TestDarwinPreparedInstallRejectsStagedAppSubstitutionBeforeActivation(t *testing.T) {
	env := newDarwinInstallTestEnv(t)
	prepared, err := newDarwinPreparedInstall(env.options, testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Commit() })

	stagePath := filepath.Join(env.appParent, ".Bx.app.update-tx-1")
	if err := os.Rename(stagePath, stagePath+".stolen"); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(stagePath, "Contents", "MacOS", "BxMenu"), "attacker-menu", 0o755)

	if err := prepared.Activate(); err == nil {
		t.Fatal("substituted app staging was accepted")
	}
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents", "MacOS", "BxMenu"), "old-menu")
}

func TestDarwinPreparedInstallActivatesRestoresAndCommits(t *testing.T) {
	env := newDarwinInstallTestEnv(t)
	prepared, err := newDarwinPreparedInstall(env.options, testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Activate(); err != nil {
		t.Fatal(err)
	}
	requireFileContents(t, env.cliPath, "new-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents", "MacOS", "BxMenu"), "new-menu")
	if mode := requireFileMode(t, env.cliPath); mode != 0o755 {
		t.Fatalf("CLI mode = %o, want 755", mode)
	}
	if mode := requireFileMode(t, filepath.Join(env.appPath, "Contents", "Info.plist")); mode != 0o644 {
		t.Fatalf("Info.plist mode = %o, want 644", mode)
	}

	if err := prepared.Restore(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Restore(); err != nil {
		t.Fatalf("second restore: %v", err)
	}
	requireFileContents(t, env.cliPath, "old-cli")
	requireFileContents(t, filepath.Join(env.appPath, "Contents", "MacOS", "BxMenu"), "old-menu")
	if err := prepared.Commit(); err != nil {
		t.Fatal(err)
	}
	requirePathAbsent(t, env.options.SnapshotDir)
	requirePathAbsent(t, env.options.StagingDir)
}

func TestDarwinPreparedInstallCleanupRefusesSubstitutedStage(t *testing.T) {
	env := newDarwinInstallTestEnv(t)
	prepared, err := newDarwinPreparedInstall(env.options, testPayload("new-cli", "new-menu"))
	if err != nil {
		t.Fatal(err)
	}

	stagePath := filepath.Join(env.appParent, ".Bx.app.update-tx-1")
	if err := os.Rename(stagePath, stagePath+".stolen"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stagePath, "keep")
	writeTestFile(t, marker, "attacker-data", 0o644)
	if err := prepared.Commit(); err == nil {
		t.Fatal("cleanup accepted a substituted app stage")
	}
	requireFileContents(t, marker, "attacker-data")
	if _, err := os.Stat(env.options.SnapshotDir); err != nil {
		t.Fatalf("failed app cleanup removed snapshot recovery state: %v", err)
	}
	if _, err := os.Stat(env.options.StagingDir); err != nil {
		t.Fatalf("failed app cleanup removed staging descriptor proof: %v", err)
	}
	if err := os.RemoveAll(stagePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stagePath+".stolen", stagePath); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Commit(); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	requirePathAbsent(t, env.options.SnapshotDir)
	requirePathAbsent(t, env.options.StagingDir)
}

type darwinInstallTestEnv struct {
	root      string
	cliPath   string
	appParent string
	appPath   string
	options   InstallOptions
}

func newDarwinInstallTestEnv(t *testing.T) *darwinInstallTestEnv {
	t.Helper()
	root := t.TempDir()
	cliPath := filepath.Join(root, "usr", "local", "bin", "bx")
	appParent := filepath.Join(root, "Users", "console", "Applications")
	appPath := filepath.Join(appParent, "Bx.app")
	snapshotRoot := filepath.Join(root, "var", "lib", "bx", "update", "snapshots")
	stagingRoot := filepath.Join(root, "var", "lib", "bx", "update", "staging")
	for _, path := range []string{filepath.Dir(cliPath), appParent, snapshotRoot, stagingRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, cliPath, "old-cli", 0o755)
	writeTestFile(t, filepath.Join(appPath, "Contents", "Info.plist"), "old-plist", 0o644)
	writeTestFile(t, filepath.Join(appPath, "Contents", "MacOS", "BxMenu"), "old-menu", 0o755)

	return &darwinInstallTestEnv{
		root:      root,
		cliPath:   cliPath,
		appParent: appParent,
		appPath:   appPath,
		options: InstallOptions{
			CLIDestination: cliPath,
			AppDestination: appPath,
			AppUID:         os.Getuid(),
			AppGID:         os.Getgid(),
			SnapshotDir:    filepath.Join(snapshotRoot, "tx-1"),
			StagingDir:     filepath.Join(stagingRoot, "tx-1"),
		},
	}
}

func requirePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q exists or could not be inspected: %v", path, err)
	}
}
