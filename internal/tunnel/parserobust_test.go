package tunnel

import (
	"encoding/base64"
	"strings"
	"testing"
)

// 链接解析器是 bx 的不可信输入边界:用户把任意分享链接甩进 bx setup。
// 任何 panic 都会让 setup 崩,故所有解析器对垃圾输入必须返回 error、绝不 panic。

// adversarialLinks 是一批刻意构造的恶劣输入(截断 base64、非 JSON、超大、奇异 unicode 等)。
func adversarialLinks() []string {
	big := strings.Repeat("A", 1<<16)
	return []string{
		"", " ", "://", "ss://", "vmess://", "trojan://", "vless://", "hysteria2://", "hy2://",
		"ss://@", "ss://@:", "ss://:@:", "ss://!!!", "ss://" + strings.Repeat("@", 100),
		"ss://" + base64.StdEncoding.EncodeToString([]byte("nocolon")),
		"ss://" + base64.StdEncoding.EncodeToString([]byte(":")),
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("m:p")), // 缺 @host:port
		"vmess://", "vmess://!!!notbase64", "vmess://" + base64.StdEncoding.EncodeToString([]byte("not json")),
		"vmess://" + base64.StdEncoding.EncodeToString([]byte("[]")),                       // JSON 非对象
		"vmess://" + base64.StdEncoding.EncodeToString([]byte("null")),                     // null
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"port":{}}`)),              // 字段类型异常
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"h","port":[1,2]}`)), // port 是数组
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"h","port":1e999}`)), // 超大数
		"trojan://@h:1", "trojan://:@:", "trojan://p@h:99999999999999999999",
		"vless://@h", "vless://x@h:notaport",
		"hysteria2://@", "hy2://p@h:-1",
		big, "ss://" + big, "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"`+big+`"}`)),
		"ss://\x00\x01\x02", "vmess://\xff\xfe", "trojan://p@\x00:1",
		"ss://%%%@h:1", "vmess://%zz",
	}
}

func TestParsersNeverPanicOnGarbage(t *testing.T) {
	for _, link := range adversarialLinks() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on %q: %v", truncate(link), r)
				}
			}()
			// 任一解析器都不该 panic;返回值无所谓(垃圾输入返回 error 即可)。
			_, _ = parseSSLink(link)
			_, _ = parseVmessLink(link)
			_, _ = parseTrojanLink(link)
			_, _ = parseVlessLink(link)
			_, _ = parseHysteria2Link(link)
			// 派生 API 也过一遍(host 抽取走完整解析路径)。
			_, _ = SSHost(link)
			_, _ = VmessHost(link)
			_ = Kind(link)
			_ = IsClientLink(link)
		}()
	}
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "...(" + itoa(len(s)) + "B)"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// FuzzParseSSLink:Go 原生模糊,自动探 panic(`go test -fuzz=FuzzParseSSLink`)。
func FuzzParseSSLink(f *testing.F) {
	for _, s := range adversarialLinks() {
		f.Add(s)
	}
	f.Add("ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw")) + "@1.2.3.4:8388#x")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseSSLink(s)
		_, _ = SSHost(s)
	})
}

// FuzzParseVmessLink:vmess base64-JSON 解析的模糊(最易 panic 的一处)。
func FuzzParseVmessLink(f *testing.F) {
	for _, s := range adversarialLinks() {
		f.Add(s)
	}
	f.Add("vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"1.2.3.4","port":"443","id":"x","net":"ws","tls":"tls"}`)))
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseVmessLink(s)
		_, _ = VmessHost(s)
	})
}
