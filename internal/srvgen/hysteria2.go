package srvgen

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// HysteriaParams 是一套 hysteria2 服务端 + 对应客户端链接所需的全部参数。
// hysteria2 是「速度档」(QUIC/UDP);bx 默认开 salamander 混淆对抗 QUIC SNI 检测/限速。
// TLS 用自签证书 + 客户端 insecure=1(个人隧道惯例):密码做服务端认证,TLS 主要做传输/混淆,
// 自签即可;省去 ACME 域名/证书的重型依赖,契合「零配置好默认」。
type HysteriaParams struct {
	Host         string
	Port         int    // 默认 443(UDP;与 reality 的 443/TCP 不冲突)
	SNI          string // QUIC ClientHello 里的 SNI + 自签证书 CN
	Password     string // hysteria2 认证密码
	ObfsPassword string // salamander 混淆密码
	CertPEM      string // 自签证书(服务端用)
	KeyPEM       string // 自签私钥(服务端持有;绝不进客户端链接)
}

// randB64 生成 n 字节随机数据的 base64url 字符串(用作密码)。
func randB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// selfSignedCert 为 sni 生成一张自签 ECDSA P-256 证书(PEM)。
func selfSignedCert(sni string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sni},
		DNSNames:     []string{sni},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0), // 10 年,自签免续期
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

// GenerateHysteria2 生成一套完整 hysteria2 参数(SNI 默认 DefaultRealitySNI,
// salamander 混淆默认开,自签证书;port<=0 → 443/UDP)。
func GenerateHysteria2(host, sni string, port int) (HysteriaParams, error) {
	if host == "" {
		return HysteriaParams{}, fmt.Errorf("host 不能为空")
	}
	if sni == "" {
		sni = DefaultRealitySNI
	}
	if port <= 0 {
		port = 443
	}
	if port > 65535 {
		return HysteriaParams{}, fmt.Errorf("端口非法: %d", port)
	}
	pw, err := randB64(16)
	if err != nil {
		return HysteriaParams{}, err
	}
	obfsPw, err := randB64(16)
	if err != nil {
		return HysteriaParams{}, err
	}
	cert, key, err := selfSignedCert(sni)
	if err != nil {
		return HysteriaParams{}, err
	}
	return HysteriaParams{
		Host:         host,
		Port:         port,
		SNI:          sni,
		Password:     pw,
		ObfsPassword: obfsPw,
		CertPEM:      cert,
		KeyPEM:       key,
	}, nil
}

// ServerConfig 生成 sing-box hysteria2 服务端配置(内联自签证书 + salamander obfs + direct 出站)。
func (p HysteriaParams) ServerConfig() ([]byte, error) {
	cfg := map[string]any{
		"log": map[string]any{"level": "warn", "timestamp": false},
		"inbounds": []any{map[string]any{
			"type":        "hysteria2",
			"tag":         "hy2-in",
			"listen":      "::",
			"listen_port": p.Port,
			"users":       []any{map[string]any{"password": p.Password}},
			"obfs":        map[string]any{"type": "salamander", "password": p.ObfsPassword},
			"tls": map[string]any{
				"enabled":     true,
				"server_name": p.SNI,
				"certificate": p.CertPEM,
				"key":         p.KeyPEM,
			},
		}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// ClientLink 生成对应客户端 hysteria2://… 链接(salamander obfs + insecure=1 对应自签证书)。
// 只含密码/obfs 密码,绝不含服务端私钥。
func (p HysteriaParams) ClientLink() string {
	q := url.Values{}
	q.Set("sni", p.SNI)
	q.Set("obfs", "salamander")
	q.Set("obfs-password", p.ObfsPassword)
	q.Set("insecure", "1")
	return fmt.Sprintf("hysteria2://%s@%s:%d?%s", p.Password, p.Host, p.Port, q.Encode())
}
