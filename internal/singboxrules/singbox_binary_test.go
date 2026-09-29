package singboxrules_test

import (
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/singboxrules"
)

// 这两条守卫拿**内嵌的那个 sing-box**(手机端跟的就是它这一族)对生成物说话,离线、
// 不联网、不起服务:`check` 验 schema(spec §4.3:跨版本有破坏性变更,翻译得出来 ≠ 它认),
// `rule-set match` 验后缀语义(spec §4.1 的实测,这里自动化)—— 它是 Evaluate 的语义与真
// sing-box 对得上的唯一证据;没有它,Evaluate 只是我对 sing-box 的想象。
// 内嵌为空的构建(windows / 其它 arch)明说跳过:跑不了 ≠ 跑了没过。

func singboxBinary(t *testing.T) string {
	t.Helper()
	raw := embedded.Singbox()
	if len(raw) == 0 {
		t.Skip("this build embeds no sing-box; the binary guards cannot run here")
	}
	path := filepath.Join(t.TempDir(), "sing-box")
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeBundle 把翻译产物摆成 libbox 会看到的样子:一份完整配置 + 两个 rule-set 文件,
// 出站用占位(check 只验 schema,不拨号)。
func writeBundle(t *testing.T, dir string, b singboxrules.Bundle) string {
	t.Helper()
	routeJSON, err := b.RouteJSON()
	if err != nil {
		t.Fatal(err)
	}
	var full map[string]json.RawMessage
	if err := json.Unmarshal(routeJSON, &full); err != nil {
		t.Fatal(err)
	}
	full["log"] = json.RawMessage(`{"level":"error"}`)
	full["outbounds"] = json.RawMessage(`[{"type":"direct","tag":"` + singboxrules.OutboundDirect + `"},` +
		`{"type":"block","tag":"` + singboxrules.OutboundProxy + `"},{"type":"block","tag":"` + singboxrules.OutboundBlock + `"}]`)
	cfgJSON, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, cfgJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range b.Route.RuleSet {
		raw, err := b.RuleSetJSON(ref.Tag)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ref.Path), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath
}

func TestEmbeddedSingboxAcceptsTheTranslatedConfig(t *testing.T) {
	bin := singboxBinary(t)
	cfg, err := config.Parse([]byte(consistencyYAML))
	if err != nil {
		t.Fatal(err)
	}
	lists := singboxrules.Lists{
		ChinaDomain: embeddedLines(t, embedded.ChinaDomain(), "china 域名列表"),
		ChinaCIDR:   embeddedLines(t, embedded.ChinaCIDR(), "china 网段列表"),
	}
	for _, global := range []bool{false, true} {
		cfg.Global = global
		b, err := singboxrules.Translate(cfg, lists)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		cfgPath := writeBundle(t, dir, b)
		cmd := exec.Command(bin, "check", "-c", cfgPath)
		cmd.Dir = dir // rule_set 的 path 是相对的,libbox 也是相对工作目录
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("global=%v: sing-box %s rejects the translated config: %v\n%s", global, singboxrules.TargetSingboxVersion, err, out)
		}
	}
	// 两个 rule-set 文件也要真的被这个 sing-box 读得动(12k 行 domain_suffix、6k 行 ip_cidr):
	// check 不解析 rule_set 引用的文件(实测:引用一个不存在的 tag 也 exit 0),用 rule-set match
	// 逐个文件读一遍,读不动是 FATAL。
	cfg.Global = false
	b, err := singboxrules.Translate(cfg, lists)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeBundle(t, dir, b)
	chinaAddr := ""
	for _, line := range lists.ChinaCIDR {
		if p, err := netip.ParsePrefix(strings.TrimSpace(line)); err == nil {
			chinaAddr = p.Addr().Next().String()
			break
		}
	}
	if chinaAddr == "" {
		t.Fatal("no parsable prefix in the embedded china cidr list")
	}
	for _, probe := range []struct{ tag, input string }{{singboxrules.RuleSetChinaDomain, "www.qq.com"}, {singboxrules.RuleSetChinaCIDR, chinaAddr}} {
		out, err := exec.Command(bin, "rule-set", "match", filepath.Join(dir, probe.tag+".json"), probe.input).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "match rules.[") {
			t.Fatalf("sing-box could not use the %s rule-set for %s: err=%v\n%s", probe.tag, probe.input, err, out)
		}
	}
	// 反向自检:check 实测只验键名与值的写法(空 rule、未知出站、未知 rule_set 引用都 exit 0),
	// 所以这里用一个写错的键名 —— 它必须被拒,否则上面的「接受」什么也证明不了。
	dir = t.TempDir()
	cfgPath := writeBundle(t, dir, b)
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(strings.Replace(string(raw), `"domain_suffix"`, `"domain_sufix"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "check", "-c", cfgPath)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("sing-box check accepted a misspelled rule key; this guard cannot tell good from bad:\n%s", out)
	}
}

// 真 sing-box 对一份 rule-set 的 match 结果,必须与 Evaluate 对同一份 rule-set 的答案逐个相同。
func TestEvaluateAgreesWithEmbeddedSingboxRuleSetMatch(t *testing.T) {
	bin := singboxBinary(t)
	sets := map[string]singboxrules.RuleSetFile{
		"synthetic": {Version: 1, Rules: []singboxrules.Rule{
			{DomainSuffix: []string{"a.com", "zoom.us", "example"}},
			{IPCIDR: []string{"192.0.2.0/24", "2001:db8::/32", "192.0.2.4/32"}},
		}},
	}
	b := singboxrules.Bundle{
		Route:    singboxrules.Route{Rules: []singboxrules.Rule{{RuleSet: []string{"synthetic"}, Outbound: "direct"}}, Final: "proxy"},
		RuleSets: sets,
	}
	dir := t.TempDir()
	raw, err := b.RuleSetJSON("synthetic")
	if err != nil {
		t.Fatal(err)
	}
	rsPath := filepath.Join(dir, "synthetic.json")
	if err := os.WriteFile(rsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	inputs := []string{
		"a.com", "x.a.com", "y.x.a.com", "xa.com", "evila.com", "a.com.evil.net", "com", "example", "sub.example", "myexample",
		"zoom.us", "us04web.zoom.us", "zoom.usa", "A.COM",
		"192.0.2.9", "198.51.100.7", "2001:db8::1", "2001:db9::1", "192.0.2.4", "192.0.2.5",
	}
	agreed, matchedOnce, unmatchedOnce := 0, false, false
	for _, in := range inputs {
		out, err := exec.Command(bin, "rule-set", "match", rsPath, in).CombinedOutput()
		if err != nil {
			t.Fatalf("rule-set match %s: %v\n%s", in, err, out)
		}
		real := strings.Contains(string(out), "match rules.[")
		dst := singboxrules.Destination{Domain: in}
		if addr, err := netip.ParseAddr(in); err == nil {
			dst = singboxrules.Destination{IP: addr}
		}
		model := b.Evaluate(dst).RuleIndex == 0
		if real != model {
			t.Errorf("%q: sing-box %s says matched=%v, Evaluate says %v (%s)", in, singboxrules.TargetSingboxVersion, real, model, strings.TrimSpace(string(out)))
			continue
		}
		agreed++
		matchedOnce = matchedOnce || real
		unmatchedOnce = unmatchedOnce || !real
	}
	if !matchedOnce || !unmatchedOnce {
		t.Fatalf("inputs never produced both outcomes (matched=%v unmatched=%v); a constant model would pass", matchedOnce, unmatchedOnce)
	}
	if agreed != len(inputs) {
		t.Fatalf("%d/%d inputs agreed", agreed, len(inputs))
	}
}
