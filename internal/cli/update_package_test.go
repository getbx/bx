package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractMacOSPackageRequiresCLIAndMenu(t *testing.T) {
	archive := macOSPackageArchive(t, map[string][]byte{
		"bx-macos-arm64/bx":                         []byte("cli"),
		"bx-macos-arm64/Bx.app/Contents/Info.plist": []byte("plist"),
	})
	if _, err := extractMacOSPackage(archive, "arm64"); err == nil {
		t.Fatal("package without BxMenu must be rejected")
	}
}

func TestExtractMacOSPackageReturnsVerifiedPayload(t *testing.T) {
	archive := macOSPackageArchive(t, map[string][]byte{
		"bx-macos-arm64/bx":                             []byte("cli"),
		"bx-macos-arm64/Bx.app/Contents/Info.plist":     []byte("plist"),
		"bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu":   []byte("menu"),
		"bx-macos-arm64/Bx.app/Contents/Resources/icon": []byte("icon"),
		"bx-macos-arm64/README.txt":                     []byte("readme"),
	})
	payload, err := extractMacOSPackage(archive, "arm64")
	if err != nil {
		t.Fatalf("extract package: %v", err)
	}
	if got := string(payload.CLI); got != "cli" {
		t.Fatalf("CLI = %q", got)
	}
	if got := string(payload.Menu["Contents/MacOS/BxMenu"]); got != "menu" {
		t.Fatalf("BxMenu = %q", got)
	}
}

func TestExtractMacOSPackageAcceptsNormalDirectoryEntries(t *testing.T) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	entries := []tar.Header{
		{Name: "bx-macos-arm64/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "bx-macos-arm64/Bx.app/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "bx-macos-arm64/Bx.app/Contents/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "bx-macos-arm64/Bx.app/Contents/MacOS/", Typeflag: tar.TypeDir, Mode: 0o755},
	}
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(&entry); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string][]byte{
		"bx-macos-arm64/bx":                           []byte("cli"),
		"bx-macos-arm64/Bx.app/Contents/Info.plist":   []byte("plist"),
		"bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu": []byte("menu"),
	} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractMacOSPackage(out.Bytes(), "arm64"); err != nil {
		t.Fatalf("normal directory entries must be accepted: %v", err)
	}
}

func TestReplaceMacOSMenuAppAtomicallyReplacesExistingApp(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "Applications", "Bx.app")
	oldMenu := filepath.Join(destination, "Contents", "MacOS", "BxMenu")
	if err := os.MkdirAll(filepath.Dir(oldMenu), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldMenu, []byte("old menu"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := macOSPackagePayload{Menu: map[string][]byte{
		"Contents/Info.plist":   []byte("new plist"),
		"Contents/MacOS/BxMenu": []byte("new menu"),
	}}

	if err := replaceMacOSMenuApp(destination, payload); err != nil {
		t.Fatalf("replace app: %v", err)
	}
	got, err := os.ReadFile(oldMenu)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new menu" {
		t.Fatalf("menu = %q", got)
	}
}

func TestParseMacOSAppOwner(t *testing.T) {
	owner, err := parseMacOSAppOwner("501:20")
	if err != nil {
		t.Fatalf("parse owner: %v", err)
	}
	if owner.uid != 501 || owner.gid != 20 {
		t.Fatalf("owner = %+v", owner)
	}
	for _, raw := range []string{"", "501", "501:group", "-1:20", "501:20:1"} {
		if _, err := parseMacOSAppOwner(raw); err == nil {
			t.Fatalf("owner %q must be rejected", raw)
		}
	}
}

func TestApplyMacOSPackageReplacesCLIAndMenuApp(t *testing.T) {
	root := t.TempDir()
	cliDestination := filepath.Join(root, "bin", "bx")
	if err := os.MkdirAll(filepath.Dir(cliDestination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliDestination, []byte("old cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := macOSPackagePayload{
		CLI: []byte("new cli"),
		Menu: map[string][]byte{
			"Contents/Info.plist":   []byte("new plist"),
			"Contents/MacOS/BxMenu": []byte("new menu"),
		},
	}
	appDestination := filepath.Join(root, "Applications", "Bx.app")
	if err := applyMacOSPackage(cliDestination, appDestination, payload, nil); err != nil {
		t.Fatalf("apply package: %v", err)
	}
	cliData, err := os.ReadFile(cliDestination)
	if err != nil {
		t.Fatal(err)
	}
	if string(cliData) != "new cli" {
		t.Fatalf("CLI = %q", cliData)
	}
	menuData, err := os.ReadFile(filepath.Join(appDestination, "Contents", "MacOS", "BxMenu"))
	if err != nil {
		t.Fatal(err)
	}
	if string(menuData) != "new menu" {
		t.Fatalf("menu = %q", menuData)
	}
}

func macOSPackageArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	for name, data := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
