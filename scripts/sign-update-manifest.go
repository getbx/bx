package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getbx/bx/internal/update"
)

type assetsFlag []string

func (a *assetsFlag) String() string         { return strings.Join(*a, ",") }
func (a *assetsFlag) Set(value string) error { *a = append(*a, value); return nil }

func main() {
	var version, out string
	var assets assetsFlag
	var packages assetsFlag
	flag.StringVar(&version, "version", "", "release version")
	flag.StringVar(&out, "out", "", "manifest output path")
	flag.Var(&assets, "asset", "platform:path, repeat for each asset")
	flag.Var(&packages, "package", "platform:path, repeat for each full application package")
	flag.Parse()
	if version == "" || out == "" || len(assets) == 0 {
		fail("usage: --version <tag> --out <path> --asset <platform:path>")
	}
	key := loadKey(os.Getenv("BX_UPDATE_PRIVATE_KEY"))
	manifest := update.Manifest{Version: version}
	for _, raw := range assets {
		manifest.Assets = append(manifest.Assets, readAsset(raw, "asset"))
	}
	for _, raw := range packages {
		manifest.Packages = append(manifest.Packages, readAsset(raw, "package"))
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		fail("encode manifest: %v", err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fail("write manifest: %v", err)
	}
	if err := os.WriteFile(out+".sig", ed25519.Sign(key, data), 0o644); err != nil {
		fail("write signature: %v", err)
	}
}

func readAsset(raw, kind string) update.Asset {
	platform, path, ok := strings.Cut(raw, ":")
	if !ok || platform == "" || path == "" {
		fail("invalid --%s %q", kind, raw)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fail("read %s %s: %v", kind, path, err)
	}
	return update.Asset{Platform: platform, Name: filepath.Base(path), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Size: int64(len(data))}
}

func loadKey(raw string) ed25519.PrivateKey {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		fail("BX_UPDATE_PRIVATE_KEY must be PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		fail("parse update signing key: %v", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		fail("update signing key must be Ed25519")
	}
	return key
}
func fail(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(2) }
