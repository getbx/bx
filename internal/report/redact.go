package report

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Redact 返回一份脱敏后的副本。判据(见 spec):
//   - 公网 IPv4/IPv6 → `<ip-N>`(同一个地址同一个 N,读报告的人才对得上「这两处是同一台」);
//     私网、CGNAT、link-local、loopback、TUN(198.51.100/24)、fake-IP 池(198.18/15)保留。
//   - `xx://…` 形状的链接与带 token=/key=/secret= 的查询串 → `<link>`,整段先换,里面的地址
//     不再单独露出来。
//   - 主机名 → `<host>`,只保留 bx 自己的端点(icanhazip / cloudflare trace / workers.dev)
//     与 localhost。
//
// 输入不改。
func Redact(b Bundle) Bundle {
	r := newRedactor()
	out := b
	out.Doctor = make([]Check, len(b.Doctor))
	for i, c := range b.Doctor {
		c.Detail = r.line(c.Detail)
		c.Hint = r.line(c.Hint)
		out.Doctor[i] = c
	}
	out.LogTail = make([]string, len(b.LogTail))
	for i, l := range b.LogTail {
		out.LogTail[i] = r.line(l)
	}
	out.Failure.ErrorCode = r.line(b.Failure.ErrorCode)
	return out
}

type redactor struct {
	ips map[string]int
}

func newRedactor() *redactor { return &redactor{ips: map[string]int{}} }

var (
	// 任何 scheme://… 直到空白或引号;bx/vless/hysteria2/trojan/ss/vmess/https 一律。
	linkRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	// 裸的 token=… / key=… / secret=… / password=…(链接之外也可能出现)。
	secretRe = regexp.MustCompile(`(?i)\b(token|key|secret|password|passwd|uuid)=[^\s&"']+`)
	ipv4Re   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?:/\d{1,2})?\b`)
	ipv6Re   = regexp.MustCompile(`\b(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{1,4}(?:/\d{1,3})?\b`)
	hostRe   = regexp.MustCompile(`\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:[a-z]{2,63})\b`)
)

// keepHosts 是允许原样保留的主机名后缀:bx 自己的端点,不指向用户的任何东西。
var keepHosts = []string{"icanhazip.com", "cloudflare.com", "workers.dev", "github.com", "apple.com", "localhost"}

var keepPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8",
		"198.51.100.0/24", "198.18.0.0/15", "224.0.0.0/4", "0.0.0.0/8", "255.255.255.255/32",
		"::1/128", "fe80::/10", "fc00::/7", "ff00::/8", "::/128",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func keepAddr(a netip.Addr) bool {
	for _, p := range keepPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (r *redactor) placeholder(addr netip.Addr) string {
	key := addr.String()
	n, ok := r.ips[key]
	if !ok {
		n = len(r.ips) + 1
		r.ips[key] = n
	}
	return fmt.Sprintf("<ip-%d>", n)
}

func (r *redactor) replaceAddr(match string) string {
	base, suffix := match, ""
	if i := strings.IndexByte(match, '/'); i >= 0 {
		base, suffix = match[:i], match[i:]
	}
	addr, err := netip.ParseAddr(base)
	if err != nil {
		return match // 不是地址(比如版本号 1.2.3.4 这种也会被当地址;宁可多脱不少脱)
	}
	if keepAddr(addr) {
		return match
	}
	return r.placeholder(addr) + suffix
}

// line 对一行文本脱敏,顺序承重:链接整段先换(里面的地址与主机名不再单独露出来),
// 再换裸 secret,再换地址,最后换主机名。
func (r *redactor) line(s string) string {
	if s == "" {
		return s
	}
	s = linkRe.ReplaceAllStringFunc(s, func(link string) string {
		// bx 自己端点的裸 URL(没有查询串)保留:它说明的是探测了哪个端点,不指向用户。
		if !strings.Contains(link, "?") && isKeptHost(hostOfLink(link)) {
			return link
		}
		return "<link>"
	})
	s = secretRe.ReplaceAllString(s, "$1=<redacted>")
	s = ipv4Re.ReplaceAllStringFunc(s, r.replaceAddr)
	s = ipv6Re.ReplaceAllStringFunc(s, r.replaceAddr)
	s = hostRe.ReplaceAllStringFunc(s, func(h string) string {
		if isKeptHost(h) {
			return h
		}
		return "<host>"
	})
	return s
}

func isKeptHost(h string) bool {
	lower := strings.ToLower(h)
	for _, keep := range keepHosts {
		if lower == keep || strings.HasSuffix(lower, "."+keep) {
			return true
		}
	}
	return false
}

// hostOfLink 取 scheme://host[:port]/… 里的 host(不含 userinfo);取不出来返回空(空不在保留表里)。
func hostOfLink(link string) string {
	rest := link[strings.Index(link, "://")+3:]
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
			rest = rest[at+1:]
		}
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 && !strings.Contains(rest, "]") {
		rest = rest[:i]
	}
	return rest
}
