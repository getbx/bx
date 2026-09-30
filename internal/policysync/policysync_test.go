package policysync_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/policysync"
)

const (
	mine   = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"
	friend = "vless://66666666-7777-8888-9999-000000000000@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"
)

var policy = policysync.Policy{
	Version: 7, UpdatedAt: "2026-09-30T06:00:00Z", Global: true,
	Direct: []string{"*.apple.com", "secret-intranet.example"}, Proxy: []string{"x.com"},
}

func derive(t *testing.T, link string) policysync.Keys {
	t.Helper()
	k, err := policysync.Derive(link)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return k
}

// 同一个人的 Mac 与 iPhone 用同一条链接(裸的或 bx:// 换壳的都一样)⇒ 同一个存放位置、同一把钥匙。
func TestTheSameLinkGivesTheSameKeysOnEveryDevice(t *testing.T) {
	a, b := derive(t, mine), derive(t, blink.Encode(mine))
	if a.BlobID != b.BlobID || len(a.BlobID) != 32 {
		t.Fatalf("blob ids differ or malformed: %q vs %q", a.BlobID, b.BlobID)
	}
	blob, err := policysync.Seal(a, policy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := policysync.Open(b, blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Version != 7 || !got.Global || len(got.Direct) != 2 || got.Direct[1] != "secret-intranet.example" || got.Proxy[0] != "x.com" {
		t.Fatalf("round trip = %+v", got)
	}
}

// bx server share 给朋友的是另一个 uuid:朋友既找不到你的规则放在哪,拿到了也打不开。
func TestASharedOutLinkCanNeitherFindNorOpenYourRules(t *testing.T) {
	me, them := derive(t, mine), derive(t, friend)
	if me.BlobID == them.BlobID {
		t.Fatal("a friend's link points at your blob")
	}
	blob, _ := policysync.Seal(me, policy)
	if _, err := policysync.Open(them, blob); !errors.Is(err, policysync.ErrWrongKey) {
		t.Fatalf("friend opening your blob: err = %v, want ErrWrongKey", err)
	}
}

// VPS 只见密文:规则一个字都不许以明文出现在 blob 里;篡改一个字节就打不开。
func TestTheBlobHidesTheRulesAndDetectsTampering(t *testing.T) {
	k := derive(t, mine)
	blob, _ := policysync.Seal(k, policy)
	for _, plain := range []string{"apple.com", "secret-intranet", "x.com", "global"} {
		if bytes.Contains(blob, []byte(plain)) {
			t.Fatalf("blob contains %q in the clear", plain)
		}
	}
	again, _ := policysync.Seal(k, policy)
	if bytes.Equal(blob, again) {
		t.Fatal("two seals of the same policy are identical: the nonce is not fresh")
	}
	blob[len(blob)-1] ^= 1
	if _, err := policysync.Open(k, blob); !errors.Is(err, policysync.ErrWrongKey) {
		t.Fatalf("tampered blob: err = %v", err)
	}
	if _, err := policysync.Open(k, []byte("not a blob")); err == nil {
		t.Fatal("garbage opened")
	}
}

// 只有 reality 链接能派生(钥匙取 uuid / pbk / sid);别的说清楚。
func TestDeriveRefusesLinksWithoutRealityCredentials(t *testing.T) {
	for _, l := range []string{"", "brook://server?server=203.0.113.9%3A9999&password=x", "not a link"} {
		if _, err := policysync.Derive(l); err == nil {
			t.Errorf("%q: derived keys", l)
		}
	}
	if _, err := policysync.Derive("  " + mine + "\n"); err != nil {
		t.Errorf("surrounding whitespace should not matter: %v", err)
	}
	if !strings.Contains(policysync.StoreAddr, "127.0.0.1:") {
		t.Fatalf("the store must be loopback-only, got %s", policysync.StoreAddr)
	}
}
