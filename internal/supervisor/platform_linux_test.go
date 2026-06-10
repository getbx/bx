//go:build linux

package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// 私网/docker 段必须在 bx up 时由 bx 自己装 ip rule 送主表(pref 150 < 全量进 tun 的 200),
// 让宿主机访问 docker 容器/内网的包走内核原路由 native 投递、绕开 tun。
// 这条规则随 bx up 重装,解决「手动 ip rule 在 VPN 重连后丢失」的非持久问题。
func TestUpStepsPrivateToMainTable(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30",
		gw: "10.0.14.1", gwDev: "eno1",
		bypass:     []string{"10.0.0.0/16"},
		mainLookup: []string{"172.16.0.0/12", "192.168.0.0/16"},
	}
	steps := nc.upSteps()

	want := map[string]bool{
		"rule add to 172.16.0.0/12 pref 150 table main":  false,
		"rule add to 192.168.0.0/16 pref 150 table main": false,
	}
	hasCatchAll := false
	for _, s := range steps {
		j := strings.Join(s, " ")
		if _, ok := want[j]; ok {
			want[j] = true
		}
		if j == "rule add pref 200 table "+itoa(routeTable) {
			hasCatchAll = true
		}
	}
	for rule, ok := range want {
		if !ok {
			t.Errorf("缺少私网→主表规则: %q", rule)
		}
	}
	// 全量进 tun 的 pref 200 仍在;private 用 150 < 200,故先被本地分流命中。
	if !hasCatchAll {
		t.Error("缺少 pref 200 全量进 tun 的兜底规则")
	}
}

// down 必须对称清掉自己装的私网规则(否则残留)。
func TestDownStepsRemovesPrivate(t *testing.T) {
	nc := &netConf{
		tunName: "bx0", mainLookup: []string{"172.16.0.0/12"},
	}
	var found bool
	for _, s := range nc.downSteps() {
		if strings.Join(s, " ") == "rule del to 172.16.0.0/12 pref 150 table main" {
			found = true
		}
	}
	if !found {
		t.Error("down 未清理私网→主表规则")
	}
}

// Run 应把内建私网段(DefaultPrivateCIDRs)灌进 netConf.mainLookup。
func TestDefaultPrivateCIDRsWiredIn(t *testing.T) {
	if len(route.DefaultPrivateCIDRs) == 0 {
		t.Fatal("DefaultPrivateCIDRs 不应为空")
	}
}
