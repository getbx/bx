package platformcheck

import (
	"os"
	"strings"

	"github.com/getbx/bx/internal/doctor"
)

// Check 与 doctor.Check 是同一个类型:平台检查的产物直接进 doctor.Facts.Platform,
// 两边(CLI / Guardian)不必各转一次。
type Check = doctor.Check

func TerminalProxyChecks() []Check {
	var values []string
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values = append(values, key+"="+redactProxyValue(value))
		}
	}
	if len(values) == 0 {
		return []Check{{Name: "terminal_proxy", Status: "info", Detail: "not set"}}
	}
	return []Check{{Name: "terminal_proxy", Status: "ok", Detail: truncateDetail(strings.Join(values, "; "))}}
}

func redactProxyValue(value string) string {
	if i := strings.LastIndex(value, "@"); i > 0 {
		scheme := ""
		if j := strings.Index(value[:i], "://"); j >= 0 {
			scheme = value[:j+3]
		}
		return scheme + "<redacted>@" + value[i+1:]
	}
	return value
}

func truncateDetail(s string) string {
	s = strings.TrimSpace(oneLine(s))
	if len(s) <= 180 {
		return s
	}
	return s[:177] + "..."
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
