package supervisor

import (
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/config"
	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/splitdns"
	"golang.org/x/net/dns/dnsmessage"
)

// 本文件端到端验证 mergeHostOverrides 的输出经真实 dns.Server.SetStaticA/Respond
// 之后的行为——不是靠推断"merged 长得对"就够了,复审那次抓到的 bug(大小写/尾点
// 不归一导致冲突判定失效)正是在这一层才会现出原形:merged 本身可以两个 key
// 都"看起来对",但 SetStaticA 会把它们各自归一再 append 进同一个桶,一次查询
// 吐出两条 A 记录才是真正的故障现象。

func buildAQuery(t *testing.T, name string) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 1, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	buf, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return buf
}

// allA 返回应答里全部 A 记录(而不只是第一条)——判定"有没有静默多出一条"
// 必须数出全部,只看第一条会把这个 bug 又测漏一次。
func allA(t *testing.T, msg []byte) []netip.Addr {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		t.Fatal(err)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatal(err)
	}
	var out []netip.Addr
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			break
		}
		if h.Type == dnsmessage.TypeA {
			a, err := p.AResource()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, netip.AddrFrom4(a.A))
			continue
		}
		if err := p.SkipAnswer(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func newTestDNSServer(t *testing.T, staticA map[string][]netip.Addr) *bxdns.Server {
	t.Helper()
	pool, err := fakeip.New("198.18.0.0/15")
	if err != nil {
		t.Fatalf("fakeip.New: %v", err)
	}
	s := bxdns.NewServer(pool, 1)
	s.SetStaticA(staticA, splitdns.NewSet())
	return s
}

// 复审复现的真实攻击面:server link 的 host 带尾点(net/url.Hostname() 原样
// 保留),用户配了同一域名的干净写法。冲突判定若只补了大小写、漏了去尾点
// (fix round 1 的状态),这里会在真实 dns.Server 上吐出 2 条 A 记录——服务器
// 真 IP 和用户覆盖 IP 都在,客户端选哪条全凭运气,bx status 完全看不出分歧。
func TestHostOverridesRealDNSServerTrailingDotConflictYieldsSingleServerRecord(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com.": {netip.MustParseAddr("203.0.113.20")}}
	user := map[string]netip.Addr{"vps.example.com": netip.MustParseAddr("127.0.0.1")}

	merged, _, ignored := mergeHostOverrides(server, user)
	if len(ignored) != 1 || ignored[0] != "vps.example.com" {
		t.Fatalf("必须报出被忽略的域名,实际 ignored=%v", ignored)
	}

	s := newTestDNSServer(t, merged)
	resp, err := s.Respond(buildAQuery(t, "vps.example.com."))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	got := allA(t, resp)
	if len(got) != 1 || got[0].String() != "203.0.113.20" {
		t.Fatalf("必须只应答服务器真 IP,实际 %v —— 混进用户覆盖会让隧道连去哪个全凭客户端运气", got)
	}
}

// 反向确认:mergeHostOverrides 的修复不能以牺牲普通解析为代价。混合大小写或带
// 尾点的服务器域名,在**没有**用户覆盖时,经真实 dns.Server 仍必须正常解析到
// 服务器真 IP——一个只顾着堵住冲突、却把正常查询也堵死的"修复"比原来的 bug 更糟。
func TestHostOverridesRealDNSServerNormalResolutionUnaffected(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverHost string
		query      string
	}{
		{"大小写", "VPS.example.com", "vps.example.com."},
		{"尾点", "vps.example.com.", "vps.example.com."},
		{"大小写+尾点", "VPS.Example.Com.", "vps.example.com."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := map[string][]netip.Addr{tc.serverHost: {netip.MustParseAddr("203.0.113.20")}}
			merged, applied, ignored := mergeHostOverrides(server, nil)
			if len(ignored) != 0 || len(applied) != 0 {
				t.Fatalf("没有用户覆盖时不该有 ignored/applied:ignored=%v applied=%v", ignored, applied)
			}

			s := newTestDNSServer(t, merged)
			resp, err := s.Respond(buildAQuery(t, tc.query))
			if err != nil {
				t.Fatalf("Respond: %v", err)
			}
			got := allA(t, resp)
			if len(got) != 1 || got[0].String() != "203.0.113.20" {
				t.Fatalf("正常解析被修复连累了:%v", got)
			}
		})
	}
}

// 这条测试只验证行为:对三重尾点 + 首尾空白 + 大小写混杂这一个精心构造的输入,
// mergeHostOverrides 判出的结果要跟直接调 config.NormalizeHostName 算出来的
// 一致。它能抓住"归一化规则实现得不完整"(比如只 TrimSuffix 一次、或漏了
// TrimSpace),**但抓不住"重新实现"本身**——复审证实了这一点:把这里换成一份
// 逐字节行为相同的手写 normalize 函数,本测试和其余测试全部照样通过,因为它
// 只断言输出、不关心输出是怎么算出来的。两份实现只要在测过的输入上凑巧一致,
// 就能滑过这条测试——而这正是本轮要修的那类问题的本质:分歧不是「测漏了某个
// 输入」,是「压根有两份实现」。真正堵住"重新实现"这件事的是下面那条读源码的
// TestHostsMergeUsesSharedNormalizerNotALocalReimplementation;这条测试仍然
// 保留,因为"行为是否正确"和"是否共用同一份代码"是两件事,值得分别断言。
func TestMergeHostOverridesNormalizationMatchesConfigExactly(t *testing.T) {
	serverKey := "  VPS.Example.COM...  "
	// 先验证测试数据本身站得住:config.NormalizeHostName 确实会把这个畸形写法
	// 归一成跟用户那条一样的 "vps.example.com",这条用例才有意义。
	if got := config.NormalizeHostName(serverKey); got != "vps.example.com" {
		t.Fatalf("测试前提不成立:config.NormalizeHostName(%q) = %q, want vps.example.com", serverKey, got)
	}

	server := map[string][]netip.Addr{serverKey: {netip.MustParseAddr("203.0.113.20")}}
	user := map[string]netip.Addr{"vps.example.com": netip.MustParseAddr("127.0.0.1")}

	_, applied, ignored := mergeHostOverrides(server, user)
	if len(ignored) != 1 || ignored[0] != "vps.example.com" {
		t.Fatalf("必须识别出与服务器域名冲突,实际 ignored=%v", ignored)
	}
	if len(applied) != 0 {
		t.Fatalf("被忽略的条目不得出现在 applied 里,实际 %v", applied)
	}
}
