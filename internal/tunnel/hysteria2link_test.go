package tunnel

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestParseHysteria2Link(t *testing.T) {
	h, err := parseHysteria2Link("hysteria2://pw123@1.2.3.4:8443?sni=bing.com&obfs=salamander&obfs-password=op&insecure=1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.Password != "pw123" || h.Host != "1.2.3.4" || h.Port != 8443 {
		t.Fatalf("基础字段不对: %+v", h)
	}
	if h.SNI != "bing.com" || !h.Insecure || h.Obfs != "salamander" || h.ObfsPassword != "op" {
		t.Fatalf("参数解析不对: %+v", h)
	}
}

// hy2:// 简写也认;SNI 缺省回退到 host;insecure 默认 false。
func TestParseHysteria2LinkDefaults(t *testing.T) {
	h, err := parseHysteria2Link("hy2://secret@example.com:443")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if h.SNI != "example.com" {
		t.Fatalf("SNI 缺省应回退 host, got %q", h.SNI)
	}
	if h.Insecure {
		t.Fatal("insecure 默认应 false")
	}
	if h.Obfs != "" {
		t.Fatalf("无 obfs 应为空, got %q", h.Obfs)
	}
}

func TestParseHysteria2LinkErrors(t *testing.T) {
	for _, bad := range []string{
		"vless://x@h:1",                 // 错 scheme
		"hysteria2://1.2.3.4:8443",      // 缺 password
		"hysteria2://pw@host",           // 缺端口
		"hysteria2://pw@1.2.3.4:notnum", // 端口非数
	} {
		if _, err := parseHysteria2Link(bad); err == nil {
			t.Errorf("应报错: %q", bad)
		}
	}
}

func TestHysteria2SingboxConfig(t *testing.T) {
	h, _ := parseHysteria2Link("hysteria2://pw@1.2.3.4:8443?sni=bing.com&obfs=salamander&obfs-password=op")
	b, err := h.singboxConfig("127.0.0.1:10800", "")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("生成的不是合法 JSON: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"hysteria2"`, `"server": "1.2.3.4"`, `"server_port": 8443`, `"password": "pw"`, `"server_name": "bing.com"`, `"salamander"`, `"socks"`} {
		if !strings.Contains(s, want) {
			t.Errorf("配置缺 %q:\n%s", want, s)
		}
	}
}

// 无 obfs 时不写 obfs 块。
func TestHysteria2SingboxConfigNoObfs(t *testing.T) {
	h, _ := parseHysteria2Link("hy2://pw@h:443")
	b, _ := h.singboxConfig("127.0.0.1:10800", "")
	if strings.Contains(string(b), "obfs") {
		t.Fatalf("无 obfs 不该写 obfs 块:\n%s", b)
	}
}

func TestHysteria2FactoryWritesConfig(t *testing.T) {
	dir := t.TempDir()
	conf := dir + "/sb.json"
	link := "hysteria2://pw@1.2.3.4:8443?sni=bing.com&obfs=salamander&obfs-password=op"
	f := hysteria2Factory(truePath(t), link, conf, "")
	r, err := f("127.0.0.1:10822")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer r.Kill()
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "10822") || !strings.Contains(string(data), "hysteria2") {
		t.Errorf("config missing socks port or hysteria2: %s", data)
	}
}

func TestHysteria2FactoryRejectsBadLink(t *testing.T) {
	f := hysteria2Factory(truePath(t), "vless://x@h:1", t.TempDir()+"/c.json", "")
	if _, err := f("127.0.0.1:1"); err == nil {
		t.Fatal("非 hysteria2 链接应报错")
	}
}
