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
			//
			// **两条命令都带 sudo,理由不对称,分开说(与 internal/cli/rulereview.go
			// 的 riskyRuleFinding 同一份分析,措辞按各自站点保留):**
			//
			// `direct rm` **必须**带:editRuleAction(internal/cli/direct.go)对
			// /etc/bx/config.yaml 直接 os.ReadFile/os.WriteFile,不自提权、也不经
			// Guardian 鉴权——`sudo bx setup` 建的这份文件是 0600 属主 root(真机
			// 验证,2026-08-17,`bx status` 非 root 用户跑得动、照着这条 hint 敲的
			// `bx direct rm` 却 permission denied)。
			//
			// `down`/`up` **不总是需要,但仍然加**:macOS 上经 Guardian `/v1/up`、
			// `/v1/down`(authorizeOwnerPeer)鉴权,owner_uid 配置了(`sudo bx
			// setup` 时从 SUDO_UID 自动捕获)的机器上不需要 root。但这条 hint 跨
			// 平台共用同一份文本:**Linux/Windows 上 upAction/downAction 根本不经
			// Guardian,直接 systemctl/SCM,始终需要 root**;macOS 上 owner_uid
			// 未配置时也退化成 root-only。本项目的主平台是 Linux,不带 sudo 在那
			// 里必定 permission denied,与 `direct rm` 是同一类 bug;多带的代价
			// 只是 macOS 已配置 owner 的机器上一次不必要的密码提示——不对称,
			// 两条都印 sudo。
			Hint: fmt.Sprintf("sudo bx direct rm '%s'；改完要 sudo bx down && sudo bx up", f.Rule),
		})
	}
	return out
}
