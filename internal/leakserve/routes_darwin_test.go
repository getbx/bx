package leakserve

import "testing"

// **两个地址族都要读。**
//
// 只读 v4 的话,一条抢走全部 v6 流量的隧道一个字都不会被说出来 —— 而 TunnelVision
// 有 v6 版本(RA / DHCPv6 注入路由)。这一条此前住在 exec 那一侧、没有任何测试盯着:
// 删掉 inet6 那一行编译得过、整套测试全绿(变异实测)。
func TestBothAddressFamiliesAreRead(t *testing.T) {
	want := map[string]string{"inet": "0.0.0.0/0", "inet6": "::/0"}
	if len(routeFamilies) != len(want) {
		t.Fatalf("要读的路由表有 %d 张,want %d —— 少一张就是一整个地址族看不见",
			len(routeFamilies), len(want))
	}
	for _, f := range routeFamilies {
		deflt, ok := want[f.flag]
		if !ok {
			t.Errorf("多了一张没登记的表 %q", f.flag)
			continue
		}
		// **`default` 的含义随地址族而变**,归一化必须在读表的地方做 ——
		// 只有读表的人知道自己读的是哪一张,判据层不该去猜。
		if f.deflt != deflt {
			t.Errorf("%s 表里 default 归一化成 %q,want %q", f.flag, f.deflt, deflt)
		}
	}
}

// `default` 必须按表归一化,克隆条目(flags 带 W)必须跳过。
func TestParseDarwinRoutesNormalisesDefaultAndSkipsNeighbours(t *testing.T) {
	got := parseDarwinRoutes(`Internet6:
Destination            Gateway            Flags       Netif
default                fe80::%utun4       UGSc        utun4
fe80::5e:3bb7%en0      6:d3:1e:51:8f:56   UHLWI       en0
::1                    ::1                UHLr        lo0
`, "::/0")

	if len(got) != 2 {
		t.Fatalf("解析出 %d 条,want 2(邻居缓存那条必须跳过):%+v", len(got), got)
	}
	if got[0].Destination != "::/0" {
		t.Errorf("default 没有按 v6 归一化:%q", got[0].Destination)
	}
	for _, e := range got {
		if e.Interface == "en0" {
			t.Errorf("邻居缓存条目被带进来了:%+v", e)
		}
	}
}
