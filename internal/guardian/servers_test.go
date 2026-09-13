package guardian

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
)

// 两台服务器,链接里带一个**看得出来的凭据**(uuid)—— 下面有一条守卫专门
// 检查它不会被发到菜单那一侧去。
const serversTestUUID = "11111111-2222-3333-4444-555555555555"

func serversTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "servers:\n" +
		"    - name: tokyo\n" +
		"      link: vless://" + serversTestUUID + "@203.0.113.10:443?security=reality\n" +
		"    - name: osaka\n" +
		"      link: vless://" + serversTestUUID + "@203.0.113.20:443?security=reality\n" +
		"      udp: hysteria2://pw@203.0.113.21:443\n" +
		"current: tokyo\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noSwitch(t *testing.T) serverSwitcher {
	t.Helper()
	return func(name, link, udp string) error {
		t.Fatalf("不该热切,却切了:%s", name)
		return nil
	}
}

// 与 /v1/up、/v1/rules 同一道门。换服务器会改变出口 IP,是同一量级的动作。
func TestServersEndpointRequiresOwnerOrRoot(t *testing.T) {
	handler := serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, nil)
	for _, tc := range []struct {
		name string
		uid  uint32
		got  bool
		want int
	}{
		{"owner", 501, true, http.StatusOK},
		{"root", 0, true, http.StatusOK},
		{"别的用户", 502, true, http.StatusForbidden},
		{"拿不到对端凭据", 0, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), tc.uid, tc.got))
			if w.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// ownerUID 未配置时退化成 root-only —— 绝不因为「没配」就放宽。
func TestServersEndpointStaysRootOnlyWithoutOwner(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 0, noSwitch(t), nil, nil)(
		w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
	)
	if w.Code != http.StatusForbidden {
		t.Fatalf("未配置 owner 时非 root 拿到了 %d", w.Code)
	}
}

// **链接是凭据,绝不发给菜单。**
//
// 菜单要显示的是「流量从哪出去」,那是主机名;把整条带 uuid / 密码的链接送进一个
// uid 501 的进程里,只为渲染一行字,是白送出去的攻击面。这条守卫钉的是**响应体
// 的完整字节**,不是某个字段 —— 加一个 `link` 字段不会有编译错误。
func TestServerListNeverShipsTheLinkItself(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, nil)(
		w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
	)

	body := w.Body.String()
	if strings.Contains(body, serversTestUUID) {
		t.Fatalf("响应体里出现了链接凭据:%s", body)
	}
	for _, scheme := range []string{"vless://", "hysteria2://", "bx://", "brook://"} {
		if strings.Contains(body, scheme) {
			t.Fatalf("响应体里出现了 %s 链接:%s", scheme, body)
		}
	}
	// 反面:主机确实发出去了,否则上面那条可以靠「什么都不发」满足。
	for _, host := range []string{"203.0.113.10", "203.0.113.20", "203.0.113.21"} {
		if !strings.Contains(body, host) {
			t.Errorf("主机 %s 没发出去,菜单显示不出流量从哪走:%s", host, body)
		}
	}
}

func TestServerListMarksTheCurrentOne(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, nil)(
		w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
	)

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 2 {
		t.Fatalf("清单长度 = %d, want 2", len(got.Servers))
	}
	if got.Current != "tokyo" || !got.Servers[0].Current || got.Servers[1].Current {
		t.Fatalf("当前那台标错了:%+v", got)
	}
	if got.Servers[1].UDPHost != "203.0.113.21" {
		t.Errorf("UDP 出口没单独标出来(%q)—— 少了它 UDP 会静默走另一台",
			got.Servers[1].UDPHost)
	}
}

// **先写配置,再热切。** 反过来的话,热切成功而写盘失败会留下「现在在 B、
// 下次启动回 A」—— 一个没人看得出来的不一致。
func TestServerSwitchWritesConfigBeforeHotSwitching(t *testing.T) {
	path := serversTestConfig(t)
	var currentWhenSwitched string
	handler := serversHandler(path, 501, func(name, link, udp string) error {
		_, currentWhenSwitched, _ = setup.ListServers(path)
		return nil
	}, nil, nil)
	w := httptest.NewRecorder()
	handler(w, withPeer(postServers(t, "osaka"), 501, true))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	if currentWhenSwitched != "osaka" {
		t.Fatalf("热切发生时盘上 current = %q,want osaka —— 顺序反了", currentWhenSwitched)
	}
	var got switchResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !got.Applied || got.Host != "203.0.113.20" {
		t.Fatalf("应答不对:%+v", got)
	}
}

// **热切给 Core 的必须是解过壳的链接。** 配置里存的可能是 bx:// 换壳链接,
// Core 只认内层;喂换壳的进去它解析不出主机、装不了 bypass,于是正确地拒绝切换,
// 而用户看到的是一句关于 base64 的错误。
func TestServerSwitchHandsCoreTheDecodedLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	inner := "vless://" + serversTestUUID + "@203.0.113.30:443?security=reality"
	wrapped := wrapLinkForTest(t, inner)
	body := "servers:\n    - name: a\n      link: " + wrapped + "\n    - name: b\n      link: " + wrapped + "\ncurrent: a\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var handed string
	handler := serversHandler(path, 501, func(name, link, udp string) error {
		handed = link
		return nil
	}, nil, nil)
	handler(httptest.NewRecorder(), withPeer(postServers(t, "b"), 501, true))
	if handed != inner {
		t.Fatalf("交给 Core 的是 %q,want 解过壳的 %q", handed, inner)
	}
}

// 热切失败时**不许说成功**:配置写好了,但正在跑的实例还在旧服务器上。
// 合成一个 ok 会让菜单说「已切换」而流量还从原来那台出去。
func TestServerSwitchReportsWhenOnlyTheConfigChanged(t *testing.T) {
	path := serversTestConfig(t)
	handler := serversHandler(path, 501, func(name, link, udp string) error {
		return errTestHotSwitch
	}, nil, nil)
	w := httptest.NewRecorder()
	handler(w, withPeer(postServers(t, "osaka"), 501, true))

	// **200 而不是 500**:配置确实写成功了,这次请求不是白做的。
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", w.Code)
	}
	var got switchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Applied {
		t.Fatal("热切失败却报 applied=true —— 菜单会说「已切换」而流量还从旧那台出去")
	}
	// 原始错误串不外传(可能含路径 / 链接),只带一个失败类别。
	if strings.Contains(w.Body.String(), errTestHotSwitch.Error()) {
		t.Errorf("原始错误串泄漏进了响应体:%s", w.Body.String())
	}
	if _, current, _ := setup.ListServers(path); current != "osaka" {
		t.Errorf("配置没写成 osaka(=%q)—— 那句「重启即可用上」就成了假话", current)
	}
}

// 名字不在清单里:不写、不切、明确拒绝。静默接受会让用户以为切过去了。
func TestServerSwitchRejectsUnknownName(t *testing.T) {
	path := serversTestConfig(t)
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServers(t, "nagoya"), 501, true))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400", w.Code)
	}
	if _, current, _ := setup.ListServers(path); current != "tokyo" {
		t.Fatalf("current 被改成了 %q", current)
	}
}

// 没接线时说「没接线」,不说「没有服务器」—— 后者会让菜单显示一个空清单,
// 用户据此以为自己没配过服务器(与 /v1/rules 同一条纪律)。
func TestServersEndpointReportsWhenNotWired(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler("", 501, noSwitch(t), nil, nil)(
		w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
	)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("状态码 = %d, want 501", w.Code)
	}
}

func postServers(t *testing.T, name string) *http.Request {
	t.Helper()
	body, err := json.Marshal(serversRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/v1/servers", strings.NewReader(string(body)))
}

func wrapLinkForTest(t *testing.T, link string) string {
	t.Helper()
	return blink.Encode(link)
}

var errTestHotSwitch = &testErr{"dial /run/bx/core.sock: connection refused"}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

// 菜单靠能力声明决定要不要画服务器入口。少了它,老 Guardian 上会画出一个
// 每次点都失败的按钮。
func TestServersCapabilityIsDeclared(t *testing.T) {
	for _, c := range GuardianCapabilities() {
		if c == CapabilityServers {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q:%v", CapabilityServers, GuardianCapabilities())
}

// **加一台不许把出口换过去。** 部署完一台新 VPS 不构成「换到那里」的请求;
// 换出口要用户在清单里显式点一下。这条守卫钉的正是那个副作用。
func TestServerAddDoesNotChangeTheCurrentExit(t *testing.T) {
	path := serversTestConfig(t)
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "add", Name: "nagoya",
		Link: "vless://" + serversTestUUID + "@203.0.113.30:443?security=reality",
	}), 501, true))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	list, current, err := setup.ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if current != "tokyo" {
		t.Fatalf("加一台之后 current 变成了 %q —— 用户的出口被偷偷换掉了", current)
	}
	if len(list) != 3 {
		t.Fatalf("清单长度 = %d, want 3", len(list))
	}
	// 应答回的是**改动之后的完整清单**,界面据此重画,不必自己推演。
	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 3 || got.Current != "tokyo" {
		t.Fatalf("应答不是改动后的完整清单:%+v", got)
	}
	// 加进来的链接同样不许回传给菜单 —— 它是凭据。
	if strings.Contains(w.Body.String(), serversTestUUID) {
		t.Errorf("应答里出现了链接凭据:%s", w.Body.String())
	}
}

// 加一台**绝不热切**:noSwitch 会在被调用时 t.Fatal。这条断言由那个替身承担,
// 单独写出来是为了让意图可读 —— 热切是「换过去」的一部分,不是「加进来」的。
func TestServerAddNeverHotSwitches(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "add", Name: "nagoya", Link: "vless://x@203.0.113.30:443",
	}), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
}

// 坏输入在写盘之前挡掉,并且**如实报错** —— 静默成功会让界面显示「已添加」而
// 配置一个字节没变。
//
// **省略名字本身不再是坏输入**(Task 6 起按链接推导,见
// TestAddServerDerivesTheNameWhenOmitted);这里改为「名字与链接都推不出来」——
// 一个连主机都解不出的 link,推导必然失败。
func TestServerAddRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  serversRequest
	}{
		{"名字省略且链接推不出主机", serversRequest{Action: "add", Link: "vless://"}},
		{"没有链接", serversRequest{Action: "add", Name: "nagoya"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := serversTestConfig(t)
			before, _ := os.ReadFile(path)
			w := httptest.NewRecorder()
			serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, tc.req), 501, true))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, want 400", w.Code)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("被拒绝的请求改动了盘上的配置")
			}
		})
	}
}

func postServersJSON(t *testing.T, req serversRequest) *http.Request {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/v1/servers", strings.NewReader(string(body)))
}

// 「没测成」与「测了不通」在线上必须分得开。今天两者都是 Reachable:false,
// 于是菜单把一台好服务器画成红的 —— 而 ServersModel 里那段注释明写不该这样。
func TestProbeDistinguishesNotMeasuredFromUnreachable(t *testing.T) {
	notMeasured := ProbeReport{Measured: false, Error: "core not running"}
	unreachable := ProbeReport{Measured: true, Reachable: false}
	if notMeasured.Reachable == unreachable.Reachable && notMeasured.Measured == unreachable.Measured {
		t.Fatal("两种结局在线上无法区分")
	}
	// measured 缺席读作「这一版 Guardian 没说」,所以它不许带 omitempty。
	b, err := json.Marshal(ProbeReport{Measured: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"measured"`) {
		t.Fatalf("measured 带了 omitempty:%s —— 键缺席就与旧 Guardian 无法区分了", b)
	}
}

// **「没能测」不是「不可达」。**
//
// Core 不在跑、或这一版不支持探测,都会让 probe 报错。把它判成不可达会**把一台
// 好服务器标成红的**,而用户据此去换服务器 —— 与 ipify 那次同一类、方向相反的错。
func TestProbeFailureIsNotReportedAsUnreachable(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t),
		func(host string, port int) (supervisor.ProbeResult, error) {
			return supervisor.ProbeResult{}, errTestHotSwitch
		}, nil)(w, withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true))

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, entry := range got.Servers {
		if entry.Probe == nil {
			t.Fatalf("%s 没有结论 —— 键缺席读作「没测过」,而我们确实测了", entry.Name)
		}
		if entry.Probe.Measured {
			t.Errorf("%s 没测成却标记为 Measured:true —— 与名字承诺的区分矛盾", entry.Name)
		}
		if entry.Probe.Error == "" {
			t.Errorf("%s 没说为什么没测成", entry.Name)
		}
		if entry.Probe.RTTMS != 0 {
			t.Errorf("%s 没测成却带了 RTT", entry.Name)
		}
	}
	// 原始错误串不外传。
	if strings.Contains(w.Body.String(), errTestHotSwitch.Error()) {
		t.Errorf("原始错误串泄漏进了响应体:%s", w.Body.String())
	}
}

// 一台失败不影响其余 —— 与 internal/observe「任一项观测失败即记为 Unknown 并
// 附原因,绝不中断其余项」同源。
func TestOneServerFailingDoesNotSinkTheRest(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t),
		func(host string, port int) (supervisor.ProbeResult, error) {
			if host == "203.0.113.10" {
				return supervisor.ProbeResult{}, errTestHotSwitch
			}
			return supervisor.ProbeResult{Host: host, Port: port, Reachable: true, RTTMS: 42}, nil
		}, nil)(w, withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true))

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 2 {
		t.Fatalf("清单长度 = %d", len(got.Servers))
	}
	if got.Servers[0].Probe == nil || got.Servers[0].Probe.Reachable {
		t.Errorf("第一台应当是失败的:%+v", got.Servers[0].Probe)
	}
	if got.Servers[1].Probe == nil || !got.Servers[1].Probe.Reachable || got.Servers[1].Probe.RTTMS != 42 {
		t.Errorf("第二台的结论被第一台连累了:%+v", got.Servers[1].Probe)
	}
}

// **探测要用链接里那个端口。** 猜一个 443 会把跑在 9999 上的服务器测成不可达,
// 而用户看到的是一台好机器被标成红的 —— 比不测更糟。
func TestProbeUsesThePortFromTheLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "servers:\n    - name: odd\n      link: vless://x@203.0.113.10:9999?security=reality\ncurrent: odd\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var probed int
	serversHandler(path, 501, noSwitch(t), func(host string, port int) (supervisor.ProbeResult, error) {
		probed = port
		return supervisor.ProbeResult{Reachable: true, RTTMS: 1}, nil
	}, nil)(httptest.NewRecorder(), withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true))

	if probed != 9999 {
		t.Fatalf("测的是 %d 口,而链接里写的是 9999", probed)
	}
}

// 没接线时说「没接线」,不摆一堆红叉。
func TestProbeReportsWhenNotWired(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, nil)(
		w, withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true),
	)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("状态码 = %d, want 501", w.Code)
	}
}

// **吞吐只挂在实际在跑的那台上,而且只在真观测到的时候挂。**
//
// 吞吐是被动观测:没在用的服务器没有产生过流量,给它一个数就是编。
//
// **直接测 attachThroughput,不测 HTTP 应答体。** 走应答体的那一版被变异验证
// 当场证伪:`peak_bps` 带 omitempty,0 本来就不上线,于是「体里没有它」这条
// 断言永远成立 —— 测的是 omitempty,不是这个函数。而当前那台恰好排第一,
// 「给所有人都挂上」同样观察不到差别。两个坑都是「测试测的是相邻的东西」。
func TestThroughputOnlyLandsOnTheRunningServer(t *testing.T) {
	entries := []ServerEntry{
		{Name: "tokyo"},
		{Name: "osaka", Current: true},
		{Name: "nagoya"},
	}
	attachThroughput(entries, "osaka", coreLiveStatus{PeakBPS: 3_100_000, PeakAt: thBase}, nil, thBase)

	if entries[1].PeakBPS != 3_100_000 {
		t.Errorf("当前那台没拿到吞吐:%d", entries[1].PeakBPS)
	}
	for _, i := range []int{0, 2} {
		if entries[i].PeakBPS != 0 {
			t.Errorf("%s 没在用却被编了一个吞吐 %d —— 它没产生过流量",
				entries[i].Name, entries[i].PeakBPS)
		}
	}
}

// **没观测到就一个字都不写。** 0 会被读成「跑不动」,而真相是「这段时间没人
// 用它传东西」—— 一台整天没人用的服务器和一台被打满到爬的服务器,在计数上
// 都是安静的,只有前者是正常的。
//
// (wire 上还有 omitempty 兜底,但那是**另一层**:这里钉的是这个函数不许把
// 一个没有意义的 0 写进结构体,否则任何不经 JSON 的消费方都会读到它。)
func TestNoThroughputObservationWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running string
		live    coreLiveStatus
	}{
		{"没接线 / 问不出来", "", coreLiveStatus{}},
		{"问出来了但没有峰值", "tokyo", coreLiveStatus{PeakAt: thBase}},
		{"观测到负数", "tokyo", coreLiveStatus{PeakBPS: -1, PeakAt: thBase}},
		// **说不出年龄的数字不许上线**:一个不带年龄的峰值读起来就是现状。
		{"有峰值却没有观测时刻", "tokyo", coreLiveStatus{PeakBPS: 3_100_000}},
		// 时钟被改过 —— 与历史那一半同一条:宁可不报也不报一个负的年龄。
		{"观测时刻在未来", "tokyo", coreLiveStatus{PeakBPS: 3_100_000, PeakAt: thBase.Add(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := []ServerEntry{{Name: "tokyo", Current: true, PeakBPS: 7}}
			attachThroughput(entries, tc.running, tc.live, nil, thBase)
			if entries[0].PeakBPS != 7 {
				t.Fatalf("没有观测却动了那个值:%d", entries[0].PeakBPS)
			}
		})
	}
}

// **历史必须带年龄。** 所有者同意存历史(「以前的值没事」)的前提正是界面上要
// 标出来这是以前的 —— 一个不带年龄的历史数字读起来像现状。
func TestHistoricalThroughputCarriesItsAge(t *testing.T) {
	entries := []ServerEntry{{Name: "tokyo", Current: true}, {Name: "osaka"}}
	history := map[string]throughputEntry{
		"osaka": {PeakBPS: 8_000_000, ObservedAt: thBase.Add(-2 * time.Hour)},
	}
	attachThroughput(entries, "", coreLiveStatus{}, history, thBase)

	if entries[1].PeakBPS != 8_000_000 {
		t.Fatalf("历史没挂上:%d", entries[1].PeakBPS)
	}
	if entries[1].PeakAgeSeconds != int64(2*time.Hour/time.Second) {
		t.Fatalf("年龄 = %d 秒,want 7200 —— 不带年龄的历史数字读起来像现状",
			entries[1].PeakAgeSeconds)
	}
}

// 在跑的那台的**实时**观测压过历史,年龄归零。
func TestLiveThroughputOverridesHistoryForTheRunningServer(t *testing.T) {
	entries := []ServerEntry{{Name: "tokyo", Current: true}}
	history := map[string]throughputEntry{
		"tokyo": {PeakBPS: 8_000_000, ObservedAt: thBase.Add(-2 * time.Hour)},
	}
	attachThroughput(entries, "tokyo", coreLiveStatus{PeakBPS: 1_000_000, PeakAt: thBase}, history, thBase)

	if entries[0].PeakBPS != 1_000_000 {
		t.Fatalf("实时观测没有压过历史:%d", entries[0].PeakBPS)
	}
	if entries[0].PeakAgeSeconds != 0 {
		t.Fatalf("实时观测却带了年龄 %d 秒", entries[0].PeakAgeSeconds)
	}
}

// 时钟被改过时**宁可不报也不报一个负的年龄** —— 「-3 小时前」会让用户从此
// 不信这一栏里的任何数字。
func TestNegativeAgeIsDroppedNotShown(t *testing.T) {
	entries := []ServerEntry{{Name: "osaka"}}
	history := map[string]throughputEntry{
		"osaka": {PeakBPS: 8_000_000, ObservedAt: thBase.Add(time.Hour)},
	}
	attachThroughput(entries, "", coreLiveStatus{}, history, thBase)

	if entries[0].PeakBPS != 0 || entries[0].PeakAgeSeconds != 0 {
		t.Fatalf("未来时刻的观测被报了出来:%d bps / %d 秒",
			entries[0].PeakBPS, entries[0].PeakAgeSeconds)
	}
}

// **两次切换不许交错。**
//
// 一次切换是「写配置 → 武装 → 等隧道健康 → 确认」四步、最长二十几秒。交错的
// 后果不只是乱序,而是**验证验错了对象**:A 武装 B、B 武装 C 之后,A 那句
// 「隧道健康吗」问到的是 C 的隧道,于是 A 确认了 C,再对用户说「已切到 B」。
//
// 这是 2026-08-15 review 抓到的 —— 两边(菜单与 Guardian)当时都没有任何守卫,
// 而菜单在那二十几秒里窗口一直开着,再点一下就撞上。
func TestConcurrentSwitchesAreRejectedNotInterleaved(t *testing.T) {
	path := serversTestConfig(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var concurrent atomic.Int32
	handler := serversHandler(path, 501, func(name, link, udp string) error {
		if concurrent.Add(1) > 1 {
			t.Errorf("两次切换同时在跑 —— 第二次会验证第一次武装的那条隧道")
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		// **绝不无限阻塞。** 第一版写的是裸 `<-release`,于是守卫失效时(两次
		// 切换真的并发)主协程卡在第二次调用里、永远走不到 close(release) ——
		// 测试挂死而不是干净转红,CI 要等十分钟超时才知道出了事。
		// 一个在失败时挂住的测试,比没有测试更难用。
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		concurrent.Add(-1)
		return nil
	}, nil, nil)

	first := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		handler(w, withPeer(postServers(t, "osaka"), 501, true))
		first <- w.Code
	}()
	<-entered // 第一次已经进到热切那一步

	second := httptest.NewRecorder()
	handler(second, withPeer(postServers(t, "tokyo"), 501, true))
	// **立刻拒绝,不排队**:排队会让出口在接下来一分钟里自己跳好几次。
	if second.Code != http.StatusConflict {
		t.Fatalf("第二次切换状态码 = %d, want 409", second.Code)
	}
	if !strings.Contains(second.Body.String(), "servers_switch_busy") {
		t.Errorf("没说清是忙:%s", second.Body.String())
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("第一次切换被连累了:%d", code)
	}
}

// 一次结束之后必须能再切 —— 锁不许泄漏(defer 没写就会永久拒绝)。
func TestSwitchLockIsReleased(t *testing.T) {
	path := serversTestConfig(t)
	handler := serversHandler(path, 501, func(name, link, udp string) error { return nil }, nil, nil)
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		handler(w, withPeer(postServers(t, "osaka"), 501, true))
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次切换 = %d —— 锁泄漏了", i+1, w.Code)
		}
	}
}

// 热切失败时锁同样要放开(那条路是提前 return 的分支)。
func TestSwitchLockIsReleasedAfterFailure(t *testing.T) {
	path := serversTestConfig(t)
	handler := serversHandler(path, 501, func(name, link, udp string) error { return errTestHotSwitch }, nil, nil)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		handler(w, withPeer(postServers(t, "osaka"), 501, true))
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次 = %d", i+1, w.Code)
		}
	}
}

// 四种结局在线上必须分得开。合成一个码之后菜单只能说一句话,而那句话对
// 「已生效但确认失败」是**假的**(它切过去了,死手可能把它还原,用户得立刻
// 处理),对「回滚也失败了」则轻描淡写了一次正在发生的断网。
func TestServerSwitchPublishesADistinctOutcomePerFailure(t *testing.T) {
	seen := map[string]string{}
	for _, tc := range []struct {
		name string
		deps supervisor.SwitchDeps
		want string
	}{
		{"武装失败", supervisor.SwitchDeps{
			Arm: func(link, udp string) error { return errTestHotSwitch },
		}, "arm_failed"},
		{"不健康已回滚", supervisor.SwitchDeps{
			Arm:      func(link, udp string) error { return nil },
			Healthy:  func() bool { return false },
			Rollback: func() error { return nil },
		}, "rolled_back"},
		{"不健康且回滚失败", supervisor.SwitchDeps{
			Arm:      func(link, udp string) error { return nil },
			Healthy:  func() bool { return false },
			Rollback: func() error { return errTestHotSwitch },
		}, "rollback_failed"},
		{"已生效但确认失败", supervisor.SwitchDeps{
			Arm:     func(link, udp string) error { return nil },
			Healthy: func() bool { return true },
			Commit:  func() error { return errTestHotSwitch },
		}, "commit_failed"},
	} {
		deps := tc.deps
		path := serversTestConfig(t)
		handler := serversHandler(path, 501, func(name, link, udp string) error {
			return supervisor.SwitchServer(deps, name, link, udp)
		}, nil, nil)
		w := httptest.NewRecorder()
		handler(w, withPeer(postServers(t, "osaka"), 501, true))

		if w.Code != http.StatusOK {
			t.Fatalf("%s:状态码 = %d, want 200", tc.name, w.Code)
		}
		var got switchResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s:%v", tc.name, err)
		}
		if got.Outcome != tc.want {
			t.Errorf("%s:outcome = %q, want %q", tc.name, got.Outcome, tc.want)
		}
		if prev, dup := seen[got.Outcome]; dup {
			t.Errorf("%s 与 %s 共用同一个码 %q —— 菜单只能对两者说同一句话", tc.name, prev, got.Outcome)
		}
		seen[got.Outcome] = tc.name

		// 与 /v1/rules 的 409 同一条门规:完整原因只进 Guardian 日志。
		// 那句原话里带着服务器名与 Core 的错误细节,码是它唯一的对外出口。
		if strings.Contains(w.Body.String(), errTestHotSwitch.Error()) {
			t.Errorf("%s:原始错误串泄漏进了响应体:%s", tc.name, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "切换到") {
			t.Errorf("%s:那句中文原话泄漏进了响应体:%s", tc.name, w.Body.String())
		}
	}
}

// 认不出的错误不许套用四种里的任何一种。说错了比不说更糟:一句「已回滚」
// 会让用户以为流量还好好的走在原来那台上。
func TestServerSwitchDoesNotGuessAnOutcomeItCannotTell(t *testing.T) {
	handler := serversHandler(serversTestConfig(t), 501, func(name, link, udp string) error {
		return errTestHotSwitch
	}, nil, nil)
	w := httptest.NewRecorder()
	handler(w, withPeer(postServers(t, "osaka"), 501, true))
	var got switchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"arm_failed", "rolled_back", "rollback_failed", "commit_failed"} {
		if got.Outcome == code {
			t.Fatalf("认不出的错误被当成了 %q", code)
		}
	}
	if got.Outcome == "" {
		t.Fatal("认不出也得说一声,不能什么都不说")
	}
}

// **配置说 B、实际在跑 A,这两者不同正是这里最有价值的诊断信号。**
//
// 热切换是**先写配置再切**(反过来会留下「现在在 B、下次启动回 A」这种没人
// 看得出来的不一致),所以切换失败的那一刻配置已经是 B 了。合并成一个字段
// 之后,「配置说 B、流量还从 A 出去」就再也表达不出来 —— 界面会在弹出
// 「隧道没切过去」的同一秒,用那个 ● 断言你的流量从 B 出去。
func TestRunningServerIsPublishedBesideTheConfiguredOne(t *testing.T) {
	path := serversTestConfig(t)
	if err := setup.SetCurrentServer(path, "osaka"); err != nil {
		t.Fatal(err)
	}
	// Core 说它此刻指着 tokyo 那台的主机 —— 切换失败后的现场。
	handler := serversHandler(path, 501, noSwitch(t), nil, func() (coreLiveStatus, bool) {
		return coreLiveStatus{ServerHost: "203.0.113.10"}, true
	})
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true))

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Current != "osaka" {
		t.Fatalf("配置里选的那台 = %q, want osaka", got.Current)
	}
	if got.Running != "tokyo" {
		t.Fatalf("实际在跑的那台 = %q, want tokyo —— 与配置合并之后,"+
			"「配置说 B、流量还从 A 出去」就再也表达不出来", got.Running)
	}
}

// **Core 问不出来时那个键必须缺席,不是空串。**
//
// 空串会被读成「没有在跑」,而真相是「没问出来」—— 这个仓库为把
// 「问不出来」压成一个确定的坏答案栽过很多次(Tristate 那条纪律)。
func TestRunningServerIsAbsentWhenCoreCannotBeReached(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status coreStatusReader
	}{
		{"没接线", nil},
		{"Core 不可达", func() (coreLiveStatus, bool) { return coreLiveStatus{}, false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, tc.status)(
				w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
			)
			var got ServerListResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Running != "" {
				t.Fatalf("问不出来却报了一台在跑:%q", got.Running)
			}
			// 键缺席才是「没问出来」;`"running":""` 读起来是「没有在跑」。
			if strings.Contains(w.Body.String(), `"running"`) {
				t.Fatalf("问不出来时 running 键仍在体里:%s", w.Body.String())
			}
		})
	}
}

// **Core 报的主机在清单里对不上任何一台时,说不出名字就别说。**
//
// 编一个名字出来(比如退回配置里选的那台)恰恰是这一整条改动要消灭的谎。
func TestRunningServerIsAbsentWhenCoreReportsAnUnknownHost(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, func() (coreLiveStatus, bool) {
		return coreLiveStatus{ServerHost: "198.51.100.77", PeakBPS: 9_000_000}, true
	})(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true))

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Running != "" {
		t.Fatalf("认不出那台主机却报了个名字:%q", got.Running)
	}
	for _, s := range got.Servers {
		if s.PeakBPS != 0 {
			t.Fatalf("%s 拿到了一个不属于它的峰值 %d —— 认不出跑的是哪一台就"+
				"不该有人拿到那个数", s.Name, s.PeakBPS)
		}
	}
}

// **同一台主机上有两台时,说不出是哪一台就别说 —— 而这不是「对不上」,是
// 「对上了两条」。**
//
// Core 只报得出一个**主机**(RuntimeState.ServerHost 里没有名字),而同一主机上
// 挂两条记录是真会发生的:`bx server install --with-hysteria2` 出的两条链接、
// 凭据轮换时先加后删的那段过渡。此前那个循环取**第一条匹配**,于是:填实的 `●`
// 与「bx is actually using X right now.」落在错的那一台上,attachThroughput 把
// 实时峰值给了错的一行,而 recordThroughputOnce 会把它**按错的名字永久落盘**;
// `bx server list` 还会对着一台什么都没分歧的机器打出那段 ⚠ 分歧文案。
//
// 「说不出名字就别说」这条规则写在 runningServerName 头上 —— **有歧义是
// 「说不出」的一种**,与 observe.Tristate 那条「问不出来不是 false」同源。
func TestRunningServerIsAbsentWhenTwoEntriesShareTheHost(t *testing.T) {
	entries := []ServerEntry{
		{Name: "tokyo-tcp", Host: "203.0.113.10"},
		{Name: "tokyo-udp", Host: "203.0.113.10"},
	}
	if got := runningServerName(entries, "203.0.113.10"); got != "" {
		t.Errorf("两台同主机却报出了 %q —— 那个名字有一半的机会是错的,"+
			"而它会被 recordThroughputOnce 按名字永久写进盘上那份历史", got)
	}
	// 反面自检:少了它,一个「永远返回空串」的实现照样满足上面那条,而这一整个
	// 字段的价值(配置说 B、流量还从 A 出去)就没了。
	if got := runningServerName(entries, "203.0.113.11"); got != "" {
		t.Errorf("对不上任何一台却报了 %q", got)
	}
	unique := []ServerEntry{{Name: "tokyo", Host: "203.0.113.10"}, {Name: "osaka", Host: "203.0.113.20"}}
	if got := runningServerName(unique, "203.0.113.20"); got != "osaka" {
		t.Errorf("唯一那台没被认出来:%q", got)
	}
}

// **峰值挂在 Core 报的那台头上,不是配置里选的那台。**
//
// Core 的峰值来自一块进程级速率表,热切换**不会**把它清零。挂到配置里那台
// 头上、年龄再强行归零,读起来正是「刚刚在 B 上量到的」—— 而那个数是 A 的。
func TestLiveThroughputFollowsTheRunningServerNotTheConfiguredOne(t *testing.T) {
	entries := []ServerEntry{
		{Name: "tokyo", Host: "203.0.113.10"},
		{Name: "osaka", Host: "203.0.113.20", Current: true},
	}
	history := map[string]throughputEntry{
		"osaka": {PeakBPS: 8_000_000, ObservedAt: thBase.Add(-2 * time.Hour)},
	}
	attachThroughput(entries, "tokyo", coreLiveStatus{PeakBPS: 3_100_000, PeakAt: thBase}, history, thBase)

	// 配置里选的那台只能拿到**带年龄的历史**,绝不能拿到一个 age=0 的数 ——
	// 那读起来就是「刚刚在这台上量到的」,而那个数是另一台的。
	if entries[1].PeakBPS != 0 && entries[1].PeakAgeSeconds == 0 {
		t.Errorf("配置里选的那台顶着一个 age=0 的峰值 %d —— 那是另一台的数",
			entries[1].PeakBPS)
	}
	if entries[1].PeakBPS != 8_000_000 || entries[1].PeakAgeSeconds != int64(2*time.Hour/time.Second) {
		t.Errorf("配置里选的那台没有回落到带年龄的历史:%d bps / %d 秒",
			entries[1].PeakBPS, entries[1].PeakAgeSeconds)
	}
	// 反面:那个数必须落在实际在跑的那台上,否则「谁都不给」也能满足上面两条。
	if entries[0].PeakBPS != 3_100_000 || entries[0].PeakAgeSeconds != 0 {
		t.Errorf("实际在跑的那台没拿到实时峰值:%d bps / %d 秒",
			entries[0].PeakBPS, entries[0].PeakAgeSeconds)
	}
}

// **盘上那份历史也必须记在实际在跑的那台名下。**
//
// 只修应答体、不修落盘的那一半,会在磁盘上留下一条错的记录 —— 而它此后每次
// 打开窗口都会被当成「B 以前跑到过这么快」原样显示出来。
func TestThroughputHistoryIsRecordedUnderTheRunningServer(t *testing.T) {
	path := serversTestConfig(t)
	if err := setup.SetCurrentServer(path, "osaka"); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(t.TempDir(), "throughput-history.json")
	observedAt := thBase.Add(-time.Minute)

	recordThroughputOnce(path, historyPath, func() (stats.Report, error) {
		// 配置说 osaka,而 Core 指着 tokyo 那台的主机。
		return stats.Report{Server: "203.0.113.10", PeakBPS: 3_100_000, PeakAt: observedAt}, nil
	})

	state, err := loadThroughputState(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Servers["osaka"]; ok {
		t.Fatalf("峰值被记到了配置里那台名下 —— 那条错的历史此后会一直冒充 osaka 的成绩:%+v", state.Servers)
	}
	got, ok := state.Servers["tokyo"]
	if !ok || got.PeakBPS != 3_100_000 || !got.ObservedAt.Equal(observedAt) {
		t.Fatalf("实际在跑的那台没记上:%+v", state.Servers)
	}
}

// 认不出 Core 报的那台主机时**一个字都不写盘**:记到错的名下之后没有任何
// 一处会说它错了。
func TestThroughputHistoryRecordsNothingForAnUnknownRunningHost(t *testing.T) {
	path := serversTestConfig(t)
	historyPath := filepath.Join(t.TempDir(), "throughput-history.json")

	recordThroughputOnce(path, historyPath, func() (stats.Report, error) {
		return stats.Report{Server: "198.51.100.77", PeakBPS: 3_100_000, PeakAt: thBase}, nil
	})

	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("认不出跑的是哪一台却写了盘(err=%v)", err)
	}
}

// **一次成功的切换照样会撒谎,而上面那条守卫看不见它。**
//
// `TestLiveThroughputFollowsTheRunningServerNotTheConfiguredOne` 只演了
// running != current(切换失败)那一半。切换**成功**时 running == current,
// 按「谁在跑归谁」这条判据一切正常 —— 但 Core 那块速率表是**进程级**的,
// 热切换不会把它的峰值清零:在 tokyo 上跑了 25 分钟的 3.1 MB/s,切到 osaka
// 之后它照样报这个数。年龄写死成 0(而 PeakAgeSeconds 带 omitempty,0 连键
// 都不上线)之后,界面读到的就是「刚刚在 osaka 上量到 3.1 MB/s」。
//
// 这里要的只是**说出那个数字真实的年龄**;把速率表在热切时清零是 Core 那边的
// 改动,不在这一层做。
func TestLiveThroughputCarriesItsRealAgeAfterASuccessfulSwitch(t *testing.T) {
	entries := []ServerEntry{
		{Name: "tokyo", Host: "203.0.113.10"},
		{Name: "osaka", Host: "203.0.113.20", Current: true},
	}
	// 切换已经成功:配置与 Core 都指着 osaka。而那个峰值是 25 分钟前的事,
	// 那时流量还从 tokyo 出去。
	live := coreLiveStatus{
		ServerHost: "203.0.113.20",
		PeakBPS:    3_100_000,
		PeakAt:     thBase.Add(-25 * time.Minute),
	}
	attachThroughput(entries, "osaka", live, nil, thBase)

	if entries[1].PeakAgeSeconds != int64(25*time.Minute/time.Second) {
		t.Errorf("年龄 = %d 秒, want 1500 —— 写死 0 的那个数读起来就是"+
			"「刚刚在 osaka 上量到的」,而它是 25 分钟前在 tokyo 上量到的",
			entries[1].PeakAgeSeconds)
	}
	if entries[1].PeakBPS != 3_100_000 {
		t.Errorf("那个数本身不该被丢掉:%d", entries[1].PeakBPS)
	}
}

// 反面:真的是刚量到的,就**不该**凭空长出一个年龄来 —— 否则「不带年龄 =
// 现状」这条约定会被一个恒非零的年龄从另一头毁掉。
func TestGenuinelyFreshThroughputHasNoAge(t *testing.T) {
	entries := []ServerEntry{{Name: "tokyo", Host: "203.0.113.10", Current: true}}
	live := coreLiveStatus{ServerHost: "203.0.113.10", PeakBPS: 3_100_000, PeakAt: thBase.Add(-300 * time.Millisecond)}
	attachThroughput(entries, "tokyo", live, nil, thBase)

	if entries[0].PeakBPS != 3_100_000 || entries[0].PeakAgeSeconds != 0 {
		t.Fatalf("刚量到的却带了年龄:%d bps / %d 秒",
			entries[0].PeakBPS, entries[0].PeakAgeSeconds)
	}
}

// **年龄要真的到得了线上。** 上面那条测的是 attachThroughput,而
// PeakAgeSeconds 带 omitempty —— 一个在结构体里算对了、却没被发出去的年龄,
// 与写死 0 在窗口里完全一样。
func TestServerListPublishesTheAgeOfTheLivePeak(t *testing.T) {
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t), nil, func() (coreLiveStatus, bool) {
		return coreLiveStatus{
			ServerHost: "203.0.113.10",
			PeakBPS:    3_100_000,
			PeakAt:     time.Now().Add(-25 * time.Minute),
		}, true
	})(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true))

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Servers[0].PeakBPS != 3_100_000 {
		t.Fatalf("峰值没上线:%+v", got.Servers[0])
	}
	// 25 分钟前 ±1 分钟(这条走真实时钟)。
	if age := got.Servers[0].PeakAgeSeconds; age < 24*60 || age > 26*60 {
		t.Fatalf("年龄 = %d 秒, want ≈1500 —— 缺席的年龄读起来就是「刚刚量到的」", age)
	}
}

// ---------------------------------------------------------------------------
// remove / replace:两个此前只有命令行够得着的动作
// ---------------------------------------------------------------------------

// 删掉一台不在用的:盘上真的少了那一条,current 一动不动。
func TestServerRemoveDropsTheEntryFromTheConfig(t *testing.T) {
	path := serversTestConfig(t)
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "remove", Name: "osaka",
	}), 501, true))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	list, current, err := setup.ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !strings.EqualFold(list[0].Name, "tokyo") {
		t.Fatalf("盘上的清单 = %+v,want 只剩 tokyo —— 应答说删了而盘上没删,是最坏的一种", list)
	}
	if current != "tokyo" {
		t.Fatalf("删掉别的那台却动了 current(=%q)", current)
	}
	// 回的是改动后的完整清单,界面据此重画,不必自己推演。
	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 1 || got.Current != "tokyo" {
		t.Fatalf("应答不是改动后的完整清单:%+v", got)
	}
}

// **删掉当前正在用的那台一律拒绝,而且盘上文件一个字节都不许动。**
//
// 删掉它会让 current 指向一个不存在的名字,下一次启动直接起不来 —— 而用户
// 只是想清理一条记录。**一次「被拒绝」却仍然写了盘的删除,比拒绝失败更糟**:
// 用户看到一句拒绝,以为什么都没发生,而配置已经变了。
func TestServerRemoveRefusesTheCurrentOneAndWritesNothing(t *testing.T) {
	path := serversTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "remove", Name: "tokyo",
	}), 501, true))

	// **先查盘,再查应答,而且都用 Errorf。** 顺序与严厉程度都是有意的:
	// 「拒绝了但还是写了盘」比「没拒绝」更糟,而一个 Fatalf 的状态码断言会让
	// 后面那条永远跑不到 —— 两条守卫塌成一条,而活下来的是次要的那条。
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(before) != string(after) {
		t.Errorf("这次删除动了盘上的配置:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if w.Code != http.StatusConflict {
		t.Errorf("状态码 = %d, want 409:%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "servers_remove_current") {
		t.Errorf("没说清是「那是当前那台」:%s", w.Body.String())
	}
	// 反面自检:那台确实还在、current 也还指着它 —— 否则「文件没变」也可能
	// 是因为这个 fixture 压根就没有 tokyo,而那样这条守卫什么都没守。
	list, current, lerr := setup.ListServers(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(list) != 2 || current != "tokyo" {
		t.Errorf("拒绝之后清单变了:%+v current=%q", list, current)
	}
}

// 就地换掉同名那台的链接:current 不变,其余内容不动。
//
// **换链接是修一条记录,不是换出口。** 凭据轮换、VPS 重建换了地址,都不构成
// 「把我的流量换到那里去」的请求 —— 与「加一台不许把出口换过去」同一条。
func TestServerReplaceSwapsTheLinkInPlace(t *testing.T) {
	path := serversTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	oldLink := "vless://" + serversTestUUID + "@203.0.113.20:443?security=reality"
	newLink := "vless://" + serversTestUUID + "@203.0.113.99:8443?security=reality"
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "replace", Name: "osaka", Link: newLink,
	}), 501, true))

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	list, current, lerr := setup.ListServers(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if current != "tokyo" {
		t.Fatalf("换一条链接把出口换到了 %q —— 换链接不构成换出口的请求", current)
	}
	if len(list) != 2 {
		t.Fatalf("清单长度 = %d, want 2 —— 换链接不该多出或少掉一台:%+v", len(list), list)
	}
	if list[1].Link != newLink {
		t.Fatalf("osaka 的链接 = %q, want %q", list[1].Link, newLink)
	}
	if !strings.Contains(list[0].Link, "203.0.113.10") {
		t.Fatalf("另一台的链接被连累了:%q", list[0].Link)
	}
	// **盘上其余内容一个字都没动**:唯一的差别就是那条链接本身。
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if want := strings.Replace(string(before), oldLink, newLink, 1); want != string(after) {
		t.Fatalf("盘上不止那条链接变了:\n--- want\n%s\n--- got\n%s", want, after)
	}
	// 应答回的是改动后的完整清单,而且链接本身仍然不许出门(它是凭据)。
	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 2 || got.Current != "tokyo" || got.Servers[1].Host != "203.0.113.99" {
		t.Fatalf("应答不是改动后的完整清单:%+v", got)
	}
	if strings.Contains(w.Body.String(), serversTestUUID) {
		t.Errorf("应答里出现了链接凭据:%s", w.Body.String())
	}
}

// **没让它改的东西不许被顺手抹掉。**
//
// osaka 那台配了独立的 UDP 传输。只换主链接时把 udp: 一起删掉,UDP 会**静默**
// 回落到主传输 —— 没有任何一处会报错,而用户以为自己只改了一条链接。
func TestServerReplaceKeepsTheUDPLinkItWasNotAskedToChange(t *testing.T) {
	path := serversTestConfig(t)
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "replace", Name: "osaka",
		Link: "vless://" + serversTestUUID + "@203.0.113.99:8443?security=reality",
	}), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	list, _, err := setup.ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if list[1].UDP != "hysteria2://pw@203.0.113.21:443" {
		t.Fatalf("osaka 的 UDP 传输变成了 %q —— 只换主链接不该动它,UDP 会静默走主传输", list[1].UDP)
	}
	// 反面:明确给了 UDP 时它必须被换掉,否则「永远不动 udp」也能满足上面那条。
	w = httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "replace", Name: "osaka",
		Link: "vless://" + serversTestUUID + "@203.0.113.99:8443?security=reality",
		UDP:  "hysteria2://pw@203.0.113.99:8443",
	}), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	if list, _, err = setup.ListServers(path); err != nil {
		t.Fatal(err)
	} else if list[1].UDP != "hysteria2://pw@203.0.113.99:8443" {
		t.Fatalf("给了 UDP 却没换:%q", list[1].UDP)
	}
}

// **名字不在清单里的 replace 必须被拒,而且一个字节都不写。**
//
// 底下那个原语对不存在的名字是「加一台」:用户在名字上敲错一个字母,就会凭空
// 多出一台顶着新链接的服务器,而界面只会说「替换成功」。
func TestServerReplaceRejectsAnUnknownNameInsteadOfAddingIt(t *testing.T) {
	path := serversTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "replace", Name: "nagoya", Link: "vless://x@203.0.113.30:443",
	}), 501, true))

	// 盘先查、且都用 Errorf:一个 Fatalf 的状态码断言会让「凭空多出一台」
	// 那条永远跑不到,而后者才是这条守卫真正要挡的东西。
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(before) != string(after) {
		t.Errorf("这次 replace 动了盘上的配置:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if list, _, lerr := setup.ListServers(path); lerr != nil || len(list) != 2 {
		t.Errorf("清单 = %+v(err=%v),want 仍是两台 —— 敲错一个字母不该凭空多出一台", list, lerr)
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d, want 400:%s", w.Code, w.Body.String())
	}
}

// 坏输入在写盘之前挡掉,并且如实报错 —— 与 add 那条同一条纪律。
func TestServerRemoveAndReplaceRejectBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  serversRequest
	}{
		{"remove 没给名字", serversRequest{Action: "remove"}},
		{"replace 没给名字", serversRequest{Action: "replace", Link: "vless://x@203.0.113.30:443"}},
		{"replace 没给链接", serversRequest{Action: "replace", Name: "osaka"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := serversTestConfig(t)
			before, _ := os.ReadFile(path)
			w := httptest.NewRecorder()
			serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, tc.req), 501, true))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, want 400:%s", w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("被拒绝的请求改动了盘上的配置")
			}
		})
	}
}

// remove 与 replace **都绝不热切**:noSwitch 在被调用时 t.Fatal。改配置与
// 换出口是两件事,后者要用户在清单里显式点一下。
func TestServerRemoveAndReplaceNeverHotSwitch(t *testing.T) {
	for _, req := range []serversRequest{
		{Action: "remove", Name: "osaka"},
		{Action: "replace", Name: "osaka", Link: "vless://x@203.0.113.99:443"},
	} {
		path := serversTestConfig(t)
		w := httptest.NewRecorder()
		serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, req), 501, true))
		if w.Code != http.StatusOK {
			t.Fatalf("%s:状态码 = %d:%s", req.Action, w.Code, w.Body.String())
		}
	}
}

// **每一条应答都得说「实际在跑的是哪一台」,不只是 GET 那一条。**
//
// 上一轮把 Running 只加在了 GET 那条路上,而 `bx server list --test` 走的是
// probe 那条 —— 于是一台 Guardian 与 Core 都完全健康的机器,每次都被告知
// 「实际在跑的是哪一台这次没问到」。
//
// **修法不许是「让消费方记住上一次的值」**:那是客户端状态,会陈旧,而它陈旧
// 的那一刻恰好就是热切换刚失败、这个字段最有价值的一刻。服务端每一次都说实话。
//
// **「每一条」指的是回清单的那些,共五条(GET + 四个动作)。** 第六条出路 ——
// 换服务器 —— 回的是 switchResponse,那是另一个类型、按设计没有 running:
// 它答的是「这一次切换的结局」,而不是「现在的清单长什么样」,菜单紧接着会
// 重新拉一次清单。名字里的 Every 指的是前者;真要给 switchResponse 也加,
// 那是另一个决定,不是这条守卫漏掉的东西。
func TestEveryServerResponsePublishesTheRunningServer(t *testing.T) {
	core := func() (coreLiveStatus, bool) {
		return coreLiveStatus{ServerHost: "203.0.113.10"}, true
	}
	probe := func(host string, port int) (supervisor.ProbeResult, error) {
		return supervisor.ProbeResult{Host: host, Port: port, Reachable: true, RTTMS: 7}, nil
	}
	for _, tc := range []struct {
		name string
		req  func(t *testing.T) *http.Request
	}{
		{"GET 清单", func(t *testing.T) *http.Request {
			return httptest.NewRequest(http.MethodGet, "/v1/servers", nil)
		}},
		{"probe", func(t *testing.T) *http.Request {
			return postServersJSON(t, serversRequest{Action: "probe"})
		}},
		{"add", func(t *testing.T) *http.Request {
			return postServersJSON(t, serversRequest{Action: "add", Name: "nagoya", Link: "vless://x@203.0.113.30:443"})
		}},
		{"remove", func(t *testing.T) *http.Request {
			return postServersJSON(t, serversRequest{Action: "remove", Name: "osaka"})
		}},
		{"replace", func(t *testing.T) *http.Request {
			return postServersJSON(t, serversRequest{Action: "replace", Name: "osaka", Link: "vless://y@203.0.113.20:443"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			serversHandler(serversTestConfig(t), 501, noSwitch(t), probe, core)(
				w, withPeer(tc.req(t), 501, true),
			)
			if w.Code != http.StatusOK {
				t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
			}
			var got ServerListResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Running != "tokyo" {
				t.Fatalf("running = %q, want tokyo —— 少了它,一台完全健康的机器"+
					"每次都被告知「实际在跑的是哪一台这次没问到」:%s", got.Running, w.Body.String())
			}
		})
	}
}

// **拼错一个动作名不许把用户的出口换掉。**
//
// 空 Action = 「换到 Name 那一台」是这个端点最初的契约,而 remove / replace
// 的请求**按构造带着一个合法的名字** —— 正是用户想删掉或想换链接的那一台。
// 于是一个 `{"action":"delete","name":"osaka"}` 落进兼容分支之后,做的不是
// 「找不到名为 delete 的服务器」,而是**真的把出口切到 osaka**:配置改了、
// 热切也做了、还回 200。Task 6 的 Delete 按钮只要把 "remove" 写成 "delete",
// 点一下就换了出口 IP 与国家 —— multi-server 设计里明写「只有用户可以切」。
//
// 断言三件事,**而且刻意不用 noSwitch 那个替身**:它是 t.Fatal,一旦热切真的
// 发生,整个子测试当场结束 —— 后面「盘上动没动」那条就永远跑不到,而那正是
// 这里最该看见的一条。改用一个只记账的替身,三条各自 Errorf、互不遮蔽。
func TestAMisspelledActionIsRejectedInsteadOfSwitchingTheExit(t *testing.T) {
	for _, action := range []string{"delete", "rm", "del", "update", "switch", "replace-link"} {
		t.Run(action, func(t *testing.T) {
			path := serversTestConfig(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switched := ""
			w := httptest.NewRecorder()
			serversHandler(path, 501, func(name, link, udp string) error {
				switched = name
				return nil
			}, nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
				// 名字是合法的、而且不是当前那台 —— 这正是 remove/replace 请求的形状。
				Action: action, Name: "osaka",
			}), 501, true))

			if switched != "" {
				t.Errorf("认不出的动作 %q 把正在跑的实例切到了 %q", action, switched)
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(before) != string(after) {
				t.Errorf("认不出的动作 %q 改了盘上的配置:\n--- before\n%s\n--- after\n%s", action, before, after)
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("状态码 = %d, want 400:%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "servers_unknown_action") {
				t.Errorf("没说清是「这个动作我不认识」:%s", w.Body.String())
			}
		})
	}
	// 反面:空 Action 仍然是「换过去」,那是这个端点最初的契约,不许被一起收掉。
	path := serversTestConfig(t)
	switched := ""
	w := httptest.NewRecorder()
	serversHandler(path, 501, func(name, link, udp string) error {
		switched = name
		return nil
	}, nil, nil)(w, withPeer(postServers(t, "osaka"), 501, true))
	if w.Code != http.StatusOK || switched != "osaka" {
		t.Fatalf("空 Action 不再换服务器了(code=%d switched=%q)—— 兼容契约被一起收掉了", w.Code, switched)
	}
}

// **换链接必须校验那条链接解不解得出主机 —— 而这一条的代价落在当前那台上。**
//
// 此前 replace 只判 `link != ""`,底下的 `setup.ReplaceServerLink` 也不校验:
// 任何字符串都写得进去。换的是**当前**那台时,配置就此指着一条下一次重连解析
// 不出来的链接,而界面刚刚承诺「bx picks up the new one when it reconnects」——
// 一句当场就被证伪的话,而且它把机器留在一个起不来的状态里。
//
// `setup.LinkHost` 就在同一个请求里被逐台调过(serverEntries 拿它算 Host),
// 这道门只是把它挪到写盘**之前**。**拒绝只发码**:链接是凭据,不回显、不进日志。
func TestServerReplaceRejectsALinkWithNoParseableHost(t *testing.T) {
	path := serversTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	const badLink = "vless://user:supersecretpassword@ho st:443"
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "replace", Name: "tokyo", Link: badLink,
	}), 501, true))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("解不出主机的链接被写进了当前那台:%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "servers_replace_failed") {
		t.Errorf("没给一个菜单说得出话的码:%s", w.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("拒绝之后盘上的配置动了 —— 一次「被拒绝」却仍然改了配置,比拒绝失败更糟")
	}
	if logged := buf.String(); strings.Contains(logged, "supersecretpassword") || strings.Contains(logged, badLink) {
		t.Fatalf("日志里出现了凭据:%s", logged)
	}
}

// **一份没有 current: 的配置照样在跑,换链接不许顺手替它挑一个出口。**
//
// `config.resolveServers` 对没有 current 的清单回落 servers[0],所以这种配置
// 是**能起来的**(手改出来的配置正是这个样子,而 7.2 这一节的受众恰好就是
// 手改配置的人)。底下那个原语会「顺手填上空的 current」—— 对 add 是对的
// (一份新清单必须有一台在用),对 replace 就是把出口从 tokyo 挪到了 osaka,
// 而用户只是换了一条链接。
//
// **这与不走 UpsertServer 是同一种伤害换了一扇门进来。**
func TestServerReplaceDoesNotPickAnExitForACurrentlessConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "servers:\n" +
		"    - name: tokyo\n" +
		"      link: vless://" + serversTestUUID + "@203.0.113.10:443?security=reality\n" +
		"    - name: osaka\n" +
		"      link: vless://" + serversTestUUID + "@203.0.113.20:443?security=reality\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	oldLink := "vless://" + serversTestUUID + "@203.0.113.20:443?security=reality"
	newLink := "vless://" + serversTestUUID + "@203.0.113.99:8443?security=reality"
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "replace", Name: "osaka", Link: newLink,
	}), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d:%s", w.Code, w.Body.String())
	}
	_, current, err := setup.ListServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if current != "" {
		t.Errorf("换一条链接给一份本来没有 current 的配置挑了 %q 当出口 —— "+
			"Core 本来用的是清单里第一台(tokyo)", current)
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if want := strings.Replace(body, oldLink, newLink, 1); want != string(after) {
		t.Errorf("盘上不止那条链接变了:\n--- want\n%s\n--- got\n%s", want, after)
	}
	// 应答也不许自己编一个 current 出来。
	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Current != "" {
		t.Errorf("应答里的 current = %q,而盘上没有这一行", got.Current)
	}
}

// **「这台已经没了」与「盘没写成」必须分得开。**
//
// 两个菜单窗口开着、同一台删两次是真会发生的;把「已经删掉了」和一次真实的
// 写盘失败折成同一个码,菜单就只能对两者说同一句话,而前者其实什么都不用做。
// replace 那半早有 servers_unknown_name,remove 这半此前没有。
func TestServerRemoveSaysWhenTheNameIsAlreadyGone(t *testing.T) {
	path := serversTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t), nil, nil)(w, withPeer(postServersJSON(t, serversRequest{
		Action: "remove", Name: "nagoya",
	}), 501, true))

	if w.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d, want 400:%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "servers_unknown_name") {
		t.Errorf("「已经没了」与「写盘失败」共用一个码:%s", w.Body.String())
	}
	after, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(before) != string(after) {
		t.Errorf("删一个不存在的名字动了盘上的配置")
	}
}

// **单服务器配置不是「一份空清单」,而窗口要说不同的话。**
//
// `bx setup` 从不写 `servers:` 清单,所以每一个正常装好 bx 的用户打开服务器
// 窗口时清单都是空的 —— 而 bx 此刻正跑着一台服务器。两种「空」在 JSON 里都是
// `[]`,客户端推不出来,所以这个区分必须由服务端说出来。
func TestSingleServerConfigIsDistinguishableFromAnEmptyServerList(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"单服务器配置", "server: vless://" + serversTestUUID + "@203.0.113.10:443\n", true},
		{"transports 配置", "transports:\n    - vless://" + serversTestUUID + "@203.0.113.10:443\n", true},
		{"空清单", "servers: []\n", false},
		{"有清单", "servers:\n    - name: tokyo\n      link: vless://" +
			serversTestUUID + "@203.0.113.10:443\ncurrent: tokyo\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			serversHandler(path, 501, noSwitch(t), nil, nil)(
				w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
			)
			if w.Code != http.StatusOK {
				t.Fatalf("状态码 = %d,body=%s", w.Code, w.Body.String())
			}
			var resp ServerListResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.SingleServer != tc.want {
				t.Errorf("single_server = %v, want %v(body=%s)", resp.SingleServer, tc.want, w.Body.String())
			}
		})
	}

	// **不带 omitempty**:键缺席读作「这一版 Guardian 没说」,而它是客户端
	// 区分新旧 Guardian 的唯一信号。
	b, err := json.Marshal(ServerListResponse{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"single_server"`) {
		t.Fatalf("single_server 带了 omitempty:%s —— 键缺席就与旧 Guardian 无法区分了", b)
	}
}

// **失败原因必须带一个机器可读的码,而不只是一句中文。**
//
// `supervisor.probeServer` 给的 Error 是中文(它的第一个消费方是 `bx server list`),
// 而这份应答的另一个消费方是**全英文**的 macOS 菜单 —— 真机上一台关着的服务器
// 会让菜单显示「连接被拒(端口没在听)」。菜单那边的 CJK 守卫只扫它自己的源码,
// 看不见从这里来的字符串,所以这个区分必须由这一层发出去:**服务端发码,
// 客户端出语言**。
func TestProbeReportsCarryAMachineReadableCode(t *testing.T) {
	// ① 真的测了、没通:码原样转发。
	w := httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t),
		func(host string, port int) (supervisor.ProbeResult, error) {
			return supervisor.ProbeResult{
				Host: host, Port: port,
				Error:     "连接被拒(端口没在听)",
				ErrorCode: supervisor.ProbeErrRefused,
			}, nil
		}, nil)(w, withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true))

	var got ServerListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, entry := range got.Servers {
		if entry.Probe == nil || entry.Probe.ErrorCode != supervisor.ProbeErrRefused {
			t.Errorf("%s 的失败没有带码:%+v —— 菜单只能显示服务端那句中文了", entry.Name, entry.Probe)
		}
	}

	// ② 探测这一步压根没做成:Guardian 自己那两处产地也要带码。
	w = httptest.NewRecorder()
	serversHandler(serversTestConfig(t), 501, noSwitch(t),
		func(host string, port int) (supervisor.ProbeResult, error) {
			return supervisor.ProbeResult{}, errTestHotSwitch
		}, nil)(w, withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true))
	got = ServerListResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, entry := range got.Servers {
		if entry.Probe == nil || entry.Probe.ErrorCode != supervisor.ProbeErrCoreUnreachable {
			t.Errorf("%s 的「没测成」没有带码:%+v", entry.Name, entry.Probe)
			continue
		}
		// **人话也要经 supervisor.ProbeErrorText,不许在这儿手写一句。**
		// 手写的后果有两层:这句话的唯一消费方是中文的 `bx server list`
		// (菜单不读这个字段),手写出来的英文在那儿是错的语言;而且
		// 「码 ↔ 中文」那张表里的这一条在生产里就**永远不可达**,
		// 于是守卫声称覆盖 11 个码、实际只覆盖得到 9 个。
		if want := supervisor.ProbeErrorText(supervisor.ProbeErrCoreUnreachable); entry.Probe.Error != want {
			t.Errorf("%s 的人话不是 ProbeErrorText 给的:%q,want %q", entry.Name, entry.Probe.Error, want)
		}
	}

	// ③ 链接解不出主机那一处。
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("servers:\n    - name: broken\n      link: \"://\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	serversHandler(path, 501, noSwitch(t),
		func(host string, port int) (supervisor.ProbeResult, error) {
			t.Fatal("主机都解不出来,不该真去拨号")
			return supervisor.ProbeResult{}, nil
		}, nil)(w, withPeer(postServersJSON(t, serversRequest{Action: "probe"}), 501, true))
	got = ServerListResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Servers) != 1 {
		t.Fatalf("清单长度 = %d", len(got.Servers))
	}
	if got.Servers[0].Probe == nil || got.Servers[0].Probe.ErrorCode != supervisor.ProbeErrLinkUnparsed {
		t.Fatalf("链接解不出主机那一处没有带码:%+v", got.Servers[0].Probe)
	}
	if want := supervisor.ProbeErrorText(supervisor.ProbeErrLinkUnparsed); got.Servers[0].Probe.Error != want {
		t.Errorf("链接解不出主机那一处的人话不是 ProbeErrorText 给的:%q,want %q",
			got.Servers[0].Probe.Error, want)
	}
}

// postServersRaw 发一个**原样的 JSON 串**,不经 serversRequest 编码。
//
// 这个功能的整个判据是「服务端收到一个它不认识的**键**时会怎样」,而
// postServersJSON 按构造只发得出认识的键 —— 用它写这条测试,被测的那件事
// 在输入里根本不存在。
func postServersRaw(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/servers", strings.NewReader(body))
}

// recordingSwitch 记下有没有热切、切到了哪台。
//
// **刻意不用 noSwitch。** 后者用 t.Fatalf,而 Fatalf 会 runtime.Goexit ——
// 一旦缺陷复发、真的切了,这条测试就停在那一句上,后面「盘上一个字节没动」
// 与「状态码是 400」两条断言**永远跑不到**,而它们才是这个功能的性质。
// (这个错误在 Task 4 的第一轮里犯过一次。)
func recordingSwitch(switched *string) serverSwitcher {
	return func(name, link, udp string) error {
		*switched = name
		return nil
	}
}

// **拼错的键不许从隔壁那扇门进来。**
//
// Task 4 关掉的是拼错的**值**(`{"action":"delete"}` 落进兼容分支)。而解码器
// 当时没开 DisallowUnknownFields,于是拼错的是**键名**时 `Action` 解出空串、
// 照样落进那条「空 action = 换到 Name 那一台」的兼容分支。re-review 在真 handler
// 上实发过:
//
//	{"actoin":"remove","name":"osaka"}  →  200, switched="osaka", 配置被改写
//
// 与刚修掉的那条伤害逐字相同:用户点一下 Delete,**出口 IP 与国家换到了他想
// 删掉的那一台** —— 2026-08-09 multi-server 设计里唯一明令禁止的事。
//
// 这条守卫钉的是「未知的键不许走到切换那一步」这个**性质**,不是某个拼法:
// 把 DisallowUnknownFields 拿掉,下面三种形状里的每一种都会转红。
func TestAMisspelledKeyIsRejectedInsteadOfSwitchingTheExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		// re-review 在 HEAD 上实发的那一条,逐字。
		{"action 的键名手滑", `{"actoin":"remove","name":"osaka"}`},
		// 兼容分支的形状 + 一个多出来的键:菜单发出这种东西的唯一原因是
		// 它以为自己在请求别的动作 —— 而服务端会照兼容契约把出口切过去。
		{"兼容形状上多一个键", `{"name":"osaka","delete":true}`},
		// 动作是认识的,可请求里带着一个服务端不认识的键 —— 那意味着发它的
		// 客户端与这一版服务端**对这次请求的含义没有共识**。照旧执行等于
		// 「把那个键当不存在」,而这里那个动作是删除:一次以为带着确认/条件的
		// 删除会被当成无条件的删除做掉。
		{"认识的动作上多一个键", `{"action":"remove","name":"osaka","confrim":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := serversTestConfig(t)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switched := ""
			w := httptest.NewRecorder()
			serversHandler(path, 501, recordingSwitch(&switched), nil, nil)(
				w, withPeer(postServersRaw(tc.body), 501, true))

			if switched != "" {
				t.Errorf("%s:认不出的键让正在跑的实例切到了 %q —— 出口 IP 与国家被换掉了", tc.body, switched)
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(before) != string(after) {
				t.Errorf("%s:认不出的键改了盘上的配置:\n--- before\n%s\n--- after\n%s", tc.body, before, after)
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s:状态码 = %d, want 400:%s", tc.body, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "servers_bad_request") {
				t.Errorf("%s:没说清是「这个请求我读不懂」:%s", tc.body, w.Body.String())
			}
		})
	}

	// **反面一:空 Action 仍然换服务器。** 那是这个端点最初唯一的动作,老客户端
	// (以及菜单今天的 switchServer)还在用它,收紧未知键不许把它一起收掉。
	t.Run("只带 name 的合法请求仍然切换", func(t *testing.T) {
		path := serversTestConfig(t)
		switched := ""
		w := httptest.NewRecorder()
		serversHandler(path, 501, recordingSwitch(&switched), nil, nil)(
			w, withPeer(postServersRaw(`{"name":"osaka"}`), 501, true))
		if w.Code != http.StatusOK || switched != "osaka" {
			t.Fatalf("兼容契约被一起收掉了(code=%d switched=%q):%s", w.Code, switched, w.Body.String())
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "current: osaka") {
			t.Errorf("配置没跟着切:\n%s", body)
		}
	})

	// **反面二:认识的键一个都没被误伤。** 一条「把什么都拒绝掉」的实现会让
	// 上面三条全绿,而这一条会红。
	t.Run("认识的键组成的 remove 仍然生效", func(t *testing.T) {
		path := serversTestConfig(t)
		w := httptest.NewRecorder()
		serversHandler(path, 501, noSwitch(t), nil, nil)(
			w, withPeer(postServersRaw(`{"action":"remove","name":"osaka"}`), 501, true))
		if w.Code != http.StatusOK {
			t.Fatalf("合法的 remove 被误伤了(code=%d):%s", w.Code, w.Body.String())
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "osaka") {
			t.Errorf("remove 没生效:\n%s", body)
		}
	})
}

// **改清单的那几个动词要有自己的能力门。**
//
// CapabilityServers 的含义早于 remove / replace:一台只声明它的旧 Guardian 收到
// `{"action":"remove","name":"osaka"}` 时走的是那一版唯一的兼容行为 —— **换到
// osaka**。菜单若按 `servers` 门控 Delete 按钮,「文件换了、进程没换」那个真实
// 的升级窗口里点一下 Delete,换掉的是用户的出口国。
//
// **这条守卫钉的是能力的「值」本身,不只是「清单里有这么一个能力」。**
// 菜单按字面量门控(CapabilityLogs 有同款先例):改了值,菜单永久看不见这些
// 动词,而两侧都不报错 —— 一次静默的功能消失。
func TestServersEditCapabilityIsDeclared(t *testing.T) {
	if CapabilityServersEdit != "servers_edit" {
		t.Fatalf("CapabilityServersEdit 的值变了(%q):菜单按字面量门控 remove/replace 入口,"+
			"改了值菜单就永久看不见那些动词而两侧都不报错", CapabilityServersEdit)
	}
	// 它必须与 CapabilityServers **分开**:合成一个的话,旧 Guardian 的
	// `servers` 声明就会被读成「remove 也能用」,而它在那一版是换出口。
	if CapabilityServersEdit == CapabilityServers {
		t.Fatal("改清单的动词与「列清单 + 切换」共用了一个能力键:旧 Guardian 会被误认为支持 remove")
	}
	for _, c := range GuardianCapabilities() {
		if c == CapabilityServersEdit {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q:%v", CapabilityServersEdit, GuardianCapabilities())
}

// 三处各写一份同样的名字比对,漂移的后果是「菜单说已经删掉了,而配置里那一行
// 还在」。现在只有一份判据,这条守卫钉的是那份判据本身认得出哪些形状 ——
// 少了 EqualFold,`bx server rm Osaka` 就静默什么都不做。
func TestSameServerNameIgnoresCaseAndSurroundingSpace(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"osaka", "osaka", true},
		{"Osaka", "osaka", true},
		{"  osaka  ", "osaka", true},
		{"\tOSAKA\n", " osaka ", true},
		{"osaka", "tokyo", false},
		{"osaka", "osaka2", false},
		{"", "osaka", false},
	} {
		if got := setup.SameServerName(tc.a, tc.b); got != tc.want {
			t.Errorf("SameServerName(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	// guardian 这一侧的查找必须与它一致 —— 分家就是「查得到却写不进去」。
	list := []config.Server{{Name: "Tokyo"}, {Name: "osaka"}}
	if !serverNamed(list, " tokyo ") {
		t.Error("serverNamed 没有走同一份判据:大小写/空白不再被忽略")
	}
	if findServerNamed(list, "OSAKA") == nil {
		t.Error("findServerNamed 没有走同一份判据")
	}
	if findServerNamed(list, "kyoto") != nil {
		t.Error("findServerNamed 认出了一台不存在的服务器")
	}
}
