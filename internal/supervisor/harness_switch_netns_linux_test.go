//go:build integration && linux

// harness_switch_netns_linux_test.go 钉住换服务器路径上两条 **fail-closed** 性质:
// 目标解析不出来时必须拒绝切换并原地不动;以及 Guardian 屏障的那组开口只跟着
// 当前这台服务器走。
//
// 两条都是关于**进程对外发布的事实**(RuntimeState.ServerBypass)的,而那份事实
// 会经 cli/guardian.go 变成 guardian.BarrierContext.ServerBypass —— 每一次 down /
// update 窗口里,它的每一项都会在 `/2` reject 屏障上被打成一条字面的 permit 路由。
// 混进任何别的东西 = 本项目最强的那条安全性质上的一个洞。
//
// **本文件里的反极性断言(「不得含 X」)一律先证明观测成功再判。** 这类断言在
// 观测本身失败时会静默通过:什么都没找到,于是「没有不该有的东西」,绿灯。
// 故每条都先判 FetchRuntimeState 的 error,再判集合非空 —— 空集合同样满足
// 「不含覆盖 IP」,但它意味着屏障根本没有开口,那是另一个事故,不是通过。
// 观测不到 ≠ 观测到没有,这是 internal/observe 用三态 Tristate 表达过的同一条原则。
package supervisor

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// harnessConfigWithHostOverride = 基准的两台服务器,外加一条用户 `hosts:` 覆盖。
//
// 覆盖的值刻意用**公网** IPv4(TEST-NET-3 里的 203.0.113.77):`hosts:` 接受任意
// IPv4 字面量、不限内网,而屏障开口是在 `/2` reject 上**打洞**。用 10/8 之类写这条
// 断言会失去意义 —— 私网本来就恒直连,混不混进开口都看不出差别。
//
// 域名本身不在任何 link 里,故 mergeHostOverrides **不会**把它当冲突拒掉:它会
// 正常进静态 DNS 表。断言要的正是这个 —— 一条完全合法、确实生效的用户覆盖,
// 它该待在静态表里,而绝不该出现在屏障开口里。
const harnessConfigWithHostOverride = `global: true
current: alpha
servers:
  - name: alpha
    link: vless://u@203.0.113.10:443
    udp: hysteria2://u@203.0.113.11:443
  - name: beta
    link: vless://u@203.0.113.12:443
    udp: hysteria2://u@203.0.113.13:443
hosts:
  public-override.example: 203.0.113.77
`

// postServerSwitchAwaitingAnswer 向 /v0/server 发一次切换请求,并**等到服务端
// 真的答复**,返回状态码与响应体。
//
// 为什么不能直接用 SetServerControl:它背后的 controlHTTPClient 超时是 **3s**,
// 而服务端「切换前刷新 bypass」自己的 deadline 是 **5s**(bypassRefreshTimeout)。
// 目标需要 DNS 时,客户端**必然**先超时 —— 实测这条测试原先拿到的是
// `context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
// 也就是说「err != nil ⇒ 被拒绝了」这个推断根本不成立:一次**成功但慢**的切换
// 会给出一模一样的错误,断言照绿。这正是本任务点名要防的那类假绿,只不过它藏在
// 客户端超时里而不是藏在反极性里。
//
// 故这里自己拿一个长超时的客户端(同包,不动生产代码的默认值),把「服务端说了
// 什么」变成可断言的事实。
func postServerSwitchAwaitingAnswer(t *testing.T, sockPath, link, udp string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"link": link, "udp": udp})
	if err != nil {
		t.Fatalf("编码 /v0/server 请求: %v", err)
	}
	client := controlHTTPClientWithTimeout(sockPath, 30*time.Second)
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost, "http://local/v0/server", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造 /v0/server 请求: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// 这里的失败是**观测失败**,不是「被拒绝」。混为一谈就回到了上面说的那个假绿。
		t.Fatalf("等不到 /v0/server 的答复(观测失败,不能当作「已拒绝」): %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 /v0/server 响应体: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// 断言 3:点名目标解析不出 IP 时必须**拒绝切换**并留在原地。
// 切过去 = bypass 没落实 = 成环。这条是 Task 6 第二轮修复的核心,从没被真机走过。
func TestHarnessRefusesSwitchWhenTargetUnresolvable(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, twoServerHarnessConfig)
	defer h.stop()

	const target = "vless://u@does-not-resolve.invalid:443"

	beforeRT, err := FetchRuntimeState(h.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	// 反极性的前置证明:下面那条 DeepEqual 在「两边都是 nil」时同样成立,
	// 而两边都是 nil 意味着这台机器压根没有 server bypass —— 那本身就是成环,
	// 绝不能当成「原地不动」通过。
	if len(beforeRT.ServerBypass) == 0 {
		t.Fatalf("切换前 ServerBypass 就是空的 —— 没有开口可谈,「原地不动」无从证明")
	}
	// 先走 CLI/MCP 真正用的那条路:它对这次切换必须报错 —— 哪怕原因只是客户端超时。
	// 这条只保证生产客户端不会把一次被拒绝的切换读成成功;它**证明不了**服务端拒绝过
	//(原因见 postServerSwitchAwaitingAnswer 的注释),所以紧接着还要问一次服务端本人。
	// 放在前面是为了让它那个已经超时、却仍在服务端跑着的请求先跑完
	//(下面这一发会在 cs.mu 上排队等它),而不是留一个在飞的 mutation 撞上 teardown。
	if _, err := SetServerControl(h.sockPath, target, ""); err == nil {
		t.Fatal("SetServerControl 对一次被拒绝的切换必须报错")
	}
	code, body := postServerSwitchAwaitingAnswer(t, h.sockPath, target, "")
	if code == http.StatusOK {
		t.Fatalf("解析不出 IP 的目标必须被拒绝 —— 切过去就是在 bypass 没落实的情况下换出口;服务端却答 %d %s", code, body)
	}
	// 「非 200」还不够。实测(把拒绝那一支变异成「只记日志、继续切」)这里拿到的是
	// **409 已有待确认的改动** —— 上一发请求已经把切换 arm 上了,于是一个「切换成功、
	// 只是被后一发撞见」的世界同样能给出非 200。拒绝必须是**这一次**的拒绝,
	// 故认那句只有拒绝分支才会写的话。
	if !strings.Contains(body, "已拒绝切换") {
		t.Fatalf("答复不是「拒绝切换」而是别的什么(409 撞锁?已经 arm 上了?):%d %s", code, body)
	}
	t.Logf("服务端如实拒绝: %d %s", code, body)
	// 「原地不动」的行为学证据:假隧道工厂从没被要求建这条链接。
	// 只比 ServerBypass 是不够的 —— 传输已经换过去、而 bypass 集合恰好没变
	//(拒绝那一支若退化成「记个日志继续切」就正是这个形状)同样能让 DeepEqual 通过。
	if links := h.tunnels.links(); slices.Contains(links, target) {
		t.Fatalf("被拒绝的目标却真的被建成了隧道 —— 传输已经换过去而 bypass 没落实 = 成环;建过的=%v", links)
	}
	afterRT, err := FetchRuntimeState(h.sockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRT.ServerBypass, afterRT.ServerBypass) {
		t.Fatalf("拒绝之后必须原地不动\nbefore=%v\nafter =%v", beforeRT.ServerBypass, afterRT.ServerBypass)
	}
}

// hostOverrideIP 是配置里那条用户 hosts: 覆盖的值 —— 屏障开口里绝不该出现的那个 IP。
const hostOverrideIP = "203.0.113.77"

// assertBarrierCarveOut 观测一次屏障开口,并按「先证明观测成功、再判有无」的顺序断言。
//
// when 只进消息:同一条不变量要在**启动后**与**切换后**各观测一次(为什么见调用处),
// 两次失败必须一眼分得清是哪一次。
func assertBarrierCarveOut(t *testing.T, sockPath, when string, mustContain []string) {
	t.Helper()
	// ① 观测本身必须成功。读不到就是读不到,不能顺势变成「没找到不该有的东西」。
	rt, err := FetchRuntimeState(sockPath)
	if err != nil {
		t.Fatalf("%s:读不到运行期状态,屏障开口无从判断: %v", when, err)
	}
	// ② 空集合平凡地满足「不含覆盖 IP」,但它说的是「屏障一个开口都没有」——
	// 屏障装上时连当前这台服务器都被堵在外面,与本断言要防的洞方向相反、同样是事故。
	// 不先排掉它,下面那条反极性断言就会在观测退化成空的时候安静地报绿。
	if len(rt.ServerBypass) == 0 {
		t.Fatalf("%s:ServerBypass 为空 —— 屏障没有任何开口,反极性断言无从谈起", when)
	}
	got := strings.Join(rt.ServerBypass, ",")
	// ③ 到这里才轮到反极性那一条。
	if strings.Contains(got, hostOverrideIP) {
		t.Errorf("%s:用户 hosts: 覆盖绝不能进屏障开口(它在 /2 reject 上打洞): %s", when, got)
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("%s:%s 必须在屏障开口里,否则屏障把它挡在外面: %s", when, want, got)
		}
	}
}

// 断言 4:屏障开口(RuntimeState.ServerBypass → BarrierContext.ServerBypass)
// 只含当前那台的传输服务器地址 —— 不含用户 hosts: 覆盖、不含上一台。
// 它在 /2 reject 屏障上打洞,混进任何别的东西都是 fail-closed 上的一个口子。
// 这条走过五轮修复、三次翻车,全部只有单元测试背书。
func TestHarnessBarrierCarveOutFollowsCurrentServerOnly(t *testing.T) {
	enterIsolatedNetns(t)
	h := startHarness(t, harnessConfigWithHostOverride) // hosts: public-override.example → 203.0.113.77
	defer h.stop()

	// **启动后先看一次。** 只在切换之后看是不够的:切换会用刷新算出的那份
	// 干净集合**整体替换**掉 store,于是一个只在启动接线上开的洞会被当场抹掉。
	// 实测过 —— 把 run.go 的 wireBypass 复刻回 a8c670f(serverStatics 传成 staticA,
	// 启动时的开口里真的多出 203.0.113.77/32、日志里都印出来了),只看切换之后的
	// 版本**照样 PASS**。而屏障在那个窗口里是真的会被装上的(down / update 随时发生,
	// 不必等谁去切服务器),所以这一眼不是锦上添花。
	assertBarrierCarveOut(t, h.sockPath, "启动后", []string{"203.0.113.10", "203.0.113.11"})

	if _, err := SetServerControl(h.sockPath, "vless://u@198.51.100.20:443", "hysteria2://u@198.51.100.21:443"); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitControl(h.sockPath); err != nil {
		t.Fatal(err)
	}
	assertBarrierCarveOut(t, h.sockPath, "切换后", []string{"198.51.100.20", "198.51.100.21"})
}
