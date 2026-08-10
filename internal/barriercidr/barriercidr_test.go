package barriercidr

import (
	"strings"
	"testing"
)

// 这八条网段是整个系统里最不能写错的字面量。
//
// 它们是 /2 reject 路由,比 Core 的 /1 split-default 更长、按最长前缀匹配压过一切。
// 写错一个字符有两种后果,都很难看:覆盖不全 = 屏障漏流量;覆盖过头或落在错的段上
// = 整机黑洞,而用户连修复包都下载不了。
//
// 下沉成叶子包之后没有编译器再替这份清单把关(它不再由 barrier.go 就地定义),
// 所以逐字钉住,并断言它们**恰好铺满整个地址空间、互不重叠**。
func TestBlockingBlocksAreExactlyTheFourQuartersOfEachFamily(t *testing.T) {
	v4, v6 := Blocking()
	if got, want := strings.Join(v4, ","), "0.0.0.0/2,64.0.0.0/2,128.0.0.0/2,192.0.0.0/2"; got != want {
		t.Fatalf("IPv4 阻断段被改了:\ngot  %s\nwant %s", got, want)
	}
	if got, want := strings.Join(v6, ","), "::/2,4000::/2,8000::/2,c000::/2"; got != want {
		t.Fatalf("IPv6 阻断段被改了:\ngot  %s\nwant %s", got, want)
	}
	// 四个 /2 恰好是一个地址族的全部;少一个就是屏障有洞。
	if len(v4) != 4 || len(v6) != 4 {
		t.Fatalf("每个地址族必须恰好四个 /2, got v4=%d v6=%d", len(v4), len(v6))
	}
}

// 返回的必须是副本:两个调用方(装屏障的 guardian、问内核的 observe)共用这份清单,
// 任一方就地改动都会让另一方去问错的网段,然后信心十足地报告「屏障不在」。
func TestBlockingReturnsIndependentCopies(t *testing.T) {
	a4, a6 := Blocking()
	a4[0] = "10.0.0.0/8"
	a6[0] = "fd00::/8"
	b4, b6 := Blocking()
	if b4[0] == "10.0.0.0/8" || b6[0] == "fd00::/8" {
		t.Fatal("调用方改动污染了下一次调用 —— 观测与安装会就此看到两份不同的清单")
	}
}
