package supervisor

import (
	"os"
	"strings"
	"testing"
)

func TestTailscaleDERPMapBypassCIDRs(t *testing.T) {
	derpMap := []byte(`{
	  "Regions": {
	    "1": {"Nodes": [
	      {"Name": "1a", "IPv4": "203.0.113.10", "IPv6": "2001:db8::10"},
	      {"Name": "1b", "IPv4": "203.0.113.11"}
	    ]},
	    "2": {"Nodes": [
	      {"Name": "2a", "IPv4": "203.0.113.10"}
	    ]}
	  }
	}`)
	got := tailscaleDERPMapBypassCIDRs(derpMap)
	want := []string{"203.0.113.10/32", "203.0.113.11/32"}
	if !sameStringSet(got, want) {
		t.Fatalf("tailscale DERP bypass CIDRs = %v, want %v", got, want)
	}
}

func TestMergeBypassCIDRsDedupes(t *testing.T) {
	got := mergeBypassCIDRs(
		[]string{"23.27.134.77/32", "203.0.113.10/32"},
		[]string{"203.0.113.10/32", "203.0.113.11/32"},
	)
	want := []string{"23.27.134.77/32", "203.0.113.10/32", "203.0.113.11/32"}
	if !sameStringSet(got, want) || len(got) != len(want) {
		t.Fatalf("merged bypass CIDRs = %v, want %v", got, want)
	}
}

func TestTailscaleControlplaneFallbackCIDRs(t *testing.T) {
	got := tailscaleControlplaneFallbackCIDRs()
	for _, want := range []string{"192.200.0.101/32", "192.200.0.116/32"} {
		found := false
		for _, cidr := range got {
			if cidr == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("controlplane fallback CIDRs missing %s: %v", want, got)
		}
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		if !m[v] {
			return false
		}
	}
	return true
}

// **DERP 旁路不该按操作系统门控。**
//
// 它此前在非 darwin 上直接返回 nil,而门上没有任何理由。三个平台的旁路吃的是同一份
// CIDR 列表,而 Linux 上同时跑 bx 与 tailscaled 很常见(VPS、NAS、路由器)——
// 那些机器此前一条 DERP 旁路都没有,Tailscale 只能把中继流量塞进隧道,
// 隧道一挂就连中继都连不上(fail-closed)。
//
// 这条守卫钉的是「判据里不出现 GOOS」。函数本身要出网,所以不在这里调用它;
// 钉源码是因为这正是那种**加回去也不会有任何测试变红**的东西。
func TestTailscaleBypassIsNotGatedByOS(t *testing.T) {
	raw, err := os.ReadFile("tailscale_bypass.go")
	if err != nil {
		t.Fatalf("读不到源码:%v —— 这条守卫失去意义,必须响亮失败", err)
	}
	body := string(raw)
	i := strings.Index(body, "func tailscaleBootstrapBypassCIDRs(")
	if i < 0 {
		t.Fatal("找不到 tailscaleBootstrapBypassCIDRs —— 守卫已读不懂它要守的东西")
	}
	j := strings.Index(body[i:], "\n}\n")
	if j < 0 {
		t.Fatal("找不到函数结尾")
	}
	fnBody := body[i : i+j]
	if strings.Contains(fnBody, "runtime.GOOS") {
		t.Error("DERP 旁路又按操作系统门控了 —— Linux/Windows 上跑 tailscaled 的机器" +
			"会一条中继旁路都没有,而隧道一挂就连中继都连不上")
	}
}
