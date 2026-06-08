package provision

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureBrookExtractsAndPins(t *testing.T) {
	dir := t.TempDir()
	p, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "brook") {
		t.Fatalf("路径不对: %q", p)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "BROOKv1" {
		t.Fatalf("内容不对: %q", b)
	}
}

func TestEnsureBrookSkipsWhenVersionMatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "brook"), []byte("SENTINEL"), 0o755)
	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "brook"))
	if string(b) != "SENTINEL" {
		t.Fatalf("版本一致不应重写, got %q", b)
	}
}

func TestEnsureBrookReExtractsOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBrook(dir, "", []byte("BROOKv2"), "v2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "brook"))
	if string(b) != "BROOKv2" {
		t.Fatalf("版本变更应重写, got %q", b)
	}
}

func TestEnsureBrookOverride(t *testing.T) {
	dir := t.TempDir()
	ov := filepath.Join(dir, "mybrook")
	_ = os.WriteFile(ov, []byte("x"), 0o755)
	p, err := EnsureBrook(dir, ov, []byte("EMBED"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if p != ov {
		t.Fatalf("应返回 override 路径, got %q", p)
	}
	if _, err := EnsureBrook(dir, filepath.Join(dir, "nope"), nil, "v1"); err == nil {
		t.Fatal("override 不存在应报错")
	}
}

func TestEnsureListsCreatesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	dp, cp, err := EnsureLists(dir, []byte("D"), []byte("C"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dp); string(b) != "D" {
		t.Fatalf("domain 内容不对: %q", b)
	}
	if b, _ := os.ReadFile(cp); string(b) != "C" {
		t.Fatalf("cidr 内容不对: %q", b)
	}
	_ = os.WriteFile(dp, []byte("FRESH"), 0o644)
	if _, _, err := EnsureLists(dir, []byte("D"), []byte("C")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dp); string(b) != "FRESH" {
		t.Fatalf("已存在列表不应被覆盖, got %q", b)
	}
}
