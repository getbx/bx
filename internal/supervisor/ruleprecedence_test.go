package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
)

// internal/rulereview 的 overriddenFindings 建立在一个关于**别人的实现**的断言上:
// route.Router 先查 UserProxy 再查 UserDirect,中间没有按具体程度排序的步骤。
//
// 那个断言今天是对的。哪天它改成「最长后缀优先」,该转红的是这条测试 ——
// 而不是让用户读到一份说「你这条 direct 规则从来没生效」的报告,然后照着去删掉
// 那条**正在工作的** proxy 规则。
//
// 这里刻意用真的 BuildRouter + 真的 Explain,不复制判定。
func TestProxyRuleBeatsMoreSpecificDirectRule(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{{
			Proxy:  []string{"*.apple.com"},
			Direct: []string{"ocsp.apple.com"},
		}},
	}
	r, err := BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}

	decision, reason := r.Explain(route.Meta{Domain: "ocsp.apple.com"})
	if decision != route.Proxy {
		t.Fatalf("decision = %v, want Proxy —— 判定顺序变了,rulereview.overriddenFindings "+
			"的整个前提(proxy 先查、没有最长后缀优先)已经不成立,必须连同它一起改", decision)
	}
	if reason.Source != route.SourceUserProxy {
		t.Errorf("Source = %v, want SourceUserProxy", reason.Source)
	}
	if reason.Rule != "*.apple.com" {
		t.Errorf("Rule = %q, want %q —— 归因指向的不是压住它的那一条", reason.Rule, "*.apple.com")
	}
}

// 反方向:更宽的 direct 规则**压不住** proxy 规则,后者照常生效。
func TestBroaderDirectRuleDoesNotBeatProxyRule(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{{
			Direct: []string{"*.apple.com"},
			Proxy:  []string{"ocsp.apple.com"},
		}},
	}
	r, err := BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	decision, reason := r.Explain(route.Meta{Domain: "ocsp.apple.com"})
	if decision != route.Proxy {
		t.Fatalf("decision = %v, want Proxy —— 若这里变成 Direct,说明宽 direct 能压住 proxy,"+
			"那 rulereview 必须**反过来**也报一类,否则会漏掉真正失效的规则", decision)
	}
	if reason.Rule != "ocsp.apple.com" {
		t.Errorf("Rule = %q, want %q", reason.Rule, "ocsp.apple.com")
	}
}
