package platformcheck

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本包是**采集**(允许 os/exec),但它要被 Guardian 与 CLI 两边引,所以不许
// 反向依赖任一控制面:import guardian 会成环,import cli 把它拖回单一消费方。
func TestPlatformcheckDoesNotDependOnControlPlanes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录: %v", err)
	}
	banned := map[string]string{
		"github.com/getbx/bx/internal/guardian": "guardian 要调本包,反向 import 成环",
		"github.com/getbx/bx/internal/cli":      "cli 是消费方之一,不许反向依赖",
		"github.com/getbx/bx/internal/install":  "装机层不该被诊断采集拖进来",
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, spec := range f.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q —— %s", name, path, why)
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个源文件都没检查到 —— 守卫读不懂目录结构")
	}
}

// TerminalProxyChecks 在没有任何代理环境变量时必须给一条 info(不是空切片):
// 「查了、没有」与「没查」要分得开。
func TestTerminalProxyChecksReportsUnsetAsInfo(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(k, "")
	}
	got := TerminalProxyChecks()
	if len(got) != 1 || got[0].Name != "terminal_proxy" || got[0].Status != "info" || got[0].Detail != "not set" {
		t.Fatalf("未设置代理时 = %+v", got)
	}
	t.Setenv("HTTPS_PROXY", "http://user:pw@proxy.local:3128")
	got = TerminalProxyChecks()
	if len(got) != 1 || got[0].Status != "ok" || strings.Contains(got[0].Detail, "pw@") || !strings.Contains(got[0].Detail, "<redacted>@proxy.local") {
		t.Fatalf("凭据必须脱敏:%+v", got)
	}
}
