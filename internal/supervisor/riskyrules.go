package supervisor

import (
	"fmt"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
)

// riskyRuleWarnings 从配置里挑出**危险直连规则**,做成 bx status 的常驻告警。
//
// **只有这一类进 status。** 冗余与失效是建议,对任何成熟配置都不为零,放进常驻
// 面板会变墙纸、把真正要紧的这一条一起淹掉(与项目所有者否掉「Direct rules: N
// unreachable」常驻红字同一条判断)。它们留在 bx doctor 里,那是诊断命令。
//
// severity 取 warn:11338a0 之后只有 error 会把总状态降级成 Needs Attention,
// 而一条配置建议不该让一台工作正常的机器显示需注意。
//
// 与 mode 无关:公有云开放子域的去匿名化风险不因 global/split 而变。
func riskyRuleWarnings(cfg *config.Config) []stats.Warning {
	if cfg == nil {
		return nil
	}
	in := rulereview.Input{GlobalProxy: cfg.Global}
	for _, r := range cfg.Rules {
		in.Direct = append(in.Direct, r.Direct...)
	}
	var out []stats.Warning
	for _, f := range rulereview.Review(in).Findings {
		if f.Class != rulereview.ClassRisky {
			continue
		}
		out = append(out, stats.Warning{
			Name:     "risky_direct_rule",
			Severity: "warn",
			Detail:   fmt.Sprintf("直连白名单里的 %s 是公有云/开放子域平台:任何人都能注册它的子域,用一个子域让你的真实 IP 暴露", f.Rule),
			// bx direct rm(不是 remove —— 那是这条 hint 上一版的笔误,命令本身
			// 不存在,见 directCommands();用户照着敲会得到一句 usage 错误)。
			//
			// **不用括号收尾。** internal/stats/render.go 的 warningText 会把整条
			// hint 再套一层括号(`detail + " (" + hint + ")"`),hint 自己若也用
			// 括号收尾就会渲染出 `(...(改完要 bx down && bx up))` 这种嵌套 ——
			// 人读不清哪个括号对哪个。用分号分隔两个分句,渲染出来只有外层那一层。
			Hint: fmt.Sprintf("bx direct rm '%s'；改完要 bx down && bx up", f.Rule),
		})
	}
	return out
}
