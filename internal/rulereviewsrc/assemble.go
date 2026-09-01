// Package rulereviewsrc 把一份 config 摊成规则体检的**原料**。
//
// **为什么单独成包**:判据(internal/rulereview)是纯的 —— purity_test.go 按
// AST 禁掉 net/os/exec,所以读配置、读 china 列表、问 Core 要累计历史这些事
// 不能住在那里。而它们又必须**只有一份**:消费方有两个 —— `bx doctor`
// (root 时直接读文件)与 Guardian(它有 root,替非 root 的调用方算)——
// 两边各写一份组装,迟早会拿不同的参照物比出不同的结论,而那正是本仓库记档过的
// wrong-reference-object 事故形状(判据没错、读错了输入)。
//
// 本包**不做判定**:只负责「拿哪一份 china 列表、哪些规则、什么模式、有没有
// 累计历史」,判定仍然全在 rulereview.Review 里。
package rulereviewsrc

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
)

// Assemble 把一份 config 摊成体检的原料。
//
// **china 列表参照物的解析必须和 Core 用的是同一套算法**(wrong-reference-object
// 修复,2026-08-17)。此前这里恒用编译进二进制的内嵌快照——但 Core 真正比对的
// 从来不是它,是 provision.EnsureLists 落盘、internal/supervisor 定时经隧道刷新的
// dataDir/china_domain.txt(用户在 lists.china_domain 指了自己的文件时,是那个
// 文件,见 internal/supervisor/run.go 的 domainOverride)。若上游从列表里**删掉**
// 一个域名,内嵌快照仍带着它,拿它比就会指着一条**仍然生效**的手写规则说「已被
// 内建列表覆盖,可以删」——这是「判据没错、读错了输入」那类事故,而不是新判据。
//
// 三种结局,报告里都能分清用的是哪一份(见 ruleReviewDoctorLines/builtinListLines):
//  1. 读到 Core 实际会用的那个文件(用户没设 lists.china_domain 时是 dataDir 下的
//     默认路径,设了就是那个路径)⇒ 用它比,报告点名这是「Core 当前实际使用的」。
//  2. **默认路径**读不到(非 root 进不去 /var/lib/bx,或 Core 从没跑过 provision)
//     ⇒ 回落内嵌快照,报告明说「回落」——一份可能过期的快照仍然有用,但用户必须
//     能识别它可能过期,不能被当成等价于实时数据(这条由 ChinaFallback 单独携带,
//     不靠解析 ChinaSource 的措辞)。
//  3. **用户在 lists.china_domain 指定的**路径读不到 ⇒ 不比,说明为什么。这里
//     刻意不回落内嵌快照——那是拿用户明确换掉的参照物硬凑数,不是「可能过期的
//     同一份东西」,这是 review 抓到的两处 wrong-reference-object 里的另一处。
//
// **另一处容易读错的字段,由测试钉着**:global 取 cfg.Global(yaml `global:`)。
// cfg.Mode 是另一个东西,取值只有 host|router;spec 当天的真机 bug 就是拿错列表
// 比,而拿错 mode 是同一形状。
// CoreRuleHistory 从 Core 的状态报告里取跨重启累计历史。
//
// **Core 没在跑不是错误,是常态**(体检本来就常在保护关着时跑)—— 那时返回 nil
// 历史 + 一句人话理由,死规则那一类于是显示「未检查」而不是「零条」。
// **判据不许把「问不出来」读成「查过了,没有」**,这是这个功能最贵的那个教训。
func CoreRuleHistory(fetch func() (stats.Report, error)) (map[rulereview.RuleKey]rulereview.RuleCounts, time.Duration, int64, int, bool, string) {
	rep, err := fetch()
	if err != nil {
		return nil, 0, 0, 0, false, fmt.Sprintf("Core 没在跑或控制面读不到(%v),没有累计历史", err)
	}
	h := rep.RuleHistory
	if h == nil {
		return nil, 0, 0, 0, false, "这一版 Core 没有发布累计历史"
	}
	if h.SkipReason != "" {
		return nil, 0, 0, 0, false, h.SkipReason
	}
	// **转换住在这里,不在 rulereview 里** —— 那个包的纯度守卫不许它 import stats。
	out := make(map[rulereview.RuleKey]rulereview.RuleCounts, len(h.Rules))
	for _, r := range h.Rules {
		k := rulereview.RuleKey{Source: r.Source, Rule: r.Rule}
		c := out[k]
		c.Attempts += r.Attempts
		c.Failures += r.Failures
		out[k] = c
	}
	return out, time.Duration(h.UptimeSeconds) * time.Second, h.Decisions, len(h.Versions), h.Overflowed, ""
}

// chinaDomainPatterns 按行拆开内嵌 china 列表。**这里不是照抄 supervisor 的
// readLines**(它其实什么都不过滤,只是原样按行切开)——真正的先例是
// route.NewDomainSet:它自己也会 TrimSpace/跳注释/跳空行/去 `*.` 前缀。这里提前做
// 一遍是为了让这个函数本身可读、可单测(看得出「一行一条,# 是注释」这条约定),
// 与 NewDomainSet 内部那份重复但无害。
func chinaDomainPatterns(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func Assemble(cfg *config.Config, embeddedChina []byte, fetchStatus func() (stats.Report, error)) rulereview.Input {
	in := rulereview.Input{GlobalProxy: cfg.Global}
	if fetchStatus != nil {
		in.History, in.HistoryUptime, in.HistoryDecisions, in.HistoryVersions,
			in.HistoryOverflowed, in.HistorySkipReason = CoreRuleHistory(fetchStatus)
	} else {
		// **nil fetcher 是「这条路上没人问过 Core」,不是「Core 没在跑」。**
		// 两者都通向「未检查」,但理由不同,而这份报告的价值全在理由上。
		in.HistorySkipReason = "这条路径没有读 Core 的累计历史"
	}
	for _, r := range cfg.Rules {
		in.Direct = append(in.Direct, r.Direct...)
		in.Proxy = append(in.Proxy, r.Proxy...)
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir
	}
	override := cfg.Lists.ChinaDomain != ""
	path := provision.ChinaDomainPath(dataDir)
	if override {
		path = cfg.Lists.ChinaDomain
	}

	raw, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		in.China = route.NewDomainSet(chinaDomainPatterns(raw))
		if override {
			in.ChinaSource = fmt.Sprintf("你在 lists.china_domain 指的列表(%s)", path)
		} else {
			in.ChinaSource = fmt.Sprintf("Core 当前实际使用的 china 列表(%s)", path)
		}
	case override:
		// **不回落。** 用户明确换掉了参照物,内嵌快照不是它的可信代用品。
		in.ChinaSkipReason = fmt.Sprintf(
			"你在 lists.china_domain 指了自己的列表(%s),读不到(%v),这一类没有比对",
			path, readErr,
		)
	case len(embeddedChina) == 0:
		in.ChinaSkipReason = fmt.Sprintf(
			"Core 实际使用的列表(%s)读不到(%v),也没有内嵌快照可回落,这一类没有比对",
			path, readErr,
		)
	default:
		in.China = route.NewDomainSet(chinaDomainPatterns(embeddedChina))
		in.ChinaFallback = true
		in.ChinaSource = fmt.Sprintf(
			"内嵌快照(读不到 Core 实际使用的列表 %s:%v,已回落,可能与 Core 此刻用的不一致)",
			path, readErr,
		)
	}
	return in
}
