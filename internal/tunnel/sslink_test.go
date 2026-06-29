package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// SIP002:ss://base64url(method:password)@host:port#tag
func TestParseSSLinkSIP002(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw123"))
	link := "ss://" + userinfo + "@1.2.3.4:8388#hk-node"
	s, err := parseSSLink(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Method != "aes-256-gcm" || s.Password != "pw123" || s.Host != "1.2.3.4" || s.Port != 8388 {
		t.Fatalf("字段不对: %+v", s)
	}
}

// legacy:ss://base64(method:password@host:port)#tag
func TestParseSSLinkLegacy(t *testing.T) {
	blob := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret@example.com:443"))
	s, err := parseSSLink("ss://" + blob + "#node")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Method != "chacha20-ietf-poly1305" || s.Password != "secret" || s.Host != "example.com" || s.Port != 443 {
		t.Fatalf("legacy 字段不对: %+v", s)
	}
}

// 密码含 ':'(只在第一个冒号切 method/password)。
func TestParseSSLinkPasswordWithColon(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p:a:ss"))
	s, err := parseSSLink("ss://" + userinfo + "@h:1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Method != "aes-128-gcm" || s.Password != "p:a:ss" {
		t.Fatalf("含冒号密码切错: %+v", s)
	}
}

func TestParseSSLinkErrors(t *testing.T) {
	good := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw"))
	for _, bad := range []string{
		"vless://x@h:1",            // 错 scheme
		"ss://" + good + "@host",   // 缺端口
		"ss://" + good + "@h:notn", // 端口非数
		"ss://@1.2.3.4:8388",       // 空 userinfo
	} {
		if _, err := parseSSLink(bad); err == nil {
			t.Errorf("应报错: %q", bad)
		}
	}
}

func TestSSSingboxConfig(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw123"))
	s, _ := parseSSLink("ss://" + userinfo + "@1.2.3.4:8388")
	b, err := s.singboxConfig("127.0.0.1:10800", "")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("非合法 JSON: %v", err)
	}
	str := string(b)
	for _, want := range []string{`"shadowsocks"`, `"server": "1.2.3.4"`, `"server_port": 8388`, `"method": "aes-256-gcm"`, `"password": "pw123"`, `"socks"`} {
		if !strings.Contains(str, want) {
			t.Errorf("配置缺 %q:\n%s", want, str)
		}
	}
}

func TestSSFactoryWritesConfig(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw"))
	dir := t.TempDir()
	conf := dir + "/sb.json"
	f := ssFactory(truePath(t), "ss://"+userinfo+"@1.2.3.4:8388", conf, "")
	r, err := f("127.0.0.1:10855")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer r.Kill()
	data, _ := os.ReadFile(conf)
	if !strings.Contains(string(data), "10855") || !strings.Contains(string(data), "shadowsocks") {
		t.Errorf("config 缺 socks 口或 shadowsocks: %s", data)
	}
}
