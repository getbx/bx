// Package srvgen 生成 bx server 端的「好默认」配置:目前 REALITY(强封锁首选)。
// 纯逻辑(密钥/UUID/配置模板),免 root 可测;与内嵌 sing-box reality 互通已实测
// (crypto/ecdh X25519 + RawURLEncoding 推导的公钥 == sing-box generate reality-keypair)。
package srvgen

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
)

// DefaultRealitySNI 是 reality「偷握手」借用的真站默认值:需高流量、支持 TLS1.3+X25519、
// 我们控不了、且**证书链够小**(reality 要中继真站证书,过大会握手失败)。
// **绝不用 www.microsoft.com**——它证书过大(实测 ~3410B 叶证书),reality 借壳握手失败
// (真机 e2e 坐实:microsoft 全挂、换 cloudflare 即通,含跨 GFW 出口)。www.cloudflare.com
// 证书最小(~1322B)且已端到端验过,故选它;可被 GenerateReality 的 sni 参数(--sni)覆盖
// (备选 www.apple.com / addons.mozilla.org,见 docs/reality-server-setup.md)。
const DefaultRealitySNI = "www.cloudflare.com"

// RealityParams 是一套 reality 服务端 + 对应客户端链接所需的全部参数。
type RealityParams struct {
	Host       string // 客户端连的 server 主机(VPS IP/域名)
	Port       int    // 默认 443
	SNI        string // 借用的真站
	UUID       string // vless 用户 id
	ShortID    string // reality short id(hex)
	PrivateKey string // 服务端持有(base64url x25519);绝不进客户端链接
	PublicKey  string // 客户端 pbk(base64url x25519)
}

// realityKeypair 生成 reality 的 x25519 密钥对(base64url 编码,与 sing-box/xray 互通)。
func realityKeypair() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("生成 x25519: %w", err)
	}
	priv = base64.RawURLEncoding.EncodeToString(k.Bytes())
	pub = base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
	return priv, pub, nil
}

// uuidV4 用 crypto/rand 生成 RFC4122 v4 UUID。
func uuidV4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// GenerateReality 生成一套完整 reality 参数(SNI 默认 DefaultRealitySNI;
// port<=0 → 443:reality 用 443 最自然,只在 443 被占/防火墙受限时才换)。
func GenerateReality(host, sni string, port int) (RealityParams, error) {
	if host == "" {
		return RealityParams{}, fmt.Errorf("host 不能为空")
	}
	if sni == "" {
		sni = DefaultRealitySNI
	}
	if port <= 0 {
		port = 443
	}
	if port > 65535 {
		return RealityParams{}, fmt.Errorf("端口非法: %d", port)
	}
	uuid, err := uuidV4()
	if err != nil {
		return RealityParams{}, err
	}
	priv, pub, err := realityKeypair()
	if err != nil {
		return RealityParams{}, err
	}
	var sid [4]byte // 8 hex 字符的 short id
	if _, err := rand.Read(sid[:]); err != nil {
		return RealityParams{}, err
	}
	return RealityParams{
		Host:       host,
		Port:       port,
		SNI:        sni,
		UUID:       uuid,
		ShortID:    hex.EncodeToString(sid[:]),
		PrivateKey: priv,
		PublicKey:  pub,
	}, nil
}

// ServerConfig 生成 sing-box reality 服务端配置(vless 入站 + reality + direct 出站)。
// inbound 返回 reality vless 入站 map(供 ServerConfig 与 CombinedServerConfig 复用)。
func (p RealityParams) inbound() map[string]any {
	return map[string]any{
		"type":        "vless",
		"tag":         "reality-in",
		"listen":      "::",
		"listen_port": p.Port,
		"users":       []any{map[string]any{"uuid": p.UUID, "flow": "xtls-rprx-vision"}},
		"tls": map[string]any{
			"enabled":     true,
			"server_name": p.SNI,
			"reality": map[string]any{
				"enabled": true,
				"handshake": map[string]any{
					"server":      p.SNI, // 探测转发到真站
					"server_port": 443,
				},
				"private_key": p.PrivateKey,
				"short_id":    []any{p.ShortID},
			},
		},
	}
}

func (p RealityParams) ServerConfig() ([]byte, error) {
	return marshalServer([]any{p.inbound()})
}

// marshalServer 把若干入站包成一份完整 sing-box 服务端配置(共享 direct 出站)。
func marshalServer(inbounds []any) ([]byte, error) {
	return json.MarshalIndent(map[string]any{
		"log":       map[string]any{"level": "warn", "timestamp": false},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	}, "", "  ")
}

// CombinedServerConfig 把 reality(TCP)+ hysteria2(UDP)合成一份 sing-box 服务端配置
// (两个入站 + 共享 direct 出站)——一台 server 同时供「隐蔽 TCP + 加速 UDP」。
func CombinedServerConfig(rp RealityParams, hp HysteriaParams) ([]byte, error) {
	return marshalServer([]any{rp.inbound(), hp.inbound()})
}

// ClientLink 生成对应的客户端 vless://…reality 链接(带 bx 推荐默认 flow/fp)。
// 只含公钥(pbk),绝不含服务端私钥。
func (p RealityParams) ClientLink() string {
	q := url.Values{}
	q.Set("security", "reality")
	q.Set("pbk", p.PublicKey)
	q.Set("sid", p.ShortID)
	q.Set("sni", p.SNI)
	q.Set("flow", "xtls-rprx-vision")
	q.Set("fp", "chrome")
	// 手拼以保持参数顺序稳定、且 query 值不被过度转义(base64url 的 -_ 不该被编码)。
	return fmt.Sprintf("vless://%s@%s:%d?%s", p.UUID, p.Host, p.Port, decodeSafe(q))
}

// decodeSafe 输出 query 但保留 base64url 的 - _ 不转义(url.Values.Encode 会编码它们,
// 而分享链接惯例是原样;sni 的 . 等也无需转义)。
func decodeSafe(q url.Values) string {
	enc := q.Encode()
	// Encode 不会转义 - _ . ~,且 = & 是分隔符——对我们的取值集(base64url/hex/域名)已安全。
	return enc
}
