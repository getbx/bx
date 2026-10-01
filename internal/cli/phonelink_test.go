package cli

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
)

// `bx phone-link` prints the link the iPhone needs for the server this Mac is using — the menu turns
// it into a QR ("Add to iPhone") after an administrator prompt (the config is root-only; no new
// way to read it is opened).
func TestPhoneLinkIsTheCurrentServersRealityLink(t *testing.T) {
	cfg := "servers:\n  - name: home\n    link: " + deployTestLink + "\n  - name: other\n    link: vless://11111111-2222-3333-4444-555555555555@198.51.100.7:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd\ncurrent: home\n"
	link, err := phoneLinkFromConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	inner, err := blink.Decode(link)
	if err != nil || !strings.HasPrefix(link, "bx://") || !strings.Contains(inner, "@203.0.113.9:") {
		t.Fatalf("link %q (inner %q, err %v): want the current server, as bx://", link, inner, err)
	}
	if _, err := phoneLinkFromConfig([]byte("server: brook://server?server=203.0.113.9%3A9999&password=x\n")); err == nil || !strings.Contains(err.Error(), "reality") {
		t.Fatalf("a brook server: err = %v — iPhone runs reality only, say so", err)
	}
}
