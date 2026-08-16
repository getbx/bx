package route

import (
	"net/netip"
	"testing"
)

func mustEgressSet(t *testing.T, pairs ...[2]string) *EgressSet {
	t.Helper()
	set, err := NewEgressSet(pairs)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// **这是整个功能的落点:具名出口压过「私网恒直连」。**
//
// 用户要开的 10.84.3.239 落在 10/8 里,而那条不变量会把它送去本地网卡 ——
// 那台机器不在这边的局域网,必然失败。只有用户逐条写出来的网段走到这里。
func TestEgressBeatsThePrivateDirectInvariant(t *testing.T) {
	private, err := NewCIDRSet(DefaultPrivateCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	r := &Router{
		PrivateDirect: private,
		UserEgress:    mustEgressSet(t, [2]string{"office", "10.84.0.0/16"}),
	}
	dec, why := r.ExplainIP(netip.MustParseAddr("10.84.3.239"))
	if dec != Via {
		t.Fatalf("判定 = %v, want Via —— 私网不变量把它抢走了", dec)
	}
	if why.Source != SourceUserEgress || why.Rule != "office" {
		t.Fatalf("归因不对:%+v", why)
	}
}

// **白名单:没写进来的私网地址一个字都不受影响。**
//
// 反过来做的后果是 docker、LAN、SSH 全被卷进那条出口 —— 而那正是「私网恒直连」
// 守着的东西。这条守卫钉的就是「只动点名的」。
func TestEgressNeverTouchesUnlistedPrivateAddresses(t *testing.T) {
	private, err := NewCIDRSet(DefaultPrivateCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	r := &Router{
		PrivateDirect: private,
		UserEgress:    mustEgressSet(t, [2]string{"office", "10.84.0.0/16"}),
	}
	for _, addr := range []string{
		"10.0.0.5",     // 同在 10/8,但不在白名单里
		"192.168.1.20", // LAN
		"172.17.0.2",   // docker
		"100.64.0.1",   // CGNAT / Tailscale
	} {
		dec, why := r.ExplainIP(netip.MustParseAddr(addr))
		if dec != Direct {
			t.Errorf("%s 判成了 %v(%s)—— 白名单之外的私网地址不许被动", addr, dec, why.Source)
		}
	}
}

// 出口也压过用户自己的 direct/proxy IP 规则:它是最具体的一条声明。
// (真出现冲突时以出口为准,而配置里两条同时写就是用户自己的问题 ——
// 但行为必须**确定**,不能看谁先谁后。)
func TestEgressTakesPrecedenceOverOtherUserIPRules(t *testing.T) {
	directIP, err := NewCIDRSet([]string{"10.84.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	r := &Router{
		UserDirectIP: directIP,
		UserEgress:   mustEgressSet(t, [2]string{"office", "10.84.0.0/16"}),
	}
	if dec, _ := r.ExplainIP(netip.MustParseAddr("10.84.3.239")); dec != Via {
		t.Fatalf("判定 = %v, want Via", dec)
	}
}

// 顺序即优先级:先写的先匹配,与配置文件的阅读顺序一致。
func TestEgressFirstMatchWins(t *testing.T) {
	set := mustEgressSet(
		t,
		[2]string{"office", "10.84.0.0/16"},
		[2]string{"lab", "10.84.3.0/24"}, // 更具体,但写在后面
	)
	name, ok := set.Lookup(netip.MustParseAddr("10.84.3.239"))
	if !ok || name != "office" {
		t.Fatalf("命中 %q(ok=%v),want office —— 顺序即优先级", name, ok)
	}
}

// v4-in-v6 映射要能匹配上,否则「有时通有时不通」而毫无规律。
func TestEgressMatchesMappedAddresses(t *testing.T) {
	set := mustEgressSet(t, [2]string{"office", "10.84.0.0/16"})
	if _, ok := set.Lookup(netip.MustParseAddr("::ffff:10.84.3.239")); !ok {
		t.Fatal("v4-in-v6 映射没匹配上")
	}
}

// 空集合与 nil 恒不匹配 —— 绝大多数配置不用这个功能,它们一个字节都不该受影响。
func TestEmptyEgressSetNeverMatches(t *testing.T) {
	var nilSet *EgressSet
	if _, ok := nilSet.Lookup(netip.MustParseAddr("10.84.3.239")); ok {
		t.Fatal("nil 集合匹配上了")
	}
	empty := mustEgressSet(t)
	if _, ok := empty.Lookup(netip.MustParseAddr("10.84.3.239")); ok {
		t.Fatal("空集合匹配上了")
	}
	r := &Router{UserEgress: empty}
	if dec, _ := r.ExplainIP(netip.MustParseAddr("10.84.3.239")); dec == Via {
		t.Fatal("空集合却判了 Via")
	}
}

// 坏输入在构造期就拒绝(与 config 那层同一条纪律:悄悄没生效最难查)。
func TestNewEgressSetRejectsBadInput(t *testing.T) {
	if _, err := NewEgressSet([][2]string{{"", "10.84.0.0/16"}}); err == nil {
		t.Error("没有出口名却被接受")
	}
	if _, err := NewEgressSet([][2]string{{"office", "10.84.3.239"}}); err == nil {
		t.Error("不是网段却被接受")
	}
}
