package tunnel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParseTrojanLink(t *testing.T) {
	h, err := parseTrojanLink("trojan://pw123@1.2.3.4:443?sni=bing.com&fp=chrome#node1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.Password != "pw123" || h.Host != "1.2.3.4" || h.Port != 443 {
		t.Fatalf("基础字段不对: %+v", h)
	}
	if h.SNI != "bing.com" || h.Fingerprint != "chrome" {
		t.Fatalf("参数不对: %+v", h)
	}
}

// SNI 缺省回退 host;insecure 默认 false。
func TestParseTrojanLinkDefaults(t *testing.T) {
	h, err := parseTrojanLink("trojan://secret@example.com:443")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.SNI != "example.com" || h.Insecure {
		t.Fatalf("缺省不对: %+v", h)
	}
}

func TestParseTrojanLinkErrors(t *testing.T) {
	for _, bad := range []string{
		"vless://x@h:1",          // 错 scheme
		"trojan://1.2.3.4:443",   // 缺 password
		"trojan://pw@host",       // 缺端口
		"trojan://pw@h:notanum",  // 端口非数
	} {
		if _, err := parseTrojanLink(bad); err == nil {
			t.Errorf("应报错: %q", bad)
		}
	}
}

func TestTrojanSingboxConfig(t *testing.T) {
	h, _ := parseTrojanLink("trojan://pw@1.2.3.4:443?sni=bing.com&fp=chrome")
	b, err := h.singboxConfig("127.0.0.1:10800", "")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("生成的不是合法 JSON: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"trojan"`, `"server": "1.2.3.4"`, `"server_port": 443`, `"password": "pw"`, `"server_name": "bing.com"`, `"chrome"`, `"socks"`} {
		if !strings.Contains(s, want) {
			t.Errorf("配置缺 %q:\n%s", want, s)
		}
	}
}

func TestTrojanFactoryWritesConfig(t *testing.T) {
	dir := t.TempDir()
	conf := dir + "/sb.json"
	f := trojanFactory(truePath(t), "trojan://pw@1.2.3.4:443?sni=bing.com", conf, "")
	r, err := f("127.0.0.1:10844")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer r.Kill()
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "10844") || !strings.Contains(string(data), "trojan") {
		t.Errorf("config missing socks port or trojan: %s", data)
	}
}

func TestTrojanFactoryRejectsBadLink(t *testing.T) {
	f := trojanFactory(truePath(t), "vless://x@h:1", t.TempDir()+"/c.json", "")
	if _, err := f("127.0.0.1:1"); err == nil {
		t.Fatal("非 trojan 链接应报错")
	}
}
