package guardian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rulesTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "server: bx://x\nglobal: true\nrules:\n    - direct:\n        - '*.steamstatic.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withPeer(r *http.Request, uid uint32, got bool) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), peerCredentialsKey{}, peerCredentials{uid: uid, got: got}))
}

// **规则读写与 /v1/up、/v1/down 同一道门(owner 或 root),不是 /v1/status 那种人人可读。**
//
// 判据不是「规则比开关更敏感」—— 恰恰相反:能关掉保护的人已经能做更坏的事。
// 一致才是要点:菜单要能改规则,而菜单以 owner 身份跑。
func TestRulesEndpointRequiresOwnerOrRoot(t *testing.T) {
	handler := rulesHandler(rulesTestConfig(t), 501)
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
			handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), tc.uid, tc.got))
			if w.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// ownerUID 未配置时退化成 root-only —— **绝不因为「没配」就放宽**(与 authorizeOwnerPeer 同规矩)。
func TestRulesEndpointStaysRootOnlyWithoutOwner(t *testing.T) {
	handler := rulesHandler(rulesTestConfig(t), 0)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), 501, true))
	if w.Code != http.StatusForbidden {
		t.Fatalf("未配置 owner 时非 root 拿到了 %d", w.Code)
	}
}

func TestRulesRoundTripThroughTheEndpoint(t *testing.T) {
	path := rulesTestConfig(t)
	handler := rulesHandler(path, 501)

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(body))
		handler(w, withPeer(r, 501, true))
		return w
	}
	if w := post(`{"action":"add","kind":"direct","pattern":"*.qq.com"}`); w.Code != http.StatusOK {
		t.Fatalf("加规则 = %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"action":"remove","kind":"direct","pattern":"*.steamstatic.com"}`); w.Code != http.StatusOK {
		t.Fatalf("删规则 = %d %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), 501, true))
	body := w.Body.String()
	// **判据是 direct 列表本身,不是整个响应体的子串。**
	// 应答现在还带着每一组的**完整域名目录**(供界面把失败归因对到组上),
	// 于是 `*.steamstatic.com` 作为「可选项」必然出现在响应里 —— 而它作为
	// 「已安装的规则」应当消失。第一版拿整体子串当判据,分不开这两件事。
	var got rulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解不出应答:%v %s", err, body)
	}
	installed := strings.Join(got.Direct, " ")
	if !strings.Contains(installed, "*.qq.com") || strings.Contains(installed, "steamstatic") {
		t.Fatalf("读回来的直连规则不对:%v", got.Direct)
	}
	// **改完必须明说要重启才生效。** bx 不热重载;不说这句,用户会以为已经生效,
	// 然后在问题依旧时排除掉这一步 —— 而那正是真正的原因。
	if !strings.Contains(body, "requires_restart") {
		t.Errorf("响应没说需要重启:%s", body)
	}
}

// **写失败必须报错,不能静默成功。** 静默成功会让菜单显示「已保存」而盘上一个字没变。
func TestRulesWriteFailureIsReported(t *testing.T) {
	handler := rulesHandler(filepath.Join(t.TempDir(), "does-not-exist.yaml"), 501)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(`{"action":"add","kind":"direct","pattern":"*.a.com"}`))
	handler(w, withPeer(r, 501, true))
	if w.Code == http.StatusOK {
		t.Fatal("配置不存在却报告了成功")
	}
	// 响应体只带失败类别,**不外传原始错误串**(可能含路径/链接/凭据)——
	// 与 Guardian 其它端点同一条纪律,完整原因只进日志。
	if strings.Contains(w.Body.String(), "does-not-exist.yaml") {
		t.Errorf("响应体外传了文件路径:%s", w.Body.String())
	}
}

// 没有配置路径时如实回 501 —— 「没接线」不是「没有规则」。
func TestRulesEndpointReportsWhenNotWired(t *testing.T) {
	handler := rulesHandler("", 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), 501, true))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("未接线时 = %d, want 501", w.Code)
	}
}

// **接线守卫。** /v1/rules 改的必须是 Core 真正在读的那个文件。
//
// 两处漂开的后果是静默的:菜单说「已保存」、Guardian 日志也说改成功了,
// 而 Core 读的是另一个文件 —— 用户重启一次网络,发现规则毫无效果。
// 组装根进不去测试正是本仓库全部事故的来源,故那几行已抽成纯函数。
func TestDaemonWiresTheSameConfigPathIntoRules(t *testing.T) {
	const path = "/etc/bx/config.yaml"
	got := localAPIOptionsFor(DaemonOptions{ConfigPath: path, LocalAPIOwnerUID: 501})
	if got.ConfigPath != path {
		t.Fatalf("LocalAPI 拿到的配置路径 = %q, want %q —— 菜单会改一个 Core 不读的文件", got.ConfigPath, path)
	}
	if got.OwnerUID != 501 {
		t.Fatalf("owner uid 没接上:%d", got.OwnerUID)
	}
}

// 能力声明里必须有 rules,否则菜单认不出这一版支持编辑。
func TestRulesCapabilityIsDeclared(t *testing.T) {
	for _, c := range GuardianCapabilities() {
		if c == CapabilityRules {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q:%v", CapabilityRules, GuardianCapabilities())
}

// **按组发布,而且是三态。**
//
// 界面上一个勾选框只有开/关两态,而真实配置里一组常常**只装了一半**(用户手工
// 删过几条、或者 preset 后来加了新域名)。把半装的组画成「开」,用户会以为
// 那几条在生效;画成「关」更糟。三态是唯一诚实的表达。
func TestRulesEndpointPublishesGroupsWithThreeStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "server: bx://x\nrules:\n    - direct:\n" +
		"        - '*.icloud.com'\n" + // apple 组:只装了一部分
		"        - '*.steamstatic.com'\n" +
		"        - '*.steamcontent.com'\n" +
		"        - client-update.akamai.steamstatic.com\n" +
		"        - steamcdn-a.akamaihd.net\n" +
		"        - media.steampowered.com\n" + // gaming 组:全装了
		"        - gsa.apple.com\n" // 手写的
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	rulesHandler(path, 501)(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), 501, true))

	var got rulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("解不出应答:%v %s", err, w.Body.String())
	}
	states := map[string]string{}
	for _, g := range got.Groups {
		states[g.Name] = g.State
		if g.Title == "" {
			t.Errorf("组 %q 没有给人看的标题", g.Name)
		}
	}
	if states["gaming"] != "on" {
		t.Errorf("gaming 全装了却是 %q", states["gaming"])
	}
	if states["apple"] != "partial" {
		t.Errorf("apple 只装了一部分却是 %q —— 半装的组画成开或关都是撒谎", states["apple"])
	}
	if states["china-cdn"] != "off" {
		t.Errorf("china-cdn 一条没装却是 %q", states["china-cdn"])
	}
	// **手写的规则必须单独列出来**,否则界面无从知道哪些不该被组开关碰。
	if len(got.Custom) != 1 || got.Custom[0] != "gsa.apple.com" {
		t.Fatalf("自定义 = %v,want [gsa.apple.com]", got.Custom)
	}
	// 配置路径要发布出去 —— 「在 Finder 中显示」靠它,而菜单不该自己猜一份。
	if got.ConfigPath != path {
		t.Errorf("config_path = %q, want %q", got.ConfigPath, path)
	}
}

// 整组开关走同一个端点。
func TestRulesEndpointTogglesWholeGroups(t *testing.T) {
	path := rulesTestConfig(t)
	handler := rulesHandler(path, 501)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(body)), 501, true))
		return w
	}
	if w := post(`{"action":"enable_group","group":"apple"}`); w.Code != http.StatusOK {
		t.Fatalf("开组 = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(post(`{"action":"disable_group","group":"gaming"}`).Body.String(), `"name":"gaming"`) {
		t.Error("关组之后没有回完整列表")
	}
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), 501, true))
	var got rulesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	for _, g := range got.Groups {
		if g.Name == "apple" && g.State != "on" {
			t.Errorf("apple 没开成:%q", g.State)
		}
		if g.Name == "gaming" && g.State != "off" {
			t.Errorf("gaming 没关成:%q", g.State)
		}
	}
}

// 认不出的组名要报错,别静默什么都不做 —— 静默会让界面显示成功而配置没变。
func TestUnknownGroupIsRejected(t *testing.T) {
	handler := rulesHandler(rulesTestConfig(t), 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/rules",
		strings.NewReader(`{"action":"enable_group","group":"not-a-preset"}`)), 501, true))
	if w.Code == http.StatusOK {
		t.Fatal("接受了不存在的组名")
	}
}
