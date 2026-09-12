package supervisor

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeProbeDialer struct {
	addr  string
	delay time.Duration
	err   error
}

func (f *fakeProbeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	f.addr = address
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	client, server := net.Pipe()
	go func() { _ = server.Close() }()
	return client, nil
}

func TestProbeMeasuresTheHandshake(t *testing.T) {
	dialer := &fakeProbeDialer{delay: 12 * time.Millisecond}
	got := probeServer(context.Background(), dialer, ProbeRequest{Host: "203.0.113.10", Port: 443})

	if !got.Reachable {
		t.Fatalf("判成不可达:%+v", got)
	}
	if got.RTTMS < 10 {
		t.Errorf("RTT = %dms,没量到真实耗时", got.RTTMS)
	}
	if dialer.addr != "203.0.113.10:443" {
		t.Errorf("拨到了 %q", dialer.addr)
	}
}

// **「没通」绝不能表达成 RTT=0。** 界面会显示「0 毫秒」,而那是这个仓库反复
// 禁止的那种谎:零值读起来像一切正常。
func TestUnreachableNeverLooksLikeZeroLatency(t *testing.T) {
	got := probeServer(context.Background(),
		&fakeProbeDialer{err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
		ProbeRequest{Host: "203.0.113.10", Port: 443})

	if got.Reachable {
		t.Fatal("拨号失败却判成可达")
	}
	if got.RTTMS != 0 {
		t.Errorf("失败却带了 RTT %dms", got.RTTMS)
	}
	if got.Error == "" {
		t.Error("失败却没说原因")
	}
	if !strings.Contains(got.Error, "拒") {
		t.Errorf("原因没说清楚:%q", got.Error)
	}
}

// 握手快到量不出来时报 1ms,不报 0 —— 同上,0 读起来像「没量」。
func TestFastHandshakeNeverReportsZero(t *testing.T) {
	got := probeServer(context.Background(), &fakeProbeDialer{}, ProbeRequest{Host: "127.0.0.1", Port: 443})
	if !got.Reachable {
		t.Fatal("判成不可达")
	}
	if got.RTTMS < 1 {
		t.Errorf("RTT = %d,快到量不出来时也必须报 1ms", got.RTTMS)
	}
}

// 超时是**最常见**的失败(服务器关着就是不应答),必须说人话而不是把
// context 的错误原样吐出来。
func TestTimeoutSaysNoAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := probeServer(ctx, &fakeProbeDialer{delay: time.Second},
		ProbeRequest{Host: "203.0.113.10", Port: 443})
	if got.Reachable {
		t.Fatal("超时却判成可达")
	}
	if got.Error == "" || strings.Contains(got.Error, "context") {
		t.Errorf("原因不是人话:%q", got.Error)
	}
}

// 原始错误不外传:里面有本机接口名与路由细节,而用户需要的只是那三类之一。
func TestProbeErrorsDoNotLeakInterfaceDetails(t *testing.T) {
	got := probeServer(context.Background(), &fakeProbeDialer{
		err: &net.OpError{
			Op: "dial", Net: "tcp",
			Source: &net.TCPAddr{IP: net.ParseIP("192.168.1.42")},
			Err:    errors.New("bind: can't assign requested address on en0"),
		},
	}, ProbeRequest{Host: "203.0.113.10", Port: 443})

	for _, leak := range []string{"192.168.1.42", "en0", "bind"} {
		if strings.Contains(got.Error, leak) {
			t.Errorf("失败原因里泄漏了 %q:%q", leak, got.Error)
		}
	}
}

// 坏输入当场拒绝,**而且一个包都不发**。
func TestProbeRejectsBadInputWithoutDialing(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  ProbeRequest
	}{
		{"没有主机", ProbeRequest{Port: 443}},
		{"端口越界", ProbeRequest{Host: "203.0.113.10", Port: 70000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialer := &fakeProbeDialer{}
			got := probeServer(context.Background(), dialer, tc.req)
			if got.Reachable {
				t.Fatal("坏输入判成了可达")
			}
			if dialer.addr != "" {
				t.Fatalf("坏输入仍然发了一次拨号:%q", dialer.addr)
			}
		})
	}
}

// 端口留空时按 443 算(bx 的服务器绝大多数在 443),但**必须把用到的端口报回去**,
// 否则用户读到一个「通」而不知道通的是哪个口。
func TestMissingPortDefaultsAndIsReported(t *testing.T) {
	dialer := &fakeProbeDialer{}
	got := probeServer(context.Background(), dialer, ProbeRequest{Host: "203.0.113.10"})
	if got.Port != 443 {
		t.Fatalf("端口 = %d, want 443", got.Port)
	}
	if !strings.HasSuffix(dialer.addr, ":443") {
		t.Fatalf("拨到了 %q", dialer.addr)
	}
}

// **探测必须拿到 DirectDialer,不是随便一个 net.Dialer。**
//
// 这是整条路上唯一一处会**静默**出错的地方:传一个普通拨号器照样编译、照样
// 返回一个漂亮的毫秒数 —— 只不过那个数是「你 → 当前服务器 → 目标」,对
// 「该换哪一台」毫无意义。失败的信号是没有信号,正是这个仓库反复记录的形状。
//
// run.go 是组装根(单测进不去),所以这条守卫读源码。读不懂就响亮失败。
//
// **注意:`serveControlWithPathRecovery` 从位置参数改成了 `controlServeOptions`
// 具名字段之后(组装根瘦身,修复轮 1),原来「按位置数第几个实参」的判据机制
// 本身就不再成立——具名字段没有固定位置。这里改成按字段名 + 字面量匹配,
// 证明的事实一个字没变(ProbeDial 确实接的是 direct、ConfigWarnings 确实接的是
// riskyRuleWarnings(cfg)),变的只是「怎么在源码文本里认出这个事实」这一层
// 不可避免要跟着调用语法一起变。**
//
// **修复轮 2:`readServeControlCallBlock` 必须先剥掉注释再拼块。** 复审实测
// 抓到:把 `ProbeDial: direct,` 整行注释掉、换一行 `ProbeDial: &net.Dialer{},`,
// 旧版(不剥注释、直接在原文上跑正则)照样全绿——`fieldWiredTo` 在文本层面
// 匹配,分不清一行代码和一行长得像代码的注释。这正是「断言被代码自己的
// 解释性注释兜绿」那个形状本身,而且后果是真实的:一个绕回隧道的 `net.Dialer{}`
// 量出来的是「你 → 当前服务器 → 目标」的延迟,界面显示绿、隧道健康,唯独
// 「该不该切换服务器」这个判断彻底失真——这正是这条守卫存在的唯一理由。
func TestProbeIsWiredToTheDirectDialer(t *testing.T) {
	block := readServeControlCallBlock(t)
	// `direct` 就是 plat.DirectDialer() —— 先证明这一点,否则下面那句断言
	// 可能钉在一个同名但无关的变量上。
	if !strings.Contains(block.stripped, "direct := plat.DirectDialer()") {
		t.Fatal("run.go 里 `direct` 不再是 plat.DirectDialer() —— 守卫已经失效,先修守卫")
	}
	if !fieldWiredTo(block.text, "ProbeDial", "direct") {
		t.Fatalf("探测拨号器不是 direct —— 量到的会是「你 → 当前服务器 → 目标」:\n  %s", block.text)
	}
}

// **configWarnings 必须真的到达控制面,否则「危险直连规则」这条常驻告警
// 只在 Run 里算过、从没发布出去。** 同一个理由要求这条独立成守卫,而不是
// 顺带塞进上面那条——两件事各自都可能被漏接,合并断言会让其中一个的
// 失败被另一个的通过掩盖。
func TestProbeConfigWarningsAreWiredToTheControlPlane(t *testing.T) {
	block := readServeControlCallBlock(t)
	if !fieldWiredTo(block.text, "ConfigWarnings", "riskyRuleWarnings(cfg)") {
		t.Fatalf("configWarnings 没有接到 riskyRuleWarnings(cfg):\n  %s", block.text)
	}
}

// **应用流量归因必须接进控制面,否则 GET /v0/apps 恒 501。**
// 这是修复轮 1 的要害缺口本身——Task 8 只加了端点,没有从 Run 把真实的
// *AppTraffic 传进去,结果菜单→Guardian→Core 整条链通到一个恒 501 的端点,
// 而每一层各自的测试都是绿的。run.go 是组装根、单测进不去,只能读源码钉住。
func TestProbeAppTrafficIsWiredToTheControlPlane(t *testing.T) {
	block := readServeControlCallBlock(t)
	if !strings.Contains(block.stripped, "appTraffic := NewAppTraffic(") {
		t.Fatal("run.go 里再找不到 appTraffic := NewAppTraffic(...) —— 守卫已经失效,先修守卫")
	}
	if !fieldWiredTo(block.text, "AppTraffic", "appTraffic") {
		t.Fatalf("controlServeOptions.AppTraffic 没有接到 run.go 构造的 appTraffic —— GET /v0/apps 会恒 501:\n  %s", block.text)
	}
}

// **recoverer 必须真的接进控制面,否则路径恢复端点在生产上悄悄不可用。**
// 位置参数版本里,recoverer 紧挨在 probeDial 前一位,原来那条按位置匹配的
// 字符串顺带把它也钉住了;换成具名字段后这条间接覆盖消失了——今天只有传
// `nil` 会被 Go 的「未使用变量」编译错误挡住,换成**另一个** pathRecoverer
// 是完全静默的。补一条直接断言,成本接近零。
func TestProbeRecovererIsWiredToTheControlPlane(t *testing.T) {
	block := readServeControlCallBlock(t)
	if !fieldWiredTo(block.text, "Recoverer", "recoverer") {
		t.Fatalf("Recoverer 没有接到 run.go 构造的 recoverer:\n  %s", block.text)
	}
}

type serveControlCallBlock struct {
	// stripped 是整份 run.go 剥掉注释之后的文本,供两类断言共用:
	// 「先证明某变量确实是什么」的前置检查,以及从这份文本里摘出
	// controlServeOptions{...} 字面量本身。**必须剥注释**——不剥的话,
	// 一行 `// direct := plat.DirectDialer()` 就能在 `direct` 已经被改指向
	// 别处时仍让 strings.Contains 命中,前置断言形同虚设。
	stripped string
	text     string // 从 serveControlWithPathRecovery( 到其收尾 "})" 的那一段,已剥注释
}

// readServeControlCallBlock 读 run.go、剥掉全部注释(行注释与块注释)、
// 定位 serveControlWithPathRecovery 那次调用,摘出 controlServeOptions{...}
// 结构体字面量的完整文本(从调用起点到第一个独占一行、trim 后等于 "})" 的
// 收尾行)。按行扫描而不是按固定 tab 数的字符串匹配,是为了不被缩进层级的
// 细节绊倒——这段调用套在 requireControlSocket(func(){...}) 的匿名函数里,
// 外层还有一个形状相同的 "})" 收尾,必须停在第一个遇到的那个(结构体+调用
// 的收尾),而不是外层闭包的。
//
// **先剥注释再摘块,不是摘完块再剥注释**:摘块用的是「trim 后等于 "})"」
// 这种对缩进/文本形状敏感的判据,如果块里混着注释,判据本身不受影响
// (注释不会长得像 "})"),但块**里面**的字段匹配(fieldWiredTo)会被注释
// 污染——所以两步顺序其实互不依赖,选「先剥」只是让 stripped 能同时服务于
// 摘块与摘块之外的前置断言,不用剥两遍。
func readServeControlCallBlock(t *testing.T) serveControlCallBlock {
	t.Helper()
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatalf("读不到 run.go:%v", err)
	}
	stripped := stripGoComments(string(source))
	call := strings.Index(stripped, "serveControlWithPathRecovery(")
	if call < 0 {
		t.Fatal("读不出控制面的接线 —— 守卫已经失效,先修守卫")
	}
	lines := strings.Split(stripped[call:], "\n")
	var b strings.Builder
	found := false
	for i, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
		if i > 0 && strings.TrimSpace(line) == "})" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("读不出那次调用的收尾 —— 守卫已经失效,先修守卫")
	}
	return serveControlCallBlock{stripped: stripped, text: b.String()}
}

// stripGoComments 剥掉一段 Go 源码文本里的行注释(`//` 到行尾)与块注释
// (`/* … */`,可能跨行),按字节扫描并对双引号/反引号字符串字面量保持原样
// 跳过(避免把值里恰好含 "//" 或 "/*" 的字符串错误截断)。**两种注释都要
// 处理,漏一种等于没修**——复审的变异分别验证过行注释与块注释各自都能骗过
// 只处理另一种的实现。全部分隔符(`"`、反引号、`/`、`*`、`\`)在 UTF-8 里都是
// 单字节且不会出现在多字节字符的续字节里,按字节扫描对本仓库夹杂中文注释的
// 源码是安全的。
func stripGoComments(src string) string {
	var out strings.Builder
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '"' || c == '`':
			quote := c
			start := i
			i++
			for i < n {
				if quote == '"' && src[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if src[i] == quote {
					i++
					break
				}
				i++
			}
			out.WriteString(src[start:i])
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
			// 不写入被剥掉的内容;`\n`(如果有)留给下一轮循环的 default 分支写入,
			// 保住行结构——fieldWiredTo 的判据要求值后面紧跟 `,` 或换行。
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i < n && !(src[i] == '*' && i+1 < n && src[i+1] == '/') {
				i++
			}
			if i < n {
				i += 2
			}
			out.WriteByte(' ') // 块注释可能紧挨两个 token,留一个空格防误粘连
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// TestStripGoCommentsHandlesBothCommentFormsAndPreservesStrings 直接单测
// stripGoComments 本身,不经过 run.go —— 复审这一轮的教训是「读多行块但不剥
// 注释」,这里独立验证行注释、块注释(含跨行)都被剥掉,而双引号/反引号
// 字符串字面量原样保留(否则未来某个字段的值恰好是含 "//" 的字符串时,
// 会被误当成注释吃掉一部分)。
func TestStripGoCommentsHandlesBothCommentFormsAndPreservesStrings(t *testing.T) {
	src := "A: 1, // 行注释 ProbeDial: fake\n" +
		"B: /* 块注释 ProbeDial: fake */ 2,\n" +
		"C: /* 跨行\n块注释 */ 3,\n" +
		"D: \"a // not a comment /* still not */\",\n"
	got := stripGoComments(src)
	for _, leak := range []string{"行注释", "块注释", "跨行"} {
		if strings.Contains(got, leak) {
			t.Errorf("注释没剥干净,残留 %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, `D: "a // not a comment /* still not */",`) {
		t.Errorf("字符串字面量被误伤:\n%s", got)
	}
	if !fieldWiredTo(got, "A", "1") {
		t.Error("行注释剥完之后,A: 1, 应该能被 fieldWiredTo 找到")
	}
	if !fieldWiredTo(got, "B", "2") {
		t.Error("块注释剥完之后,B: 2, 应该能被 fieldWiredTo 找到")
	}
	if !fieldWiredTo(got, "C", "3") {
		t.Error("跨行块注释剥完之后,C: 3, 应该能被 fieldWiredTo 找到")
	}
}

// fieldWiredTo 判断(已剥注释的)controlServeOptions{...} 字面量里,某个
// 具名字段是否被赋值成给定的字面量表达式(如 "direct"、
// "riskyRuleWarnings(cfg)")。字段名与值之间允许任意空白(gofmt 会按最长
// 字段名对齐冒号后的空格),值后面必须紧跟一个逗号或换行,防止
// "ProbeDial: directX" 这种前缀误配。**调用方必须先经 stripGoComments** ——
// 本函数自己不剥注释,一行 `// ProbeDial: direct,` 会被裸的正则原样匹配上。
func fieldWiredTo(block, field, value string) bool {
	re := regexp.MustCompile(regexp.QuoteMeta(field) + `:\s*` + regexp.QuoteMeta(value) + `\s*[,\n]`)
	return re.MatchString(block)
}

// **码与中文成对,一个都不许漏。**
//
// 码是发给菜单的(它按码出英文),中文是发给 `bx server list` 的。加了一个码而
// 忘了给它一句中文,CLI 上就会静默退回「连不上」—— 那与真的分不出类型长得一模
// 一样,而没有任何东西会报错。菜单那一半由 `internal/cli` 的
// TestProbeErrorCodesAllHaveAnEnglishSentenceInTheMenu 对账,这里守 CLI 那一半。
func TestEveryProbeErrorCodeHasItsOwnChineseSentence(t *testing.T) {
	fallback := ProbeErrorText("no_such_code_at_all")
	if fallback == "" {
		t.Fatal("兜底文案是空的 —— 守卫已经失效,先修守卫")
	}
	for _, code := range ProbeErrorCodes {
		text := ProbeErrorText(code)
		if text == "" {
			t.Errorf("码 %q 没有中文", code)
			continue
		}
		// ProbeErrUnknown 就是「归不了类」,它**本来**就该是那句兜底。
		if code != ProbeErrUnknown && text == fallback {
			t.Errorf("码 %q 退回了兜底文案 %q —— `bx server list` 上它与"+
				"「真的分不出类型」就分不开了", code, text)
		}
	}
}

// **每一个 ProbeErr* 常量都必须登记进 ProbeErrorCodes。**
//
// 少登记一个,它对**两条**守卫都是隐形的(菜单那条对账的是这个数组,CLI 这条
// 遍历的也是它)—— 于是那个码会静默走到菜单的兜底文案上,而那正是这两条守卫
// 存在的理由,只是高了一层。所以这里不查文本、走 AST:声明的常量集合与数组里
// 的取值集合必须**逐个相等**。
//
// 读不出源码时**响亮失败**:一条在最需要它时恰好不可达的断言,与没有这条断言
// 完全一样,而它看起来更让人放心。
func TestEveryProbeErrConstantIsRegisteredInTheList(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", nil, 0)
	if err != nil {
		t.Fatalf("读不出 probe.go:%v —— 守卫已经失效,先修守卫", err)
	}
	declared := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "ProbeErr") || i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s 的值不是一个字符串字面量 —— 守卫已经失效,先修守卫", name.Name)
			}
			declared[name.Name] = value
		}
		return true
	})
	if len(declared) == 0 {
		t.Fatal("一个 ProbeErr* 常量都没扫到 —— 守卫已经失效,先修守卫")
	}

	listed := map[string]bool{}
	for _, code := range ProbeErrorCodes {
		listed[code] = true
	}
	for name, value := range declared {
		if !listed[value] {
			t.Errorf("常量 %s(%q)没有登记进 ProbeErrorCodes —— "+
				"它对两条对账守卫都是隐形的,菜单会静默退回兜底文案", name, value)
		}
	}
	if len(listed) != len(declared) {
		t.Errorf("ProbeErrorCodes 有 %d 个取值,而声明了 %d 个常量 —— "+
			"数组里有一条没有对应的常量(陈旧条目什么也不守)", len(listed), len(declared))
	}
}

// **CLI 上那几句话一个字都没变** —— 这条钉的是**生产那条路**
// (`probeServer` 的两行),不是一个薄壳。
//
// 上一版钉的是那个已删掉的薄壳,而它在拆分之后**零生产调用方**:唯一的引用
// 就是那条测试自己。一个有测试覆盖、没有调用方的函数,读起来与「这里有第二份
// 判据」一模一样,而删掉它照样编译通过。那个薄壳已经删了,断言搬到真拨号那条路上。
func TestProbeServerReportsBothTheCodeAndTheChineseSentence(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
		text string
	}{
		{"超时", context.DeadlineExceeded, ProbeErrTimeout, "超时(没有应答)"},
		{"取消", context.Canceled, ProbeErrCanceled, "已取消"},
		{"DNS", &net.DNSError{Err: "no such host"}, ProbeErrDNS, "域名解析不出来"},
		{"拒绝", &net.OpError{Err: errors.New("connect: connection refused")}, ProbeErrRefused, "连接被拒(端口没在听)"},
		{"网络不可达", &net.OpError{Err: errors.New("connect: network is unreachable")}, ProbeErrNetworkUnreachable, "网络不可达"},
		{"没有路由", &net.OpError{Err: errors.New("connect: no route to host")}, ProbeErrNoRoute, "没有到该主机的路由"},
		{"归不了类", errors.New("something else entirely"), ProbeErrUnknown, "连不上"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := probeServer(context.Background(), &fakeProbeDialer{err: tc.err},
				ProbeRequest{Host: "203.0.113.10", Port: 443})
			if got.Reachable {
				t.Fatal("拨号失败了却报成通了")
			}
			if got.ErrorCode != tc.code {
				t.Errorf("ErrorCode = %q, want %q —— 菜单按码出英文,码错了它就说错话", got.ErrorCode, tc.code)
			}
			if got.Error != tc.text {
				t.Errorf("Error = %q, want %q —— `bx server list` 的措辞变了", got.Error, tc.text)
			}
		})
	}

	// 拨号前就失败的那两条,同样要码与中文成对。
	for _, tc := range []struct {
		name string
		req  ProbeRequest
		code string
	}{
		{"没有主机", ProbeRequest{Host: "  "}, ProbeErrNoHost},
		{"端口不合法", ProbeRequest{Host: "h", Port: 70000}, ProbeErrBadPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := probeServer(context.Background(), &fakeProbeDialer{}, tc.req)
			if got.ErrorCode != tc.code {
				t.Errorf("ErrorCode = %q, want %q", got.ErrorCode, tc.code)
			}
			if got.Error == "" {
				t.Error("没有中文说法")
			}
		})
	}
}
