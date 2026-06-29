package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func vmessURL(j string) string {
	return "vmess://" + base64.StdEncoding.EncodeToString([]byte(j))
}

// v2rayN 主流格式:字段多为字符串(port/aid 也是字符串)。
func TestParseVmessLinkStringFields(t *testing.T) {
	j := `{"v":"2","ps":"hk-node","add":"1.2.3.4","port":"443","id":"b831381d-6324-4d53-ad4f-8cda48b30811","aid":"0","scy":"auto","net":"ws","type":"none","host":"cdn.example.com","path":"/ray","tls":"tls","sni":"cdn.example.com"}`
	v, err := parseVmessLink(vmessURL(j))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Add != "1.2.3.4" || v.Port != 443 || v.ID != "b831381d-6324-4d53-ad4f-8cda48b30811" {
		t.Fatalf("基本字段不对: %+v", v)
	}
	if v.AlterID != 0 || v.Security != "auto" || v.Net != "ws" {
		t.Fatalf("scy/aid/net 不对: %+v", v)
	}
	if v.Host != "cdn.example.com" || v.Path != "/ray" || !v.TLS || v.SNI != "cdn.example.com" {
		t.Fatalf("ws/tls 字段不对: %+v", v)
	}
}

// 部分面板用 JSON 数字(port/aid 为 number)。
func TestParseVmessLinkNumericFields(t *testing.T) {
	j := `{"add":"h.example.com","port":8443,"id":"uuid-x","aid":0,"net":"tcp"}`
	v, err := parseVmessLink(vmessURL(j))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Port != 8443 || v.AlterID != 0 || v.Net != "tcp" {
		t.Fatalf("数字字段不对: %+v", v)
	}
	if v.Security != "auto" { // scy 缺省回退 auto
		t.Fatalf("scy 缺省应 auto: %+v", v)
	}
}

func TestParseVmessLinkErrors(t *testing.T) {
	for _, bad := range []string{
		"trojan://x@h:1",                                 // 错 scheme
		"vmess://!!!notbase64!!!",                        // 非 base64
		vmessURL(`{"add":"h","id":"x"}`),                 // 缺 port
		vmessURL(`{"port":"443","id":"x"}`),              // 缺 add
		vmessURL(`{"add":"h","port":"443"}`),             // 缺 id
		vmessURL(`{"add":"h","port":"notnum","id":"x"}`), // port 非数
	} {
		if _, err := parseVmessLink(bad); err == nil {
			t.Errorf("应报错: %q", bad)
		}
	}
}

func TestVmessSingboxConfigWS(t *testing.T) {
	j := `{"add":"1.2.3.4","port":"443","id":"uuid-x","aid":"0","scy":"auto","net":"ws","host":"cdn.example.com","path":"/ray","tls":"tls","sni":"cdn.example.com"}`
	v, _ := parseVmessLink(vmessURL(j))
	b, err := v.singboxConfig("127.0.0.1:10800", "")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("非合法 JSON: %v", err)
	}
	str := string(b)
	for _, want := range []string{
		`"vmess"`, `"server": "1.2.3.4"`, `"server_port": 443`, `"uuid": "uuid-x"`,
		`"security": "auto"`, `"ws"`, `"path": "/ray"`, `"cdn.example.com"`, `"socks"`,
	} {
		if !strings.Contains(str, want) {
			t.Errorf("config 缺 %q:\n%s", want, str)
		}
	}
}

func TestVmessSingboxConfigPlainTCPNoTLS(t *testing.T) {
	j := `{"add":"1.2.3.4","port":"9000","id":"uuid-x","net":"tcp"}`
	v, _ := parseVmessLink(vmessURL(j))
	b, _ := v.singboxConfig("127.0.0.1:10800", "")
	str := string(b)
	if strings.Contains(str, `"tls"`) {
		t.Errorf("无 tls 的 vmess 不应带 tls 块:\n%s", str)
	}
	if strings.Contains(str, `"transport"`) {
		t.Errorf("net=tcp 不应带 transport 块:\n%s", str)
	}
}

func TestVmessFactoryWritesConfig(t *testing.T) {
	j := `{"add":"1.2.3.4","port":"443","id":"uuid-x","net":"tcp"}`
	dir := t.TempDir()
	conf := dir + "/sb.json"
	f := vmessFactory(truePath(t), vmessURL(j), conf, "")
	r, err := f("127.0.0.1:10866")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer r.Kill()
}

func TestVmessHost(t *testing.T) {
	j := `{"add":"203.0.113.10","port":"443","id":"uuid-x","net":"tcp"}`
	h, err := VmessHost(vmessURL(j))
	if err != nil || h != "203.0.113.10" {
		t.Fatalf("VmessHost=%q err=%v", h, err)
	}
}
