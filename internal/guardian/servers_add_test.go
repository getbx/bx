package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serversTestConfig(tokyo / osaka,current=tokyo)与 noSwitch 都是 servers_test.go 里既有的
// 替身,这里直接复用 —— 别再定义一份同名的,同包会撞名。
func postServersAdd(t *testing.T, path, body string) (int, ServerListResponse, string) {
	t.Helper()
	handler := serversHandler(path, 501, noSwitch(t), nil, nil)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/servers", strings.NewReader(body)), 501, true))
	var resp ServerListResponse
	if w.Code == http.StatusOK {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w.Code, resp, w.Body.String()
}

// 同名不许静默覆盖:setup.AddServer 对已存在的名字会**改写链接**,那对用户是「我加了
// 一台,结果把原来那台换掉了」。Guardian 先查重、409,盘上不动。
func TestAddServerRefusesAnExistingName(t *testing.T) {
	path := serversTestConfig(t)
	before, _ := os.ReadFile(path)
	code, _, body := postServersAdd(t, path, `{"action":"add","name":"Tokyo","link":"brook://other.example.com:9999?password=y"}`)
	if code != http.StatusConflict || !strings.Contains(body, "servers_name_exists") {
		t.Fatalf("同名 = %d %s", code, body)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("拒绝之后盘上的配置动了")
	}
}

// 名字可省略:按链接推导(config.DeriveServerName),应答里 added 告诉界面最终叫什么。
func TestAddServerDerivesTheNameWhenOmitted(t *testing.T) {
	path := serversTestConfig(t)
	// brook link 的主机藏在 `?server=` 查询参数里,不是 authority(与 vless/hysteria2
	// 不同)—— 见 tunnel.ServerHost;这里按真实格式构造,好让 DeriveServerName 真的
	// 从中解出 "vps2.example.com"。
	code, resp, body := postServersAdd(t, path, `{"action":"add","link":"brook://vps2.example.com?server=vps2.example.com%3A9999&password=y"}`)
	if code != http.StatusOK {
		t.Fatalf("加 = %d %s", code, body)
	}
	if resp.Added != "vps2.example.com" {
		t.Fatalf("added = %q, want 按链接推导的名字", resp.Added)
	}
	if resp.Current != "tokyo" {
		t.Fatalf("add 不许动 current:%q", resp.Current)
	}
	if len(resp.Servers) != 3 {
		t.Fatalf("清单 = %+v", resp.Servers)
	}
}

func TestAddServerEchoesTheGivenName(t *testing.T) {
	path := serversTestConfig(t)
	code, resp, _ := postServersAdd(t, path, `{"action":"add","name":"office","link":"brook://o.example.com:9999?password=y"}`)
	if code != http.StatusOK || resp.Added != "office" {
		t.Fatalf("= %d added=%q", code, resp.Added)
	}
}

func TestAddServerRejectsAnEmptyLink(t *testing.T) {
	path := serversTestConfig(t)
	code, _, body := postServersAdd(t, path, `{"action":"add","name":"x","link":""}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "servers_add_failed") {
		t.Fatalf("空链接 = %d %s", code, body)
	}
}
