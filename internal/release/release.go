// Package release 描述一个 bx macOS release 的自描述元数据(release.json)。
package release

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const (
	FileName      = "release.json"
	SchemaVersion = 1
)

type Info struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	Platform      string            `json:"platform"`
	Assets        map[string]string `json:"assets"`
}

func Load(path string) (Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	return Parse(data)
}

func Parse(data []byte) (Info, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var info Info
	if err := decoder.Decode(&info); err != nil {
		return Info{}, fmt.Errorf("release metadata invalid: %w", err)
	}
	if info.SchemaVersion != SchemaVersion {
		return Info{}, fmt.Errorf("release schema_version %d unsupported", info.SchemaVersion)
	}
	if info.Version == "" {
		return Info{}, errors.New("release version empty")
	}
	if info.Platform == "" {
		return Info{}, errors.New("release platform empty")
	}
	if len(info.Assets) == 0 {
		return Info{}, errors.New("release assets empty")
	}
	for name, digest := range info.Assets {
		if name == "" {
			return Info{}, errors.New("release asset name empty")
		}
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size {
			return Info{}, fmt.Errorf("release asset %q digest invalid", name)
		}
	}
	return info, nil
}

func Write(path string, info Info) error {
	if _, err := Parse(mustMarshal(info)); err != nil {
		return err
	}
	return os.WriteFile(path, mustMarshal(info), 0o644)
}

func mustMarshal(info Info) []byte {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func (i Info) MatchesPlatform(goos, goarch string) error {
	want := goos + "/" + goarch
	if i.Platform != want {
		return fmt.Errorf("release platform %q does not match %q", i.Platform, want)
	}
	return nil
}

func (i Info) VerifyAsset(name string, data []byte) error {
	digest, ok := i.Assets[name]
	if !ok {
		return fmt.Errorf("release has no asset %q", name)
	}
	sum := sha256.Sum256(data)
	want, err := hex.DecodeString(digest)
	if err != nil {
		return fmt.Errorf("release asset %q digest invalid", name)
	}
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return fmt.Errorf("release asset %q digest mismatch", name)
	}
	return nil
}
