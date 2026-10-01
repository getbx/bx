package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/releasemanifest"
)

func signedManifest(t *testing.T) (pub string, manifest, sig []byte) {
	t.Helper()
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := releasemanifest.Manifest{Version: "v9.9.9", Assets: []releasemanifest.Asset{
		{Platform: "linux/amd64", Name: "bx_linux_amd64.tar.gz", SHA256: strings.Repeat("a", 64), Size: 1},
		{Platform: "linux/arm64", Name: "bx_linux_arm64.tar.gz", SHA256: strings.Repeat("b", 64), Size: 1},
	}}
	manifest, _ = json.Marshal(m)
	return base64.StdEncoding.EncodeToString(pk), manifest, ed25519.Sign(sk, manifest)
}

func serverAnswer(manifest, sig []byte) string {
	return "tag=v9.9.9\nmanifest=" + base64.StdEncoding.EncodeToString(manifest) + "\nsig=" + base64.StdEncoding.EncodeToString(sig) + "\n"
}

// The phone may have no route to GitHub before it has a tunnel, so the server fetches the signed
// manifest — and the phone checks the signature against the key built into the app. The authority
// over what goes into the server's /usr/local/bin stays on the person's side.
func TestTheServerFetchesAndThisSideVerifies(t *testing.T) {
	pub, manifest, sig := signedManifest(t)
	var asked []string
	run := func(script string) (string, error) {
		asked = append(asked, script)
		if strings.Contains(script, "releases/latest") {
			return serverAnswer(manifest, sig), nil
		}
		return "", nil
	}
	if err := FetchViaServer(pub)("arm64", run); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || !strings.Contains(asked[1], "bx_linux_arm64.tar.gz") || !strings.Contains(asked[1], strings.Repeat("b", 64)) {
		t.Fatalf("second script must download the arm64 asset with its signed checksum: %v", asked)
	}
}

func TestATamperedManifestIsRefusedWithoutFallingBack(t *testing.T) {
	pub, manifest, sig := signedManifest(t)
	manifest = []byte(strings.Replace(string(manifest), strings.Repeat("b", 64), strings.Repeat("c", 64), 1))
	run := func(script string) (string, error) { return serverAnswer(manifest, sig), nil }
	err := FetchViaServer(pub)("arm64", run)
	if err == nil || ShouldFallBackToLocalUpload(err) {
		t.Fatalf("err = %v — a bad signature must stop everything, not try another way", err)
	}
}

func TestAServerThatCannotReachGitHubSaysSo(t *testing.T) {
	pub, _, _ := signedManifest(t)
	run := func(string) (string, error) {
		return "curl: (6) Could not resolve host: github.com", errors.New("exit status 6")
	}
	if err := FetchViaServer(pub)("amd64", run); err == nil || !strings.Contains(err.Error(), "GitHub") {
		t.Fatalf("err = %v", err)
	}
}
