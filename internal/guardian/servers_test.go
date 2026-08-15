package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/setup"
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
	handler := serversHandler(serversTestConfig(t), 501, noSwitch(t))
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
	serversHandler(serversTestConfig(t), 0, noSwitch(t))(
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
	serversHandler(serversTestConfig(t), 501, noSwitch(t))(
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
	serversHandler(serversTestConfig(t), 501, noSwitch(t))(
		w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/servers", nil), 501, true),
	)

	var got serversResponse
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
	})
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
	})
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
	})
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
	serversHandler(path, 501, noSwitch(t))(w, withPeer(postServers(t, "nagoya"), 501, true))
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
	serversHandler("", 501, noSwitch(t))(
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
