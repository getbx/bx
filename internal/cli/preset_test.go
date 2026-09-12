package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

func TestGetPreset(t *testing.T) {
	p, err := getPreset("gaming")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "gaming" || !containsString(p.Direct, "client-update.akamai.steamstatic.com") {
		t.Fatalf("gaming preset = %+v", p)
	}
	if _, err := getPreset("missing"); err == nil {
		t.Fatal("unknown preset should fail")
	}
}

func TestAppPresetsAvoidOpenCloudDomains(t *testing.T) {
	for name, preset := range appPresets {
		for _, domain := range preset.Direct {
			if risk := directRuleRisk(domain); risk != "" {
				t.Fatalf("preset %s contains risky direct domain %q: %s", name, domain, risk)
			}
		}
	}
}

func TestApplyPresetToConfigAddsDirectRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// 对侧那条冲突规则**与 preset 的域名覆盖相等**(写法可以不同)—— 这才是
	// 「加进一边就从另一边删掉」处理得了的形状。更宽的那种(`*.steamstatic.com`
	// 压着一条具体主机)由下面 TestApplyPresetRefusesWhenABroaderProxyRuleWouldSwallowIt
	// 钉住:那时 preset **拒绝**写,而不是写下一条永远不会命中的规则。
	in := `
server: brook://server?server=example.com%3A443&password=pw
rules:
  - proxy:
      - client-update.akamai.steamstatic.com
      - proxy.example.com
  - proxy:
      - "*.steamcontent.com"
`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := applyPresetToConfig(path, appPresets["gaming"])
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected preset to change config")
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	direct := ruleField(cfg, "direct")
	proxy := ruleField(cfg, "proxy")
	for _, domain := range appPresets["gaming"].Direct {
		if !containsString(direct, domain) {
			t.Fatalf("direct rules missing %q: %+v", domain, direct)
		}
		if containsString(proxy, domain) {
			t.Fatalf("preset should remove proxy conflict for %q: %+v", domain, proxy)
		}
	}
	if !containsString(proxy, "proxy.example.com") {
		t.Fatalf("non-conflicting proxy rule should be preserved: %+v", proxy)
	}
}

// **写入路径不许种一条永远不会命中的规则。**
//
// route.Router.Explain 先查 proxy 再查 direct、没有「更具体优先」,所以用户
// 已有 `*.steamstatic.com` 走隧道时,preset 里那条
// `client-update.akamai.steamstatic.com` 直连**一次都不会生效**。此前
// editYAMLRuleList 把 policy 的错误折成 changed=false,于是这里会打印
// 「preset 已经生效,无改动」—— 两句话都不真。
func TestApplyPresetRefusesWhenABroaderProxyRuleWouldSwallowIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := `
server: brook://server?server=example.com%3A443&password=pw
rules:
  - proxy:
      - "*.steamstatic.com"
`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := applyPresetToConfig(path, appPresets["gaming"])
	if err == nil {
		t.Fatalf("被更宽的 proxy 规则压住时应当拒绝,得到 changed=%v", changed)
	}
	if changed {
		t.Fatal("拒绝时不许报 changed=true")
	}
	// 出路必须点名到那一行,否则用户不知道该动哪条规则。
	if !strings.Contains(err.Error(), "*.steamstatic.com") {
		t.Errorf("错误里没点名那条挡路的 proxy 规则:%v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != in {
		t.Errorf("拒绝时动了盘上的文件:\n%s", after)
	}
}

func TestPresetApplySuccessMessageDoesNotClaimRuleCount(t *testing.T) {
	if got, want := presetApplySuccessMessage("gaming"), "✅ preset gaming 已应用。"; got != want {
		t.Fatalf("preset success message = %q, want %q", got, want)
	}
}

func TestApplyPresetToConfigNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := `
server: brook://server?server=example.com%3A443&password=pw
rules:
  - direct:
      - "*.steamcontent.com"
      - steamcdn-a.akamaihd.net
      - client-update.akamai.steamstatic.com
      - "*.steamusercontent.com"
`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := applyPresetToConfig(path, appPresets["gaming"])
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("fully applied preset should be noop")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
