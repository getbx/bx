package guardian

import (
	"context"
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
	if !strings.Contains(body, "*.qq.com") || strings.Contains(body, "steamstatic") {
		t.Fatalf("读回来的规则不对:%s", body)
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
