//go:build linux

package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// **宿主 ↔ 容器的包永不进 TUN。**
//
// docker 的默认地址池 172.16/12 在 route.DefaultPrivateCIDRs 里,由 pref 150 送主表,
// 让内核按 docker0 / br-* 的 on-link 路由原生投递。少了这一条,`docker exec`、
// 端口映射、容器间通信全都会被丢进隧道 —— 而远端服务器根本到不了你的容器。
//
// TUN 自己的地址也刻意避开这一段(198.51.100.1/30,TEST-NET-2),见 run.go。
func TestDockerSubnetsNeverEnterTheTun(t *testing.T) {
	nc := dockerTestNetConf()
	steps := stepSet(nc.routeUpSteps())

	for _, cidr := range []string{"172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"} {
		want := "rule add to " + cidr + " pref 150 table main"
		if !steps[want] {
			t.Errorf("缺少 %q —— 宿主访问容器/内网的包会被丢进隧道,而远端到不了你的容器", want)
		}
	}
}

// **容器出公网的流量会被 bx 接管,而这是靠「pref 200 没有来源限定」成立的。**
//
// 这条规则对**转发**流量一样生效,所以 docker 容器出网自动经隧道 —— 用户白捡一个
// 代理。但它是一条**没有被任何东西声明过的行为**:谁给 pref 200 加上 `iif` 或
// `from` 限定(比如为了「只劫持本机出站」而收紧),容器就会**静默失去代理**,
// 从物理网卡裸奔出去,而没有任何测试会红、没有任何日志会说话。
//
// 这条守卫把那个行为钉成契约。要改它,先改这里,并想清楚容器该怎么办。
func TestContainerEgressIsHijackedBecauseTheCatchAllHasNoSourceFilter(t *testing.T) {
	nc := dockerTestNetConf()
	var catchAll []string
	for _, s := range nc.routeUpSteps() {
		j := strings.Join(s, " ")
		if strings.HasPrefix(j, "rule add pref 200 ") {
			catchAll = s
			break
		}
	}
	if catchAll == nil {
		t.Fatal("找不到 pref 200 的全量规则 —— 这条守卫已经读不懂它要守的东西")
	}
	for _, scoping := range []string{"iif", "oif", "from", "sport", "uidrange"} {
		for _, tok := range catchAll {
			if tok == scoping {
				t.Errorf("pref 200 被 %q 限定了来源:%v —— docker 容器出网会静默失去代理,"+
					"从物理网卡裸奔,而不会有任何测试或日志说话", scoping, catchAll)
			}
		}
	}
}

func dockerTestNetConf() *netConf {
	return &netConf{
		tunName: "bx0", tunAddr: "198.51.100.1/30", gw: "192.168.1.1", gwDev: "eth0",
		bypass: []string{"1.2.3.4/32"}, mainLookup: route.DefaultPrivateCIDRs,
	}
}
