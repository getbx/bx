package release

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func validInfo(t *testing.T, payload []byte) Info {
	t.Helper()
	sum := sha256.Sum256(payload)
	return Info{
		SchemaVersion: SchemaVersion,
		Version:       "1.2.3",
		Platform:      "darwin/arm64",
		Assets:        map[string]string{"bx-cli": hex.EncodeToString(sum[:])},
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	payload := []byte("fake-cli-binary")
	info := validInfo(t, payload)
	path := filepath.Join(t.TempDir(), FileName)
	if err := Write(path, info); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != "1.2.3" || loaded.Platform != "darwin/arm64" ||
		loaded.Assets["bx-cli"] != info.Assets["bx-cli"] {
		t.Fatalf("roundtrip mismatch: %+v", loaded)
	}
	if err := loaded.VerifyAsset("bx-cli", payload); err != nil {
		t.Fatalf("VerifyAsset: %v", err)
	}
}

func TestVerifyAssetMismatch(t *testing.T) {
	info := validInfo(t, []byte("real"))
	if err := info.VerifyAsset("bx-cli", []byte("tampered")); err == nil {
		t.Fatal("want digest mismatch error")
	}
	if err := info.VerifyAsset("absent", []byte("x")); err == nil {
		t.Fatal("want unknown asset error")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"schema_version":1,"version":"v","platform":"darwin/arm64","assets":{"a":"` + hex64() + `"},"extra":1}`,
		"bad schema":    `{"schema_version":2,"version":"v","platform":"darwin/arm64","assets":{"a":"` + hex64() + `"}}`,
		"empty version": `{"schema_version":1,"version":"","platform":"darwin/arm64","assets":{"a":"` + hex64() + `"}}`,
		"no assets":     `{"schema_version":1,"version":"v","platform":"darwin/arm64","assets":{}}`,
		"bad digest":    `{"schema_version":1,"version":"v","platform":"darwin/arm64","assets":{"a":"zz"}}`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
}

func TestMatchesPlatform(t *testing.T) {
	info := validInfo(t, []byte("x"))
	if err := info.MatchesPlatform("darwin", "arm64"); err != nil {
		t.Fatalf("match: %v", err)
	}
	if err := info.MatchesPlatform("darwin", "amd64"); err == nil {
		t.Fatal("want platform mismatch error")
	}
}

func hex64() string {
	sum := sha256.Sum256([]byte("h"))
	return hex.EncodeToString(sum[:])
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); !os.IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}
