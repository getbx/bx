package deploy

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/releasemanifest"
)

// ManifestName and SignatureName are the signed release manifest's asset names.
const (
	ManifestName  = "bx-update.json"
	SignatureName = "bx-update.json.sig"
)

// ManifestFetchCommand has the server find the latest release and print its signed manifest.
// The server is on the far side of any censorship; this side may have no route to GitHub yet
// (an iPhone before its first tunnel).
func ManifestFetchCommand() string {
	latest := "https://github.com/getbx/bx/releases/latest"
	return strings.Join([]string{
		"set -e",
		`u=$(curl -fsSLI -o /dev/null -w '%{url_effective}' ` + ShellQuote(latest) + `)`,
		`tag=${u##*/}`,
		`echo "tag=$tag"`,
		`echo "manifest=$(curl -fsSL "` + ReleaseDownloadBase + `/$tag/` + ManifestName + `" | base64 -w0)"`,
		`echo "sig=$(curl -fsSL "` + ReleaseDownloadBase + `/$tag/` + SignatureName + `" | base64 -w0)"`,
	}, "\n")
}

// FetchViaServer is a Hooks.FetchBinary that needs no network on this side: the server fetches
// the signed manifest, **this side verifies the signature** with publicKey (the key built into
// the app — the authority stays with the person, not with the server), picks linux/<arch>, and
// the server downloads that asset and checks it against the verified checksum.
func FetchViaServer(publicKey string) func(arch string, runRemote func(string) (string, error)) error {
	return func(arch string, runRemote func(string) (string, error)) error {
		out, err := runRemote(ManifestFetchCommand())
		if err != nil {
			return fmt.Errorf("the server could not reach GitHub to download bx: %w: %s", err, strings.TrimSpace(out))
		}
		tag, manifest, sig, err := parseManifestFetch(out)
		if err != nil {
			return fmt.Errorf("the server could not reach GitHub to download bx: %w", err)
		}
		m, err := releasemanifest.ParseAndVerify(manifest, sig, publicKey)
		if err != nil {
			// Same words as a checksum mismatch on purpose: ShouldFallBackToLocalUpload must never
			// treat a bad signature as "try another way".
			return fmt.Errorf("bx: checksum mismatch — the release manifest's signature does not verify (%v)", err)
		}
		asset, err := releasemanifest.FindAsset(m, "linux/"+arch)
		if err != nil {
			return err
		}
		if text, err := runRemote(FetchCommand(tag, asset)); err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
		}
		return nil
	}
}

func parseManifestFetch(out string) (tag string, manifest, sig []byte, err error) {
	var m64, s64 string
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "tag":
			tag = value
		case "manifest":
			m64 = value
		case "sig":
			s64 = value
		}
	}
	if tag == "" || !strings.HasPrefix(tag, "v") || m64 == "" || s64 == "" {
		return "", nil, nil, fmt.Errorf("the server's answer had no release (%q)", strings.TrimSpace(out))
	}
	if manifest, err = base64.StdEncoding.DecodeString(m64); err != nil {
		return "", nil, nil, err
	}
	if sig, err = base64.StdEncoding.DecodeString(s64); err != nil {
		return "", nil, nil, err
	}
	return tag, manifest, sig, nil
}
