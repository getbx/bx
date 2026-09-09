package cli

import (
	"context"
	"time"

	"github.com/getbx/bx/internal/stats"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/rulereviewsrc"
)

// doctorFinding 是一行 doctor 输出的三段式,与 doctorLine / rep.addCheck 的形参同构。
// 单独成型是为了让「说什么」可以被单测,而「怎么打印」留在 doctorAction 里。
//
// **判据本体已搬进 internal/doctor**(供 CLI 与 Guardian 共用);这里留的是薄壳,
// 让既有调用点与测试不动。
type doctorFinding = doctor.Finding

// guardianRulesForDoctor 是「向 Guardian 要规则 + 当前是不是 global」的那一跳,
// 做成变量是为了让**接线**可测:这个仓库全部事故都在接线,而判据写好了、
// doctor 那条路上没人调,与没写完全一样且不会有任何东西报错。
//
// global 取自控制 socket 的模式标签,不是猜的 —— 拿错 mode 与拿错 china 列表
// 是同一形状的事故(rulereview.Input.GlobalProxy 的注释里记着那次真机 bug)。
var guardianRulesForDoctor = func() (review *rulereview.Report, configPath string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	list, err := guardian.NewClient(guardian.SocketPath).Rules(ctx)
	if err != nil {
		return nil, "", err
	}
	return list.Review, list.ConfigPath, nil
}

// deadRulesCheckName 是死规则那一行的 check 名。
//
// **单独一个常量**:这一支已经因为同名 check 静默丢过一次安全结论 —— check 名的
// 整个存在理由是「按名字取」,重名会让消费方只拿到其中一条。仓库级重名守卫
// TestDoctorReportHasNoDuplicateCheckNames 盯着这件事。
const deadRulesCheckName = doctor.DeadRulesCheckName

// ruleReviewDoctorLines 把体检报告翻成 doctor 的行。判据本体在
// internal/doctor.RuleReviewLines,这里只是薄壳,供 CLI 既有调用点使用。
func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding { return doctor.RuleReviewLines(rep) }

// ruleReviewCheckName 把人话 key 换成 JSON 里稳定的 snake_case 名 ——
// agent 与 MCP 按名字取,名字变了就是接口变了。
func ruleReviewCheckName(key string) string { return doctor.RuleReviewCheckName(key) }

// buildRuleReviewInput 是共享组装(internal/rulereviewsrc)的薄壳。
//
// **保留这个名字而不是逐个改调用点**,是因为它在 doctor 的两条路径上各被调一次,
// 而那两处的上下文注释都点着这个名字;把组装搬走的同时改名,会让那些注释指向
// 一个不存在的东西 —— 本仓库为「关于代码的陈述失效」纠过多轮。
func buildRuleReviewInput(cfg *config.Config, embeddedChina []byte, fetchStatus func() (stats.Report, error)) rulereview.Input {
	return rulereviewsrc.Assemble(cfg, embeddedChina, fetchStatus)
}
