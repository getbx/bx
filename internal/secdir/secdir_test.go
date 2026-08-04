package secdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, os.Geteuid(), 0o755); err == nil {
		t.Fatal("regular file placeholder must be rejected")
	}
}

func TestEnsureRejectsRelativeAndWritableMode(t *testing.T) {
	if err := Ensure("relative/path", os.Geteuid(), 0o755); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if err := Ensure(filepath.Join(t.TempDir(), "bx"), os.Geteuid(), 0o775); err == nil {
		t.Fatal("group-writable mode must be rejected")
	}
	if err := ValidateMode(0o707); err == nil {
		t.Fatal("other-writable mode must be rejected")
	}
}
