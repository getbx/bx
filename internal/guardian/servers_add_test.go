package guardian

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
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
//
// **链接按 brook 的真实形状写(主机在 `?server=` 里,不是 authority)。** 这两条
// fixture 原先是 `brook://host:9999?password=y` —— 一条 `tunnel.ServerHost` 解不出
// 主机的链接,也就是生产里会被这个端点拒掉的东西;它们此前能绿,只是因为
// 「给了名字就不校验链接」那个缺口。同一个文件下面二十行就写着正确的形状。
func TestAddServerRefusesAnExistingName(t *testing.T) {
	path := serversTestConfig(t)
	before, _ := os.ReadFile(path)
	code, _, body := postServersAdd(t, path, `{"action":"add","name":"Tokyo","link":"brook://other.example.com?server=other.example.com%3A9999&password=y"}`)
	if code != http.StatusConflict || !strings.Contains(body, "servers_name_exists") {
		t.Fatalf("同名 = %d %s", code, body)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("拒绝之后盘上的配置动了")
	}
}

// 名字可省略:按链接推导(setup.LinkHost),应答里 added 告诉界面最终叫什么。
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
	code, resp, _ := postServersAdd(t, path, `{"action":"add","name":"office","link":"brook://o.example.com?server=o.example.com%3A9999&password=y"}`)
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

// 用户手里的链接几乎全是 bx:// 换壳 —— 推导必须解得开壳,否则「名字」会是一串
// base64。这条钉住 setup.LinkHost(而不是不解壳的 config.DeriveServerName)真的
// 接在了推导路径上。
func TestAddServerDerivesTheNameFromABxEnvelope(t *testing.T) {
	path := serversTestConfig(t)
	link := blink.Encode("vless://" + serversTestUUID + "@vps3.example.com:443?security=reality")
	code, resp, body := postServersAdd(t, path, `{"action":"add","link":"`+link+`"}`)
	if code != http.StatusOK {
		t.Fatalf("加 = %d %s", code, body)
	}
	if resp.Added != "vps3.example.com" {
		t.Fatalf("added = %q, want 解壳之后推导的名字", resp.Added)
	}
}

// **给了名字也照样要校验链接** —— 校验此前只发生在「名字省略、要按链接推导」
// 那一支上,而那是个副作用:名字一给,`setup.LinkHost` 就不再被调,任何字符串
// 都写得进配置。
//
// 后果在 replace 那边最重(见 servers_test.go 里的同款):写进**当前**那台之后,
// 下一次重连解析不出来,而界面刚刚承诺「bx picks up the new one when it
// reconnects」。add 这边只是种下一台永远切不过去的服务器 —— 但两条路走的是同一个
// helper、各一行,让 replace 严而 add 松是不自洽的。
//
// 拒绝**只发码**:链接是凭据,一个字都不回显、不进日志。
func TestAddServerValidatesTheLinkEvenWhenTheNameIsGiven(t *testing.T) {
	path := serversTestConfig(t)
	before, _ := os.ReadFile(path)
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	// **形状要挑 `setup.LinkHost` 真的解不出主机的那一种。** 它认得出的东西比
	// 想象中多(`looksLikeHost` 对一串裸字母是放行的,那会被当成主机名)——
	// 这道门守的是「主机解不出来」,不是「这条链接一定能连上」,后者只有真拨
	// 一次才知道。
	const badLink = `vless://user:supersecretpassword@ho st:443`
	code, _, body := postServersAdd(t, path, `{"action":"add","name":"osaka2","link":"`+badLink+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "servers_add_failed") {
		t.Fatalf("解不出主机的链接被收下了 = %d %s", code, body)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("拒绝之后盘上的配置动了")
	}
	if logged := buf.String(); strings.Contains(logged, badLink) || strings.Contains(logged, "supersecret") {
		t.Fatalf("日志里出现了原始链接:%s", logged)
	}
}

// 推导失败绝不许把原始链接(凭据)写进日志。url.Parse 在解析失败时会把**逐字节的
// 原始输入**塞进它的 error string——这正是本文件那句「链接不写进日志」要防的事;
// 用 setup.LinkHost(只回 bool,没有错误文本)而不是会产出这种 error 的路径,
// 从构造上就不会有东西可泄漏。这条测试直接盯着日志里到底写了什么。
func TestAddServerDeriveFailureLogsNoLinkText(t *testing.T) {
	path := serversTestConfig(t)
	orig := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	const badLink = `vless://user:supersecretpassword@ho st:443`
	code, _, body := postServersAdd(t, path, `{"action":"add","link":"`+badLink+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "servers_add_failed") {
		t.Fatalf("= %d %s", code, body)
	}
	logged := buf.String()
	if strings.Contains(logged, "supersecretpassword") {
		t.Fatalf("日志里出现了凭据:%s", logged)
	}
	if strings.Contains(logged, badLink) || strings.Contains(logged, "ho st") {
		t.Fatalf("日志里出现了原始链接:%s", logged)
	}
}
