package bxkit_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/singboxout"
	"github.com/getbx/bx/mobile/bxkit"
)

const fakeVless = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"

type configured struct {
	Config     string            `json:"config"`
	RuleSets   map[string]string `json:"rule_sets"`
	ServerHost string            `json:"server_host"`
}

func configure(t *testing.T, link string) configured {
	t.Helper()
	raw, err := bxkit.Configure(link, string(embedded.ChinaDomain()), string(embedded.ChinaCIDR()))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	var c configured
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return c
}

// 手机上粘贴一条链接就要能出一份完整配置:代理出站就是桌面起 sing-box 时的那一个,
// 路由用桌面 `bx setup` 的默认(split:china 直连,其余走隧道)。
func TestConfigureBuildsTheDesktopDefaultForAPastedLink(t *testing.T) {
	for _, link := range []string{fakeVless, blink.Encode(fakeVless), "  " + fakeVless + "\n"} {
		c := configure(t, link)
		if c.ServerHost != "203.0.113.9" {
			t.Fatalf("server_host = %q", c.ServerHost)
		}
		var doc struct {
			Outbounds []map[string]any `json:"outbounds"`
			Route     struct {
				Rules []map[string]any `json:"rules"`
			} `json:"route"`
		}
		if err := json.Unmarshal([]byte(c.Config), &doc); err != nil {
			t.Fatal(err)
		}
		want, _ := singboxout.Outbound(fakeVless, "proxy")
		if doc.Outbounds[0]["server"] != want["server"] || doc.Outbounds[0]["uuid"] != want["uuid"] || doc.Outbounds[0]["tag"] != "proxy" {
			t.Fatalf("proxy outbound = %v, want the desktop generator's", doc.Outbounds[0])
		}
		// split 默认:china 两个 rule-set 被引用(global 时它们不出现)。
		refs := 0
		for _, r := range doc.Route.Rules {
			if rs, ok := r["rule_set"]; ok && rs != nil {
				refs++
			}
		}
		if refs != 2 {
			t.Fatalf("china rule-set references = %d, want 2 (the desktop default is split)", refs)
		}
		if len(c.RuleSets) != 2 || !strings.Contains(c.RuleSets["bx-china-domain.json"], `"domain_suffix"`) {
			t.Fatalf("rule sets = %v", keys(c.RuleSets))
		}
	}
}

// 这一期只有 reality 能在手机上跑;别的链接要说清楚,不许生成一份连不上的配置。
func TestConfigureRefusesLinksThePhoneCannotRunYet(t *testing.T) {
	for _, link := range []string{"brook://server?server=203.0.113.9%3A9999&password=x", "hysteria2://pw@203.0.113.9:443", "not a link", ""} {
		_, err := bxkit.Configure(link, "", "")
		if err == nil {
			t.Errorf("%q: accepted", link)
		}
	}
	_, err := bxkit.Configure("brook://server?server=203.0.113.9%3A9999&password=x", "", "")
	if !errors.Is(err, singboxout.ErrUnsupported) {
		t.Errorf("brook link: err = %v, want ErrUnsupported so the app can say which kinds work", err)
	}
}

// bundle(一条 bx:// 装主传输 + UDP 传输)取第一条手机跑得了的,不因为第二条是 hysteria2 就整条拒。
func TestConfigureTakesTheFirstRunnableLinkOfABundle(t *testing.T) {
	c := configure(t, blink.EncodeMulti([]string{fakeVless, "hysteria2://pw@203.0.113.9:443"}))
	if c.ServerHost != "203.0.113.9" {
		t.Fatalf("server_host = %q", c.ServerHost)
	}
}

// 真 sing-box(与手机端 libbox 同一 tag)认这份配置。
func TestEmbeddedSingboxAcceptsTheConfigurePhoneConfig(t *testing.T) {
	bin := embedded.Singbox()
	if len(bin) == 0 {
		t.Skip("this build embeds no sing-box")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "sing-box")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	c := configure(t, fakeVless)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(c.Config), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range c.RuleSets {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(exe, "check", "-c", "config.json")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sing-box rejects the phone config: %v\n%s", err, out)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
