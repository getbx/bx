package guardian

import (
	"errors"
	"os"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/rulereviewsrc"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
)

// errNoCoreForReview 是「Core 没在跑」的哨兵。**它不是错误路径**:体检本来就
// 常在保护关着时跑,那时死规则那一类如实显示「未检查」。
var errNoCoreForReview = errors.New("Core is not running")

// reviewRulesAt 让 **Guardian** 做规则体检。
//
// **为什么是它而不是 CLI**:体检要同时读三样东西 —— 配置(0600 root-only)、
// **Core 实际在用的那张 china 列表**(/var/lib/bx 是 drwx------)、Core 的累计
// 历史。非 root 的 `bx doctor` 三样里只读得到最后一样;Guardian 三样全读得到。
// 于是同一份判据在这里能给出**完整**的四类,而客户端那条退路只能给三类
// (它无从知道用户有没有指定自己的 china 列表 —— 拿默认表去比就是
// wrong-reference-object)。
//
// **判定与组装都不在这里**:判定在 rulereview.Review,组装在
// rulereviewsrc.Assemble —— Guardian 只负责「我有 root,我来读」。两个消费方
// 共用同一份组装,是这一步的全部要点。
//
// **读不到配置时返回 nil,不是空报告**:空报告与「你的规则都很健康」在应答上
// 完全一样,而前者是「没查」。这两者分不开,是这个功能最贵的那个教训。
func reviewRulesAt(configPath string, fetchStatus func() (stats.Report, error)) *rulereview.Report {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return nil
	}
	if fetchStatus == nil {
		fetchStatus = func() (stats.Report, error) {
			return supervisor.FetchStatusReport(supervisor.SockPath)
		}
	}
	report := rulereview.Review(rulereviewsrc.Assemble(cfg, embedded.ChinaDomain(), fetchStatus))
	return &report
}
