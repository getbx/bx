package mobileconfig_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/mobileconfig"
	"github.com/getbx/bx/internal/singboxrules"
	"github.com/getbx/bx/internal/tunnel"
)

// 手机扩展拿到的就是这一份:内嵌的 sing-box(与手机端 libbox 同一 tag)对它跑 check。
// 出站走真生成器 tunnel.SingboxOutbound;china 列表用内嵌全量。check 只验键名与值写法
// (singboxrules 那边实测过),引用对不对由 build_test.go 的逐键断言管。
func TestEmbeddedSingboxAcceptsTheMobileConfig(t *testing.T) {
	raw := embedded.Singbox()
	if len(raw) == 0 {
		t.Skip("this build embeds no sing-box; the binary guard cannot run here")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	const link = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"
	proxy, err := tunnel.SingboxOutbound(link, "whatever")
	if err != nil {
		t.Fatal(err)
	}
	for _, yaml := range []string{
		"server: brook://example.invalid\nrules:\n  - direct: ['a.com', '192.0.2.4']\n    proxy: ['*.b.com']\n",
		"server: brook://example.invalid\nglobal: true\n",
	} {
		cfg, err := config.Parse([]byte(yaml))
		if err != nil {
			t.Fatal(err)
		}
		lists := singboxrules.Lists{
			ChinaDomain: strings.Split(string(embedded.ChinaDomain()), "\n"),
			ChinaCIDR:   strings.Split(string(embedded.ChinaCIDR()), "\n"),
		}
		f, err := mobileconfig.Build(cfg, lists, proxy)
		if err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		if err := os.WriteFile(filepath.Join(work, "config.json"), f.Config, 0o600); err != nil {
			t.Fatal(err)
		}
		for name, body := range f.RuleSets {
			if err := os.WriteFile(filepath.Join(work, name), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(bin, "check", "-c", "config.json")
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sing-box %s rejects the mobile config: %v\n%s", singboxrules.TargetSingboxVersion, err, out)
		}
	}
}
