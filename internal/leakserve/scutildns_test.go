package leakserve

import (
	"strings"
	"testing"
)

// realSCUtilDNSOutput 是**真机捕获**的 `scutil --dns` 输出(2026-08-11,项目所有者的
// Mac,bx 开着),只把地址与内网域名替换成文档保留值,结构一个字节没动。
//
// **用真实输出当 fixture 是承重的。** 这个包里另一个解析器的 fixture 是手写的,
// 报告里明说「如果格式猜错了,一切降级成 Unidentified」—— 那是安全方向,但那条
// 「把 utun4 翻成人话」的价值就没了。手写 fixture 只能证明解析器认得自己想象中的
// 格式;它认不认得真的那份,只有真的那份能证明。
const realSCUtilDNSOutput = `DNS configuration

resolver #1
  search domain[0] : example.internal
  nameserver[0] : 127.0.0.1
  flags    : Request A records, Request AAAA records
  reach    : 0x00030002 (Reachable,Local Address,Directly Reachable Address)

resolver #2
  domain   : local
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300000

resolver #3
  domain   : 254.169.in-addr.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300200

resolver #4
  domain   : 8.e.f.ip6.arpa
  options  : mdns
  timeout  : 5
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)
  order    : 300400

DNS configuration (for scoped queries)

resolver #1
  search domain[0] : example.internal
  nameserver[0] : 198.51.100.53
  if_index : 14 (en0)
  flags    : Scoped, Request A records
`

func TestParseSCUtilDNSTakesOnlyTheDefaultResolver(t *testing.T) {
	got := parseSCUtilDNS(realSCUtilDNSOutput)
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("默认解析器 = %v, want [127.0.0.1]", got)
	}
}

// 域名限定的块(mDNS、反向 DNS 区)不回答「我的 DNS 归谁」。
// 把它们算进来,一台正常机器会凭空多出几个「解析器」,而 DNS 那条规则会据此判断。
func TestParseSCUtilDNSSkipsDomainScopedResolvers(t *testing.T) {
	in := `DNS configuration

resolver #1
  domain   : local
  nameserver[0] : 224.0.0.251
  options  : mdns
`
	if got := parseSCUtilDNS(in); len(got) != 0 {
		t.Fatalf("域名限定的解析器不该算数, got %v", got)
	}
}

// `(for scoped queries)` 是按接口分的解析器,同样不回答默认解析是谁。
// **这一条单独测**:真机输出里它就跟在后面,漏掉这个 break 会把接口解析器混进来。
func TestParseSCUtilDNSStopsAtTheScopedSection(t *testing.T) {
	got := parseSCUtilDNS(realSCUtilDNSOutput)
	for _, s := range got {
		if s == "198.51.100.53" {
			t.Fatalf("按接口分的解析器被算进来了, got %v", got)
		}
	}
}

// 一个默认解析器块里可以有多个 nameserver,而且要按出现顺序、去重。
func TestParseSCUtilDNSKeepsEveryNameserverInOrder(t *testing.T) {
	in := `DNS configuration

resolver #1
  nameserver[0] : 203.0.113.1
  nameserver[1] : 203.0.113.2
  nameserver[2] : 203.0.113.1
`
	got := parseSCUtilDNS(in)
	if len(got) != 2 || got[0] != "203.0.113.1" || got[1] != "203.0.113.2" {
		t.Fatalf("解析器 = %v, want [203.0.113.1 203.0.113.2](按序去重)", got)
	}
}

// **读不出来必须返回空,不许编。**
//
// 这条规则的全部价值建立在「问了,答案是坏的」与「没问出来」分得开;
// 编一个默认值会把整条规则变成谎话 —— 而这正是本仓库反复栽的那一类。
func TestParseSCUtilDNSReturnsNothingWhenItCannotRead(t *testing.T) {
	for _, name := range []string{"空输出", "命令不存在的报错", "格式全变了"} {
		var in string
		switch name {
		case "命令不存在的报错":
			in = "scutil: command not found\n"
		case "格式全变了":
			in = "{\n  \"resolvers\": [{\"nameserver\": \"203.0.113.1\"}]\n}\n"
		}
		if got := parseSCUtilDNS(in); len(got) != 0 {
			t.Errorf("%s:应当什么都答不出来, got %v", name, got)
		}
	}
}

// 值里带空格(`flags : Request A records, Request AAAA records`)不能把行拆坏,
// 键里带下标(`nameserver[0]`)也要认得出来 —— 只按第一个冒号拆。
func TestSplitSCUtilFieldHandlesIndexedKeysAndSpacedValues(t *testing.T) {
	key, value, ok := splitSCUtilField("  flags    : Request A records, Request AAAA records")
	if !ok || key != "flags" || value != "Request A records, Request AAAA records" {
		t.Fatalf("= (%q, %q, %v)", key, value, ok)
	}
	key, value, ok = splitSCUtilField("  nameserver[0] : 203.0.113.1")
	if !ok || key != "nameserver[0]" || value != "203.0.113.1" {
		t.Fatalf("= (%q, %q, %v)", key, value, ok)
	}
	if _, _, ok := splitSCUtilField("DNS configuration"); ok {
		t.Error("没有冒号的行不该被当成字段")
	}
}

// 真机 fixture 必须保持真实:有人「顺手整理」掉那个 scoped 段或那些 mdns 块,
// 上面几条测试就只在想象中的格式上成立了。
func TestRealSCUtilFixtureKeepsTheShapesThatMatter(t *testing.T) {
	for _, must := range []string{
		"DNS configuration (for scoped queries)", // 漏了它,break 那条就没被测
		"options  : mdns",                        // 漏了它,域名限定那条就没被测
		"search domain[0]",                       // 带下标的键
		"reach    : 0x00030002",                  // 值里带空格与括号
	} {
		if !strings.Contains(realSCUtilDNSOutput, must) {
			t.Errorf("真机 fixture 里少了 %q —— 它是从真实输出捕获的,别按想象整理", must)
		}
	}
}
