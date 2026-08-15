package supervisor

import "testing"

// **热切之后 status 不许再报旧值。**
//
// 真机(2026-08-14):`bx server use` 热切成功、出口确实换了,而 `bx status` 仍显示
//
//	节点 195.133.192.92
//	传输 reality@166.1.190.123  UDP→hysteria2@195.133.192.92
//
// 主传输是实时读 swapper 的,而**节点与 UDP 都是启动时冻结的**。代码注释里就写着
// 「容灾列表/UDP 静态」—— 那在只有自动容灾(只换主传输)时还说得过去,而
// `SetServer` 是**两个槽一起换**,静态标签就变成了谎:它告诉用户流量走的不是
// 它实际走的地方,而这个仓库全部的力气都花在消灭这类谎上。
func TestTransportInfoReportsLiveValuesAfterASwap(t *testing.T) {
	main := &transportSwapper{}
	main.setLink("vless://u@1.1.1.1:443?security=reality")
	udp := &transportSwapper{}
	udp.setLink("hysteria2://p@1.1.1.1:443")

	info := transportInfoFrom(main, udp, nil)
	host, _, udpLabel := info()
	if host == "" || udpLabel == "" {
		t.Fatalf("初始值就不对:%q %q", host, udpLabel)
	}

	// 两个槽都换到第二台。
	main.setLink("vless://u@2.2.2.2:443?security=reality")
	udp.setLink("hysteria2://p@2.2.2.2:443")

	host, _, udpLabel = info()
	if !contains(host, "2.2.2.2") {
		t.Errorf("主传输没跟上:%q", host)
	}
	if !contains(udpLabel, "2.2.2.2") {
		t.Errorf("UDP 标签还停在旧服务器上:%q —— status 会告诉用户一个错的出口", udpLabel)
	}
}

// 没有 UDP 专用传输时标签为空,**不许拿主传输冒充**。
func TestTransportInfoWithoutUDPSwapper(t *testing.T) {
	main := &transportSwapper{}
	main.setLink("vless://u@1.1.1.1:443?security=reality")
	_, _, udpLabel := transportInfoFrom(main, nil, nil)()
	if udpLabel != "" {
		t.Fatalf("没有 UDP 传输却报了 %q", udpLabel)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
