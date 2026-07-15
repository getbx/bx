package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type Manifest struct {
	Version string  `json:"version"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

func ParseAndVerify(data, signature []byte, publicKeyBase64 string) (Manifest, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil {
		return Manifest{}, fmt.Errorf("decode update public key: %w", err)
	}
	if len(key) != ed25519.PublicKeySize {
		return Manifest{}, fmt.Errorf("invalid update public key length")
	}
	if !ed25519.Verify(ed25519.PublicKey(key), data, signature) {
		return Manifest{}, fmt.Errorf("invalid update manifest signature")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if decoder.More() {
		return Manifest{}, fmt.Errorf("decode update manifest: trailing JSON")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return Manifest{}, fmt.Errorf("update manifest missing version")
	}
	if len(manifest.Assets) == 0 {
		return Manifest{}, fmt.Errorf("update manifest has no assets")
	}
	seen := make(map[string]struct{}, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		if strings.TrimSpace(asset.Platform) == "" || strings.TrimSpace(asset.Name) == "" {
			return Manifest{}, fmt.Errorf("update manifest asset missing platform or name")
		}
		if _, ok := seen[asset.Platform]; ok {
			return Manifest{}, fmt.Errorf("update manifest has duplicate platform %q", asset.Platform)
		}
		seen[asset.Platform] = struct{}{}
		if len(asset.SHA256) != 64 {
			return Manifest{}, fmt.Errorf("update manifest asset %q has invalid SHA256", asset.Name)
		}
		if _, err := hex.DecodeString(asset.SHA256); err != nil {
			return Manifest{}, fmt.Errorf("update manifest asset %q has invalid SHA256: %w", asset.Name, err)
		}
		if asset.Size <= 0 {
			return Manifest{}, fmt.Errorf("update manifest asset %q has invalid size", asset.Name)
		}
	}
	return manifest, nil
}

func FindAsset(manifest Manifest, platform string) (Asset, error) {
	for _, asset := range manifest.Assets {
		if asset.Platform == platform {
			return asset, nil
		}
	}
	return Asset{}, fmt.Errorf("update manifest has no asset for %q", platform)
}
