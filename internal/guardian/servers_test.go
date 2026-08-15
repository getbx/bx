package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/setup"
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
func TestServerAddRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  serversRequest
	}{
		{"没有名字", serversRequest{Action: "add", Link: "vless://x@203.0.113.30:443"}},
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
		if entry.Probe.Reachable {
			t.Errorf("%s 测失败却报成可达", entry.Name)
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

// **吞吐只挂在当前那台上,而且只在真观测到的时候挂。**
//
// 吞吐是被动观测:没在用的服务器没有产生过流量,给它一个数就是编。
//
// **直接测 attachThroughput,不测 HTTP 应答体。** 走应答体的那一版被变异验证
// 当场证伪:`peak_bps` 带 omitempty,0 本来就不上线,于是「体里没有它」这条
// 断言永远成立 —— 测的是 omitempty,不是这个函数。而当前那台恰好排第一,
// 「给所有人都挂上」同样观察不到差别。两个坑都是「测试测的是相邻的东西」。
func TestThroughputOnlyLandsOnTheCurrentServer(t *testing.T) {
	entries := []ServerEntry{
		{Name: "tokyo"},
		{Name: "osaka", Current: true},
		{Name: "nagoya"},
	}
	attachThroughput(entries, func() (int64, bool) { return 3_100_000, true }, nil, thBase)

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
		name       string
		throughput throughputReader
	}{
		{"没接线", nil},
		{"没观测到", func() (int64, bool) { return 0, false }},
		{"观测到 0", func() (int64, bool) { return 0, true }},
		{"观测到负数", func() (int64, bool) { return -1, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := []ServerEntry{{Name: "tokyo", Current: true, PeakBPS: 7}}
			attachThroughput(entries, tc.throughput, nil, thBase)
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
	attachThroughput(entries, nil, history, thBase)

	if entries[1].PeakBPS != 8_000_000 {
		t.Fatalf("历史没挂上:%d", entries[1].PeakBPS)
	}
	if entries[1].PeakAgeSeconds != int64(2*time.Hour/time.Second) {
		t.Fatalf("年龄 = %d 秒,want 7200 —— 不带年龄的历史数字读起来像现状",
			entries[1].PeakAgeSeconds)
	}
}

// 当前那台的**实时**观测压过历史,年龄归零。
func TestLiveThroughputOverridesHistoryForTheCurrentServer(t *testing.T) {
	entries := []ServerEntry{{Name: "tokyo", Current: true}}
	history := map[string]throughputEntry{
		"tokyo": {PeakBPS: 8_000_000, ObservedAt: thBase.Add(-2 * time.Hour)},
	}
	attachThroughput(entries, func() (int64, bool) { return 1_000_000, true }, history, thBase)

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
	attachThroughput(entries, nil, history, thBase)

	if entries[0].PeakBPS != 0 || entries[0].PeakAgeSeconds != 0 {
		t.Fatalf("未来时刻的观测被报了出来:%d bps / %d 秒",
			entries[0].PeakBPS, entries[0].PeakAgeSeconds)
	}
}
