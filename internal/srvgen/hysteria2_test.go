package srvgen

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func TestGenerateHysteria2Defaults(t *testing.T) {
	p, err := GenerateHysteria2("vps.example.com", "", 0)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if p.Host != "vps.example.com" || p.Port != 443 {
		t.Errorf("host/port 默认不对: %+v", p)
	}
	if p.SNI == "" {
		t.Error("SNI 应有默认值")
	}
	if p.Password == "" || p.ObfsPassword == "" {
		t.Error("应生成 password + obfs 密码(salamander 默认开)")
	}
	// 自签证书必须是合法 PEM,且能解析成 x509 证书。
	block, _ := pem.Decode([]byte(p.CertPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("CertPEM 非合法证书 PEM")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Fatalf("证书解析失败: %v", err)
	}
	if kb, _ := pem.Decode([]byte(p.KeyPEM)); kb == nil {
		t.Fatalf("KeyPEM 非合法 PEM")
	}
}

func TestHysteria2ServerConfigShape(t *testing.T) {
	p, _ := GenerateHysteria2("h", "www.cloudflare.com", 0)
	b, err := p.ServerConfig()
	if err != nil {
		t.Fatalf("server cfg: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("非合法 JSON: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"hysteria2"`, p.Password, `"salamander"`, p.ObfsPassword,
		`"server_name": "www.cloudflare.com"`, "BEGIN CERTIFICATE",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("server 配置缺 %q", want)
		}
	}
}

func TestHysteria2ClientLinkShape(t *testing.T) {
	p, _ := GenerateHysteria2("1.2.3.4", "www.cloudflare.com", 0)
	link := p.ClientLink()
	if !strings.HasPrefix(link, "hysteria2://"+p.Password+"@1.2.3.4:443?") {
		t.Errorf("link 前缀不对: %q", link)
	}
	for _, want := range []string{
		"sni=www.cloudflare.com", "obfs=salamander", "obfs-password=" + p.ObfsPassword, "insecure=1",
	} {
		if !strings.Contains(link, want) {
			t.Errorf("client link 缺 %q: %s", want, link)
		}
	}
	// 客户端 link 绝不能含服务端私钥 PEM。
	if strings.Contains(link, "PRIVATE KEY") || strings.Contains(link, p.KeyPEM) {
		t.Fatal("客户端 link 泄漏了服务端私钥!")
	}
}
