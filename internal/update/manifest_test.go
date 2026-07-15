package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

const testManifest = `{"version":"v1.2.3","assets":[{"platform":"darwin/arm64","name":"bx-macos-arm64.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42}]}`

func TestParseAndVerifyAcceptsSignedManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(testManifest)
	signature := ed25519.Sign(privateKey, data)

	manifest, err := ParseAndVerify(data, signature, base64.StdEncoding.EncodeToString(publicKey))
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	if manifest.Version != "v1.2.3" {
		t.Fatalf("version = %q", manifest.Version)
	}
	asset, err := FindAsset(manifest, "darwin/arm64")
	if err != nil {
		t.Fatalf("FindAsset: %v", err)
	}
	if asset.Name != "bx-macos-arm64.tar.gz" {
		t.Fatalf("asset = %+v", asset)
	}
}

func TestParseAndVerifyRejectsTamperedManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, []byte(testManifest))
	tampered := []byte(`{"version":"v9.9.9","assets":[]}`)

	if _, err := ParseAndVerify(tampered, signature, base64.StdEncoding.EncodeToString(publicKey)); err == nil {
		t.Fatal("tampered manifest was accepted")
	}
}

func TestFindAssetRejectsWrongPlatform(t *testing.T) {
	manifest := Manifest{Version: "v1.2.3", Assets: []Asset{{
		Platform: "darwin/arm64",
		Name:     "bx-macos-arm64.tar.gz",
		SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:     42,
	}}}
	if _, err := FindAsset(manifest, "darwin/amd64"); err == nil {
		t.Fatal("missing platform was accepted")
	}
}
