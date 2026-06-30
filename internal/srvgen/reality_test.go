package srvgen

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestRealityKeypairInterop(t *testing.T) {
	priv, pub, err := realityKeypair()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// 两者都是 32 字节 base64url;pub 必须能从 priv 推导(与 sing-box 同法,已实测兼容)。
	pb, err := base64.RawURLEncoding.DecodeString(priv)
	if err != nil || len(pb) != 32 {
		t.Fatalf("priv 非 32 字节 base64url: %v len=%d", err, len(pb))
	}
	k, err := ecdh.X25519().NewPrivateKey(pb)
	if err != nil {
		t.Fatalf("priv 不是合法 x25519: %v", err)
	}
	wantPub := base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	if wantPub != pub {
		t.Fatalf("pub 与 priv 不匹配: got %q derive %q", pub, wantPub)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestGenerateRealityDefaults(t *testing.T) {
	p, err := GenerateReality("vps.example.com", "", 0)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if p.Host != "vps.example.com" {
		t.Errorf("host=%q", p.Host)
	}
	if p.Port != 443 {
		t.Errorf("默认端口应 443, got %d", p.Port)
	}
	if p.SNI == "" {
		t.Error("SNI 应有默认值")
	}
	if !uuidRe.MatchString(p.UUID) {
		t.Errorf("UUID 非合法 v4: %q", p.UUID)
	}
	if m, _ := regexp.MatchString(`^[0-9a-f]{2,16}$`, p.ShortID); !m || len(p.ShortID)%2 != 0 {
		t.Errorf("ShortID 应为偶数长 hex: %q", p.ShortID)
	}
	if p.PrivateKey == "" || p.PublicKey == "" {
		t.Error("应生成 keypair")
	}
}

// 回归守卫:默认 SNI 绝不能是 www.microsoft.com——其证书过大、reality 借壳握手必失败
// (真机 e2e 坐实:microsoft 全挂,换 cloudflare 即通)。换任何默认前须确认证书够小且端到端验过。
func TestDefaultRealitySNINotMicrosoft(t *testing.T) {
	if DefaultRealitySNI == "www.microsoft.com" {
		t.Fatal("默认 SNI 不能用 www.microsoft.com(证书过大,reality 握手失败)")
	}
	if DefaultRealitySNI == "" {
		t.Fatal("默认 SNI 不能为空")
	}
}

func TestGenerateRealityCustomSNI(t *testing.T) {
	p, _ := GenerateReality("h", "www.apple.com", 0)
	if p.SNI != "www.apple.com" {
		t.Errorf("SNI 应用自定义, got %q", p.SNI)
	}
}

func TestGenerateRealityCustomPort(t *testing.T) {
	p, err := GenerateReality("1.2.3.4", "", 9998)
	if err != nil || p.Port != 9998 {
		t.Fatalf("自定义端口 9998: port=%d err=%v", p.Port, err)
	}
	if !strings.Contains(p.ClientLink(), "@1.2.3.4:9998?") {
		t.Errorf("链接应带自定义端口: %s", p.ClientLink())
	}
	b, _ := p.ServerConfig()
	if !strings.Contains(string(b), `"listen_port": 9998`) {
		t.Error("服务端配置应监听自定义端口")
	}
	if _, err := GenerateReality("h", "", 99999); err == nil {
		t.Error("非法端口应报错")
	}
}

func TestRealityServerConfigShape(t *testing.T) {
	p, _ := GenerateReality("h", "www.cloudflare.com", 0)
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
		`"vless"`, `"reality"`, `"xtls-rprx-vision"`, p.PrivateKey, p.ShortID, p.UUID,
		`"server_name": "www.cloudflare.com"`, `"handshake"`, `"server_port": 443`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("server 配置缺 %q:\n%s", want, s)
		}
	}
	// 服务端配置绝不能含公钥派生外的客户端私密之外的东西——但必须含 private_key(服务端持私钥)。
	if strings.Contains(s, p.PublicKey) {
		t.Error("server 配置不该出现 public key(那是给客户端的 pbk)")
	}
}

func TestRealityClientLinkShape(t *testing.T) {
	p, _ := GenerateReality("1.2.3.4", "www.cloudflare.com", 0)
	link := p.ClientLink()
	if !strings.HasPrefix(link, "vless://"+p.UUID+"@1.2.3.4:443?") {
		t.Errorf("link 前缀不对: %q", link)
	}
	for _, want := range []string{
		"security=reality", "pbk=" + p.PublicKey, "sid=" + p.ShortID,
		"sni=www.cloudflare.com", "flow=xtls-rprx-vision", "fp=chrome",
	} {
		if !strings.Contains(link, want) {
			t.Errorf("client link 缺 %q: %s", want, link)
		}
	}
	// 客户端 link 必须用公钥(pbk),绝不能漏服务端私钥。
	if strings.Contains(link, p.PrivateKey) {
		t.Fatal("客户端 link 泄漏了服务端私钥!")
	}
}

func TestCombinedServerConfig(t *testing.T) {
	rp, _ := GenerateReality("1.2.3.4", "www.cloudflare.com", 443)
	hp, _ := GenerateHysteria2("1.2.3.4", "www.cloudflare.com", 443)
	b, err := CombinedServerConfig(rp, hp)
	if err != nil {
		t.Fatalf("combined: %v", err)
	}
	s := string(b)
	// 两个入站都在,reality 的私钥 + hys2 的 salamander 都在,共享一个 direct 出站
	for _, want := range []string{`"vless"`, `"reality"`, rp.PrivateKey, `"hysteria2"`, `"salamander"`, hp.Password} {
		if !strings.Contains(s, want) {
			t.Errorf("combined 配置缺 %q", want)
		}
	}
	if strings.Count(s, `"reality-in"`) != 1 || strings.Count(s, `"hy2-in"`) != 1 {
		t.Error("应恰好各一个 reality/hys2 入站")
	}
}
