package guardian

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/stats"
)

// known-gaps D「规则窗口开着时每次刷新都让 Guardian 重建一张约 12k 条的 DomainSet:没量过」。
// 这里量的就是那一次请求在 Guardian 里做的全部判据工作:读配置、Parse、拿内嵌 china 全表
// 组装、Review(Core 那一跳用桩,它是一次本机 socket 往返,另算)。配置按一台真实机器的量级
// 造:两组各 40 条 direct/proxy 规则。
//
//	go test ./internal/guardian/ -run '^$' -bench RuleReviewRequest -benchmem
func BenchmarkRuleReviewRequest(b *testing.B) {
	var direct, proxy []string
	for i := 0; i < 40; i++ {
		direct = append(direct, fmt.Sprintf("'*.direct%d.example'", i))
		proxy = append(proxy, fmt.Sprintf("'*.proxy%d.example'", i))
	}
	yaml := "server: brook://example.invalid\nrules:\n  - direct: [" + strings.Join(direct, ", ") + "]\n    proxy: [" + strings.Join(proxy, ", ") + "]\n"
	path := filepath.Join(b.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		b.Fatal(err)
	}
	status := func() (stats.Report, error) { return stats.Report{}, nil }
	if reviewRulesAt(path, status) == nil {
		b.Fatal("review returned nil: the benchmark would measure an early exit")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reviewRulesAt(path, status)
	}
}
