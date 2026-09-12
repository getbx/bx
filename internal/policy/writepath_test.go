// 这一组守卫钉的是**写入路径**(policy.Apply / policy.Edit,即 bx direct add /
// bx proxy add / bx_apply_policy)的两条性质:
//
//   - 加进一边,对侧那条**覆盖相等**的规则必须消失;
//   - 写不进去一条 bx 的匹配器认不出的规则。
//
// **判定一律拿生产那几份**:config.Parse 读回改后的配置、supervisor.BuildRouter
// 组装真正的分流脑、route.Router.Explain 给出真正的判定。自己在测试里重算一遍
// 「谁盖住谁」,守的就是自己那一份 —— 这个仓库为这个形状栽过六次。
//
// 用外部测试包(policy_test)是为了引得到 supervisor:它经 rulereview 依赖 policy,
// 同包测试里引它会成环。
package policy_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/supervisor"
)

// routerFor 把一份配置字节喂进生产那条组装路径,拿到真正的分流脑。
func routerFor(t *testing.T, raw []byte) *route.Router {
	t.Helper()
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("改后的配置读不回来:%v\n%s", err, raw)
	}
	r, err := supervisor.BuildRouter(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildRouter: %v", err)
	}
	return r
}

// **同一条规则的两种写法必须被当成同一条。**
//
// route.NewDomainSet 去掉 `*.` 只存后缀,于是 `zoom.us` 与 `*.zoom.us` 盖住的
// 东西一模一样。此前对侧删除按**字面串**比对,于是 proxy 里写着 `*.zoom.us`、
// 往 direct 加 `zoom.us` 的结果是两条并存 —— 而 Explain 先查 proxy、没有
// 「更具体优先」,新加的那条 direct **一次都不会命中**,CLI 却打了勾。
func TestAddRemovesTheCoverageEqualRuleFromTheOppositeMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   string
		mode     string
		add      string
		host     string
		want     route.Decision
		wantSrc  route.Source
		stillHas string // 不该被连累的那一条
	}{
		{
			name:     "proxy 写通配,加 direct 裸域",
			config:   "server: brook://x\nrules:\n  - proxy: ['*.zoom.us', keep.example.com]\n",
			mode:     "direct",
			add:      "zoom.us",
			host:     "a.zoom.us",
			want:     route.Direct,
			wantSrc:  route.SourceUserDirect,
			stillHas: "keep.example.com",
		},
		{
			name:     "proxy 写裸域,加 direct 通配",
			config:   "server: brook://x\nrules:\n  - proxy: [zoom.us, keep.example.com]\n",
			mode:     "direct",
			add:      "*.zoom.us",
			host:     "a.zoom.us",
			want:     route.Direct,
			wantSrc:  route.SourceUserDirect,
			stillHas: "keep.example.com",
		},
		{
			name:     "反方向也一样:direct 写通配,加 proxy 裸域",
			config:   "server: brook://x\nrules:\n  - direct: ['*.zoom.us', keep.example.com]\n",
			mode:     "proxy",
			add:      "zoom.us",
			host:     "a.zoom.us",
			want:     route.Proxy,
			wantSrc:  route.SourceUserProxy,
			stillHas: "keep.example.com",
		},
		{
			name:    "大小写与尾点也是同一条",
			config:  "server: brook://x\nrules:\n  - proxy: ['*.Zoom.US.']\n",
			mode:    "direct",
			add:     "zoom.us",
			host:    "a.zoom.us",
			want:    route.Direct,
			wantSrc: route.SourceUserDirect,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := policy.Apply([]byte(tc.config), policy.Request{Mode: tc.mode, Add: []string{tc.add}})
			if err != nil || !changed {
				t.Fatalf("Apply changed=%v err=%v", changed, err)
			}
			dec, reason := routerFor(t, out).Explain(route.Meta{Domain: tc.host})
			if dec != tc.want || reason.Source != tc.wantSrc {
				t.Fatalf("%s 判成 %v(%v),want %v(%v) —— 对侧那条覆盖相等的规则还活着:\n%s",
					tc.host, dec, reason.Source, tc.want, tc.wantSrc, out)
			}
			if tc.stillHas != "" && !strings.Contains(string(out), tc.stillHas) {
				t.Fatalf("连累了无关的规则 %q:\n%s", tc.stillHas, out)
			}
		})
	}
}

// 删除是同一枚硬币的另一面:`bx direct rm example.com` 对一份写着
// `*.example.com` 的配置此前一条都删不掉,而 CLI 如实报「不在列表里」——
// 诚实,但用户找不到那个能删掉它的串。
func TestRemoveMatchesByCoverageNotSpelling(t *testing.T) {
	for _, tc := range []struct{ name, config, remove string }{
		{"删裸域,盘上写的是通配", "server: brook://x\nrules:\n  - direct: ['*.example.com']\n", "example.com"},
		{"删通配,盘上写的是裸域", "server: brook://x\nrules:\n  - direct: [example.com]\n", "*.example.com"},
		// 这次修复之前写进去的畸形规则必须删得掉 —— 一个「删也要先过校验」的
		// 实现会把它们永久锁在配置里。
		{"删掉一条带尾点的死规则", "server: brook://x\nrules:\n  - direct: ['example.com.']\n", "example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := policy.Apply([]byte(tc.config), policy.Request{Mode: "direct", Remove: []string{tc.remove}})
			if err != nil || !changed {
				t.Fatalf("Apply changed=%v err=%v", changed, err)
			}
			if strings.Contains(string(out), "example.com") {
				t.Fatalf("规则没删掉:\n%s", out)
			}
		})
	}
}

// **写进去的东西,生产那份匹配器必须真的匹配得上。**
//
// `example.com.` 是这条守卫的原型:route.DomainSet.Match 去掉**查询**的尾点,
// 却不去掉**模式**的,于是这条规则谁也匹配不上 —— 而 `bx direct ls`、菜单、
// 规则窗口都把它显示成一条正常规则,rulereview 要等 14 天 / 20000 次判定之后
// 才敢说它可能是死的。同一个输入形状这个仓库付过一次学费(`Torchfun.com.` 的
// 静态 A 冲突,两个 Critical),`rules:` 这个面一直没跟上。
//
// 判据刻意写成「**要么拒,要么写进去的那条真的会命中**」而不是「必须拒」:
// 尾点、大小写、两边空白都是意图毫不含糊的输入,归一化掉比打回去好;要紧的是
// 落到盘上的那一行不许是死的。
func TestWhateverThisPathWritesTheMatcherCanActuallyMatch(t *testing.T) {
	for _, tc := range []struct{ add, host string }{
		{"example.com.", "example.com"},
		{"example.com.", "a.example.com"},
		{"*.example.com.", "a.example.com"},
		{"  Example.COM  ", "example.com"},
		{"*.Example.COM", "a.example.com"},
		{"example.com", "example.com"},
	} {
		t.Run(tc.add+"→"+tc.host, func(t *testing.T) {
			out, changed, err := policy.Apply([]byte("server: brook://x\n"),
				policy.Request{Mode: "direct", Add: []string{tc.add}})
			if err != nil {
				return // 拒了也算过关 —— 它没往配置里种一条死规则
			}
			if !changed {
				t.Fatalf("既没拒也没改:%s", out)
			}
			dec, reason := routerFor(t, out).Explain(route.Meta{Domain: tc.host})
			if dec != route.Direct || reason.Source != route.SourceUserDirect {
				t.Fatalf("写进去的规则匹配不上 %s:判成 %v(%v)\n%s", tc.host, dec, reason.Source, out)
			}
		})
	}
}

// 不是域名、也不是网段的东西**在动盘之前**就该被挡掉。此前这条路一个字都不校验,
// 于是一条带换行的「域名」进了 config,要等到下一次 bx up 才会发现 —— 而那时
// 用户已经断过一次网了。
func TestMalformedPatternsAreRefusedBeforeAnythingIsWritten(t *testing.T) {
	const base = "server: brook://x\n"
	for _, bad := range []string{
		"has space.com", "a\nb.com", "*.", "*", "-", "a..b.com", "", "   ",
		"http://example.com", "example.com/path", "user@example.com", "'quoted.com'",
	} {
		t.Run(bad, func(t *testing.T) {
			for _, path := range []struct {
				name string
				fn   func([]byte, policy.Request) ([]byte, bool, error)
			}{{"Apply", policy.Apply}, {"Edit", policy.Edit}} {
				out, changed, err := path.fn([]byte(base), policy.Request{Mode: "direct", Add: []string{bad}, AllowRisk: true})
				if err == nil {
					t.Fatalf("%s 接受了非法规则 %q(changed=%v):\n%s", path.name, bad, changed, out)
				}
				if changed {
					t.Errorf("%s 拒绝时报了 changed=true", path.name)
				}
				if string(out) != base && len(out) != 0 {
					t.Errorf("%s 拒绝时动了配置:\n%s", path.name, out)
				}
			}
		})
	}
}

// **合法输入一个都不许被这次收紧误伤。** IP 字面量与 CIDR 一直是这条路上的
// 正当写法(项目所有者自己的配置里就有,菜单右键对 IP 目的地给的候选也是),
// supervisor.BuildRouter 会把它们分流给 CIDRSet —— 校验器比它守着的那个面窄,
// 拒的就是真实配置。
func TestIPLiteralsAndCIDRsStayWritable(t *testing.T) {
	for _, tc := range []struct {
		add  string
		ip   string
		want route.Source
	}{
		{"180.158.6.185", "180.158.6.185", route.SourceUserDirectIP},
		{"10.84.0.0/16", "10.84.1.2", route.SourceUserDirectIP},
		{"2001:db8::1", "2001:db8::1", route.SourceUserDirectIP},
	} {
		t.Run(tc.add, func(t *testing.T) {
			out, changed, err := policy.Apply([]byte("server: brook://x\n"), policy.Request{Mode: "direct", Add: []string{tc.add}})
			if err != nil || !changed {
				t.Fatalf("Apply changed=%v err=%v", changed, err)
			}
			addr, err := netip.ParseAddr(tc.ip)
			if err != nil {
				t.Fatal(err)
			}
			dec, reason := routerFor(t, out).ExplainIP(addr)
			if dec != route.Direct || reason.Source != tc.want {
				t.Fatalf("%s 判成 %v(%v),want direct(%v):\n%s", tc.ip, dec, reason.Source, tc.want, out)
			}
		})
	}
}

// **一条被更宽的 proxy 规则盖住的 direct 规则写不进去。**
//
// 它不是「多此一举」,是**永远不会命中**:Explain 先查 proxy,没有「更具体优先」。
// 写下去再报成功,就是这个仓库明令禁止的那种谎(rulereview 事后会把它归成
// ClassOverriddenByOppositeKind,但那是体检报告,不是写入时该做的事)。
func TestDirectRuleABroaderProxyRuleWouldSwallowIsRefused(t *testing.T) {
	const in = "server: brook://x\nrules:\n  - proxy: ['*.example.com']\n"
	out, changed, err := policy.Apply([]byte(in), policy.Request{Mode: "direct", Add: []string{"a.example.com"}})
	if err == nil {
		t.Fatalf("写进了一条永远不会命中的规则(changed=%v):\n%s", changed, out)
	}
	if changed {
		t.Error("拒绝时不许报 changed=true")
	}
	// 哨兵要认得出来:MCP 那一侧按它给处置建议,按错误文本认的话措辞一改就
	// 悄悄退回一句通用的废话。
	if !errors.Is(err, policy.ErrCoveredByOppositeMode) {
		t.Errorf("这类拒绝要能被 errors.Is 认出来:%v", err)
	}
	// 出路必须点名到挡路的那一行,并且给一条真敲得动的命令。
	if !strings.Contains(err.Error(), "*.example.com") {
		t.Errorf("错误里没点名挡路的那条规则:%v", err)
	}
	if !strings.Contains(err.Error(), "bx proxy rm") {
		t.Errorf("错误里没给出路:%v", err)
	}
	// 顺手证明它拦的确实是「死规则」这件事:即便硬写进去也不会生效。
	forced := "server: brook://x\nrules:\n  - proxy: ['*.example.com']\n    direct: [a.example.com]\n"
	if dec, reason := routerFor(t, []byte(forced)).Explain(route.Meta{Domain: "a.example.com"}); dec != route.Proxy || reason.Source != route.SourceUserProxy {
		t.Fatalf("前提不成立:硬写进去之后 a.example.com 判成 %v(%v) —— 查找顺序变了,这道门要重判", dec, reason.Source)
	}
}

// **这道门只朝一个方向关。**
//
// proxy 排在 direct 前面,所以 direct 里有 `*.example.com` 时往 proxy 加
// `a.example.com` 是一条**正在生效的例外**(把一小块流量拉回隧道)—— 那是
// 合法且常用的写法。同一张表里加一条更窄的也一样:判定与用户要的一致。
// 把这两种一起拦掉,这道门就变成了「总是挡路」的那一类,而那种门会被绕过或删掉。
func TestNarrowerRulesThatDoTakeEffectAreStillAccepted(t *testing.T) {
	t.Run("proxy 收窄压在更宽的 direct 上", func(t *testing.T) {
		out, changed, err := policy.Apply([]byte("server: brook://x\nrules:\n  - direct: ['*.example.com']\n"),
			policy.Request{Mode: "proxy", Add: []string{"a.example.com"}})
		if err != nil || !changed {
			t.Fatalf("Apply changed=%v err=%v", changed, err)
		}
		r := routerFor(t, out)
		if dec, reason := r.Explain(route.Meta{Domain: "a.example.com"}); dec != route.Proxy || reason.Source != route.SourceUserProxy {
			t.Fatalf("这条例外没生效:%v(%v)\n%s", dec, reason.Source, out)
		}
		if dec, _ := r.Explain(route.Meta{Domain: "b.example.com"}); dec != route.Direct {
			t.Fatalf("更宽的那条 direct 被连累了:%v\n%s", dec, out)
		}
	})
	t.Run("同一张表里加一条更窄的", func(t *testing.T) {
		_, changed, err := policy.Apply([]byte("server: brook://x\nrules:\n  - direct: ['*.example.com']\n"),
			policy.Request{Mode: "direct", Add: []string{"a.example.com"}})
		if err != nil || !changed {
			t.Fatalf("Apply changed=%v err=%v —— 判定与用户要的一致,不该拦", changed, err)
		}
	})
}
