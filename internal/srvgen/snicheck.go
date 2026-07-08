package srvgen

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"
)

// realityCertChainWarnBytes 是 reality 借用 SNI 证书链的告警阈值。实测:
// cloudflare 2505 / apple 3230 / google 3543 / mozilla 4085(均工作)、microsoft 5879(挂)。
// 4500 干净地把推荐站与 microsoft 分开;超过它 reality 借壳握手有较大失败风险。
const realityCertChainWarnBytes = 4500

// realitySNIAdvice 由 SNI 的 TLS 探测结果产出 reality 适配告警(纯函数,可测)。
// dialErr!=nil:连不上或不支持 TLS1.3+X25519(reality 硬要求)。certChainBytes:证书链总字节。
func realitySNIAdvice(sni string, dialErr error, certChainBytes int) []string {
	if dialErr != nil {
		return []string{fmt.Sprintf("⚠ 无法以 TLS1.3+X25519 连到 %s:443(reality 二者皆必需):%v\n   换个 SNI 或确认它可达且支持 TLS1.3+X25519。", sni, dialErr)}
	}
	if certChainBytes > realityCertChainWarnBytes {
		return []string{fmt.Sprintf("⚠ %s 证书链 %dB 偏大(>%dB),reality 借壳握手很可能失败(www.microsoft.com 实测就挂)。\n   建议换更小证书的站:www.cloudflare.com / www.apple.com / addons.mozilla.org。", sni, certChainBytes, realityCertChainWarnBytes)}
	}
	return nil
}

// CheckRealitySNI 实连 sni:443,验 reality 适配性(TLS1.3+X25519 + 证书链不过大)。
// 返回告警(空=没问题)。best-effort:网络问题也只是告警,不阻断安装。
func CheckRealitySNI(sni string) []string {
	d := &net.Dialer{Timeout: 8 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(sni, "443"), &tls.Config{
		ServerName:       sni,
		MinVersion:       tls.VersionTLS13,          // 成功即证明 TLS1.3
		CurvePreferences: []tls.CurveID{tls.X25519}, // 只提 X25519,成功即证明它支持
	})
	if err != nil {
		return realitySNIAdvice(sni, err, 0)
	}
	defer conn.Close()
	total := 0
	for _, c := range conn.ConnectionState().PeerCertificates {
		total += len(c.Raw)
	}
	return realitySNIAdvice(sni, nil, total)
}
