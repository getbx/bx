package guardian

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func postRule(t *testing.T, handler http.HandlerFunc, body string) (int, rulesResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(body))
	handler(w, withPeer(r, 501, true))
	var got rulesResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("解不出应答:%v %s", err, w.Body.String())
		}
	}
	return w.Code, got
}

// **规则改完就热生效,应答如实说「不用重连」。**
//
// 此前菜单那句「bx applies routing rules when it reconnects」是常量,不是事实:
// `bx direct add` 早就经 /v0/reload 热重载,只是 Guardian 这条路从来没调过它。
// 让 Guardian 也走同一条路,菜单加的规则与命令行加的一样立刻生效。
func TestRuleChangeIsHotAppliedWhenCoreReloads(t *testing.T) {
	reloads := 0
	handler := rulesHandler(rulesTestConfig(t), 501, func() error { reloads++; return nil })
	code, got := postRule(t, handler, `{"action":"add","kind":"direct","pattern":"*.qq.com"}`)
	if code != http.StatusOK {
		t.Fatalf("加规则 = %d", code)
	}
	if reloads != 1 {
		t.Fatalf("Core 被叫醒重载了 %d 次, want 1", reloads)
	}
	if got.RequiresRestart {
		t.Fatal("Core 已经重载,应答却仍说要重连 —— 用户会白断一次网")
	}
}

// **重载失败不回滚写盘,但必须如实说要重连。** 规则已经落盘,下次重连就生效;
// 说成「已生效」会让用户在问题依旧时把这一步排除掉。
func TestRuleChangeReportsRestartWhenReloadFails(t *testing.T) {
	path := rulesTestConfig(t)
	handler := rulesHandler(path, 501, func() error { return errors.New("core.sock: connection refused") })
	code, got := postRule(t, handler, `{"action":"add","kind":"direct","pattern":"*.qq.com"}`)
	if code != http.StatusOK {
		t.Fatalf("规则已落盘,重载失败不该把整次改动报成失败:%d", code)
	}
	if !got.RequiresRestart {
		t.Fatal("Core 没重载成,应答却说不用重连")
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "*.qq.com") {
		t.Fatalf("重载失败把已写的规则弄丢了:\n%s", body)
	}
}

// 没接重载(旧组装、或测试替身)时退回原来的答案:要重连。
// **nil 是「没接线」,不是「不用重载」。**
func TestRuleChangeWithoutReloadWiringRequiresRestart(t *testing.T) {
	handler := rulesHandler(rulesTestConfig(t), 501, nil)
	_, got := postRule(t, handler, `{"action":"add","kind":"direct","pattern":"*.qq.com"}`)
	if !got.RequiresRestart {
		t.Fatal("没接重载却说不用重连")
	}
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/rules", nil), 501, true))
	var list rulesResponse
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if !list.RequiresRestart {
		t.Fatal("GET 也该如实说这一版改完要重连")
	}
}

// 写盘失败就没有东西可重载 —— 叫醒 Core 只会多一行误导人的日志。
func TestReloadIsNotCalledWhenTheWriteFails(t *testing.T) {
	reloads := 0
	handler := rulesHandler(filepath.Join(t.TempDir(), "missing.yaml"), 501, func() error { reloads++; return nil })
	code, _ := postRule(t, handler, `{"action":"add","kind":"direct","pattern":"*.qq.com"}`)
	if code == http.StatusOK {
		t.Fatal("配置不存在却报告了成功")
	}
	if reloads != 0 {
		t.Fatalf("写盘失败仍叫了 %d 次重载", reloads)
	}
}

// 生产那份重载函数真的打 Core 的 /v0/reload —— 用一个假 Core 接住它。
func TestReloadCoreRulesPostsToCoreReload(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "bxg-reload-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "core.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	hits := make(chan string, 4)
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/reload", func(w http.ResponseWriter, r *http.Request) {
		hits <- r.Method
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "reloaded"})
	})
	go func() { _ = http.Serve(ln, mux) }()
	if err := reloadCoreRulesAt(sock); err != nil {
		t.Fatalf("reload: %v", err)
	}
	select {
	case m := <-hits:
		if m != http.MethodPost {
			t.Fatalf("/v0/reload 收到的是 %s, want POST", m)
		}
	default:
		t.Fatal("Core 的 /v0/reload 一次都没被打到")
	}
}

// **接线守卫。** daemon 组装出来的 LocalAPI 必须带生产那份重载,而不是 nil
// (nil 会让每一次菜单改规则都退回「去重连」—— 与没做这个功能在输出上一模一样)。
func TestDaemonWiresRuleReloadIntoLocalAPI(t *testing.T) {
	got := localAPIOptionsFor(DaemonOptions{ConfigPath: "/etc/bx/config.yaml", LocalAPIOwnerUID: 501})
	if got.ReloadRules == nil {
		t.Fatal("LocalAPI 没接规则重载")
	}
	if reflect.ValueOf(got.ReloadRules).Pointer() != reflect.ValueOf(reloadCoreRules).Pointer() {
		t.Fatal("接上的不是生产那份 reloadCoreRules")
	}
}

// NewLocalAPI 必须把 ReloadRules 真的交给 /v1/rules —— 只在 rulesHandler 上测
// 证明不了组装根接对了。
func TestLocalAPIPassesReloadRulesToTheRulesEndpoint(t *testing.T) {
	reloads := 0
	api := NewLocalAPI(&fakeController{}, LocalAPIOptions{
		OwnerUID:    501,
		ConfigPath:  rulesTestConfig(t),
		ReloadRules: func() error { reloads++; return nil },
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/rules", strings.NewReader(`{"action":"add","kind":"direct","pattern":"*.qq.com"}`))
	api.ServeHTTP(w, withPeer(r, 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d %s", w.Code, w.Body.String())
	}
	if reloads != 1 {
		t.Fatalf("经 NewLocalAPI 的改动没有触发重载(%d 次)", reloads)
	}
}
