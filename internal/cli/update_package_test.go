package cli

import (
	"os"
	"path/filepath"
	"testing"
)

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
