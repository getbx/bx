//go:build darwin

package leakserve

import "testing"

// 解析打在**抓下来的固定文本**上,不跑 scutil:本机正跑着 bx,采集层的测试
// 一次都不许碰真实系统。
func TestParseSCUtilNCList(t *testing.T) {
	out := `Available network connection services in the current set (*=enabled):
* (Disconnected)   6E1B0000-0000-0000-0000-000000000001 PPP (L2TP)  "Old VPN"    [OnDemand]
* (Connected)      A2C40000-0000-0000-0000-000000000002 IPSec       "Work VPN"
  (Invalid)        B3D50000-0000-0000-0000-000000000003 VPN         "Broken VPN"
`
	services := parseSCUtilNCList(out)
	if len(services) != 3 {
		t.Fatalf("应解析出 3 条,得到 %d:%+v", len(services), services)
	}
	connected := 0
	for _, s := range services {
		if s.Connected {
			connected++
			if s.Name != "Work VPN" {
				t.Errorf("已连接的应是 Work VPN,得到 %q", s.Name)
			}
		}
	}
	if connected != 1 {
		t.Fatalf("只有一条是 Connected,得到 %d", connected)
	}
}

// route -n get 对「没有到达该目的地的路由」是报错退出的,而那句报错必须被翻成
// ErrNoRoute —— 「确知没有 v6 通路」是唯一能支撑一句诚实 ok 的输入。
//
// 反过来同样重要:**别的失败一律不许被认成「确知没有」**,否则一台可能正在漏
// v6 的机器会收到一句「没有 IPv6 可漏」。
func TestIsNoRouteErrorSeparatesAbsenceFromFailure(t *testing.T) {
	for _, text := range []string{
		"route: writing to routing socket: no route to host",
		"route: writing to routing socket: not in table",
		"route: writing to routing socket: network is unreachable",
		"host is down",
	} {
		if !isNoRouteError(errString(text)) {
			t.Errorf("%q 是「确知没有这条路由」,应翻成 ErrNoRoute", text)
		}
	}
	for _, text := range []string{
		"context deadline exceeded",
		"fork/exec /sbin/route: permission denied",
		"signal: killed",
		"route -n get: unexpected output",
	} {
		if isNoRouteError(errString(text)) {
			t.Errorf("%q 是「没问出来」,不许被认成「确知没有路由」—— 那会让一台可能"+
				"正在漏 v6 的机器收到一句「没有 IPv6 可漏」", text)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
