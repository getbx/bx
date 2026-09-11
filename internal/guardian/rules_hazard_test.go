package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// postRuleRaw 打一次 /v1/rules,返回状态码与**原始**响应体(不解析)——
// 本文件的断言要检查响应体里出现/不出现哪些字节,不能先被 JSON 解码抹掉证据。
// 与 rules_reload_test.go 里同名但签名不同的 postRule(接 handler、解出结构体)
// 是两个各自服务各自断言的帮手,不合并。
func postRuleRaw(t *testing.T, path, body string) (int, string) {
	t.Helper()
	handler := rulesHandler(path, 501, func() error { return nil })
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(body)), 501, true))
	return w.Code, w.Body.String()
}

// **菜单今天能一键加进一条 CLI 明确拒绝的规则。**
//
// 2026-09-08 上线的「按应用窗口右键 → Always direct」直接打这个端点,而
// applyRuleChange 一处都不查风险:setup.AddRule 只校验类型与模式语法。
// 一个连 x.s3.amazonaws.com 的应用,右键候选里就摆着 *.s3.amazonaws.com。
func TestAddDirectRuleRefusesAWildcardOnAnOpenPlatform(t *testing.T) {
	path := rulesTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	code, body := postRuleRaw(t, path, `{"action":"add","kind":"direct","pattern":"*.s3.amazonaws.com"}`)
	if code != http.StatusConflict {
		t.Fatalf("状态码 = %d, want 409(%s)", code, body)
	}
	if !strings.Contains(body, "rules_risky_direct") {
		t.Fatalf("失败码 = %s", body)
	}
	// **响应体只带 code。** 完整理由与 pattern 只进 Guardian 日志 —— 与
	// servers_name_exists 同一条纪律。
	if strings.Contains(body, "amazonaws") {
		t.Fatalf("响应体里不该出现 pattern:%s", body)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("被拒之后盘上的配置动了")
	}
}

// force 是逃生口:显式带上就放行。它存在是为了让这道门不必做到永远正确,
// 而不是为了让人顺手点过去 —— 菜单侧把它放在次要动作上。
func TestAddDirectRuleHonoursForce(t *testing.T) {
	path := rulesTestConfig(t)
	code, body := postRuleRaw(t, path, `{"action":"add","kind":"direct","pattern":"*.s3.amazonaws.com","force":true}`)
	if code != http.StatusOK {
		t.Fatalf("带 force = %d %s", code, body)
	}
	var list rulesResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("解不出应答:%v", err)
	}
	found := false
	for _, d := range list.Direct {
		if strings.EqualFold(d, "*.s3.amazonaws.com") {
			found = true
		}
	}
	if !found {
		t.Fatalf("force 之后规则没进去:%v", list.Direct)
	}
}

// 确切主机不拦(与 CLI 同一判据),proxy 不走这道门。
func TestAddRuleLetsThroughExactHostsAndProxy(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"确切主机", `{"action":"add","kind":"direct","pattern":"bucket.s3.amazonaws.com"}`},
		{"proxy 不过这道门", `{"action":"add","kind":"proxy","pattern":"*.s3.amazonaws.com"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := postRuleRaw(t, rulesTestConfig(t), tc.body)
			if code != http.StatusOK {
				t.Fatalf("= %d %s", code, body)
			}
		})
	}
}
