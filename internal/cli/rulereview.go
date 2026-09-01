package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/rulereview"
)

// doctorFinding 是一行 doctor 输出的三段式,与 doctorLine / rep.addCheck 的形参同构。
// 单独成型是为了让「说什么」可以被单测,而「怎么打印」留在 doctorAction 里。
type doctorFinding struct {
	Status string // ok | warn | info | hint
	Key    string
	Value  string
	Hint   string
}

// buildRuleReviewInput 把一份 config 摊成体检的原料。
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
// coreRuleHistory 从 Core 的状态报告里取跨重启累计历史。
//
// **Core 没在跑不是错误,是常态**(体检本来就常在保护关着时跑)—— 那时返回 nil
// 历史 + 一句人话理由,死规则那一类于是显示「未检查」而不是「零条」。
// **判据不许把「问不出来」读成「查过了,没有」**,这是这个功能最贵的那个教训。
func coreRuleHistory(fetch func() (stats.Report, error)) (map[rulereview.RuleKey]rulereview.RuleCounts, time.Duration, int64, int, bool, string) {
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

// guardianRulesForDoctor 是「向 Guardian 要规则 + 当前是不是 global」的那一跳,
// 做成变量是为了让**接线**可测:这个仓库全部事故都在接线,而判据写好了、
// doctor 那条路上没人调,与没写完全一样且不会有任何东西报错。
//
// global 取自控制 socket 的模式标签,不是猜的 —— 拿错 mode 与拿错 china 列表
// 是同一形状的事故(rulereview.Input.GlobalProxy 的注释里记着那次真机 bug)。
var guardianRulesForDoctor = func() (direct, proxy []string, global bool, configPath string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	list, err := guardian.NewClient(guardian.SocketPath).Rules(ctx)
	if err != nil {
		return nil, nil, false, "", err
	}
	// 模式读不到不是致命的:global 只影响已经被跳过的那一类,按 false 继续
	// 好过整块放弃 —— 但**不许假装读到了**,故 err 一并带回给调用方判断。
	if rep, statusErr := supervisor.FetchStatusReport(statusSocketPath()); statusErr == nil {
		global = strings.HasPrefix(rep.Mode, "global") || strings.HasPrefix(rep.Mode, "router-global")
	}
	return list.Direct, list.Proxy, global, list.ConfigPath, nil
}

// ruleReviewInputFromGuardianRules 是**配置读不出来时**的那条路:规则从
// Guardian 的 /v1/rules 来(它对业主开放,读的正是同一个 /etc/bx/config.yaml,
// 只是它有 root),global 从控制 socket 的模式来,累计历史仍从 Core 来。
//
// **它与 buildRuleReviewInput 是两条路而不是两份判据**:判定全在
// rulereview.Review 里,这里只组装原料。之所以不复用那一个,是因为
// **china 那一半必须不一样** —— 见下。
//
// **china 那一类诚实跳过,不许凑数。** Guardian 给得出规则,给不出
// `lists.china_domain`:用户可能指了自己的列表,而拿默认列表去比正是
// buildRuleReviewInput 头上那段长注释警告的 wrong-reference-object 事故
// (判据没错、读错了输入,然后指着一条仍然生效的规则说「可以删」)。
// 代价是这条路少一类;收益是另外三类 + 死规则**第一次对非 root 的 agent
// 可见**,而此前是一条都没有。
//
// **真正的终局形状是 Guardian 自己做体检并发布**(它有 root,读得到配置、
// 读得到 Core 实际在用的那张 china 列表)—— 那样这一类也不必跳过。今天不做:
// 它要往 Status 里加字段、进 statusdigest 分类、并处理每次 status 读都重算
// 一遍 12k 域名的代价,是单独一期。
func ruleReviewInputFromGuardianRules(direct, proxy []string, global bool, fetchStatus func() (stats.Report, error)) rulereview.Input {
	in := rulereview.Input{
		Direct:      append([]string(nil), direct...),
		Proxy:       append([]string(nil), proxy...),
		GlobalProxy: global,
		ChinaSkipReason: "配置文件读不到(非 root),规则改经 Guardian 取得;" +
			"而 Guardian 那条路给不出你是否指定了自己的 china 列表,故这一类没有比对",
	}
	if fetchStatus != nil {
		in.History, in.HistoryUptime, in.HistoryDecisions, in.HistoryVersions,
			in.HistoryOverflowed, in.HistorySkipReason = coreRuleHistory(fetchStatus)
	} else {
		in.HistorySkipReason = "这条路径没有读 Core 的累计历史"
	}
	return in
}

func buildRuleReviewInput(cfg *config.Config, embeddedChina []byte, fetchStatus func() (stats.Report, error)) rulereview.Input {
	in := rulereview.Input{GlobalProxy: cfg.Global}
	if fetchStatus != nil {
		in.History, in.HistoryUptime, in.HistoryDecisions, in.HistoryVersions,
			in.HistoryOverflowed, in.HistorySkipReason = coreRuleHistory(fetchStatus)
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

// ruleReviewDoctorLines 把体检报告翻成 doctor 的行。
//
// **干净就一个字都不说。** 只在真有问题时才占地方,是这套东西不被训练成噪声的前提
// (与按规则失败计数同一条纪律)。唯一的例外是「没查」——那必须说,因为静默的
// 「没查」与「没问题」在用户眼里长得一模一样。
//
// **文本路径与 JSON 路径共用这一份判据** —— doctorAction 与 collectClientDoctorWith
// 都只是拿这个函数的返回值分别渲染,不许为了保住某一侧的呈现分叉成两条判断逻辑。
// deadRulesCheckName 是死规则那一行的 check 名。
//
// **单独一个常量**:这一支已经因为同名 check 静默丢过一次安全结论 —— check 名的
// 整个存在理由是「按名字取」,重名会让消费方只拿到其中一条。仓库级重名守卫
// TestDoctorReportHasNoDuplicateCheckNames 盯着这件事。
const deadRulesCheckName = "dead rules"

func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding {
	var out []doctorFinding

	if f := riskyRuleFinding(rep); f != nil {
		out = append(out, *f)
	}

	if n := rep.OverriddenCount; n > 0 {
		out = append(out, doctorFinding{
			Status: "warn",
			Key:    "rules never in effect",
			Value:  summarizeClass(rep, rulereview.ClassOverriddenByOppositeKind, n, "条 direct 规则被更宽的 proxy 规则压住,从来没生效过"),
		})
	}
	if n := rep.ShadowedByUserCount; n > 0 {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "redundant rules",
			Value:  summarizeClass(rep, rulereview.ClassShadowedByUserRule, n, "条被你自己更宽的一条覆盖,删掉不改变任何流量"),
		})
	}
	// —— 死规则 ——
	//
	// **「没查」也要说。** 这一类没查的情况比别的类多得多(Core 没在跑 / 跟踪表
	// 满过 / 门槛还没到),静默缺席会让用户以为体检查过了这一项、而且没发现问题。
	if rep.DeadChecked {
		if n := rep.DeadCount; n > 0 {
			value := summarizeClass(rep, rulereview.ClassDead, n, "条规则累计从未命中过一次")
			// **跨了几个版本要说出来**,让用户对这份累计打折 —— 中间可能有几版的
			// 计数行为并不一致。
			if v := rep.DeadVersionsSpanned; v > 1 {
				value += fmt.Sprintf("(这份累计跨了 %d 个 bx 版本,可酌情打折)", v)
			}
			out = append(out, doctorFinding{
				Status: "info",
				Key:    deadRulesCheckName,
				Value:  value,
				Hint:   "删之前先确认那个域名你确实不再访问;删规则用 sudo bx direct rm '<规则>',改完要 sudo bx down && sudo bx up",
			})
		}
	} else if rep.DeadSkipReason != "" {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    deadRulesCheckName,
			Value:  "未检查:" + rep.DeadSkipReason,
		})
	}

	if rep.BuiltinListChecked {
		if rep.BuiltinListFallback {
			// **回落必须被点名,不许静默。** 「查了、用的是可能过期的内嵌快照」
			// 与「查了、用的是 Core 此刻实际在用的列表」在下面 builtinListLines
			// 那两行的版式上长得一样(都是 info、都是「删掉不改变流量」),用户
			// 没法从版式本身分辨哪一种——必须单独说一句,而不是只指望
			// builtinListSourceSuffix 缀在别的行尾(那句在零 finding 时根本不会
			// 出现,而「查了但比对基准可能过期」这件事跟有没有 finding 无关)。
			out = append(out, doctorFinding{
				Status: "info",
				Key:    "builtin list source",
				Value:  "china 列表比对回落到了内嵌快照,不是 Core 此刻实际使用的那一份:" + rep.BuiltinListSource,
			})
		}
		out = append(out, builtinListLines(rep)...)
	} else if rep.BuiltinSkipReason != "" {
		// **「没查」不许静默。** 它与「查了没有」在用户眼里长得一样,
		// 而两者的差别正是这个功能最贵的那个教训。
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "builtin list check",
			Value:  "未检查:" + rep.BuiltinSkipReason,
		})
	}
	return out
}

// riskyRuleFinding 把全部危险直连 finding 合并成**恰好一条** doctorFinding。
//
// **不是每条 finding 一行** —— policy.DirectRisk 的名单有 19 个域
// (aliyuncs/myqcloud/amazonaws/cloudfront/github.io…),配置里同时有两条危险直连
// 完全现实。ruleReviewCheckName 的整个存在理由是「agent 与 MCP 按名字取」,如果
// 每条 finding 各产出一条同名 check,--json 路径会对 rep.addCheck 同一个名字调
// 多次,产生多个同名 checkReport —— 按名字取的消费方(这是 JSON 路径唯一的读者)
// 只会拿到其中一条,静默丢掉其余的安全结论。一个去匿名化风险被静默丢掉,
// 方向正好是这个功能要防的那个错误的反面。
//
// **详情不截断,不同于 summarizeClass 的「前三条 + 等 N 条」**——那是给冗余/覆盖
// 这类「建议」用的折中(全列出来会把 doctor 淹掉);这一类是**安全结论**,少报一条
// 等于没报那一条。hint 同理:必须能一次处理全部规则,不能只给第一条的命令——
// 那会让用户以为删掉那一条就完了。
func riskyRuleFinding(rep rulereview.Report) *doctorFinding {
	var rules []string
	summary := ""
	for _, f := range rep.Findings {
		if f.Class != rulereview.ClassRisky {
			continue
		}
		rules = append(rules, f.Rule)
		if summary == "" {
			summary = f.Summary
		}
	}
	if len(rules) == 0 {
		return nil
	}
	quoted := make([]string, len(rules))
	for i, r := range rules {
		quoted[i] = "'" + r + "'"
	}
	return &doctorFinding{
		Status: "warn",
		Key:    "risky direct rule",
		Value: fmt.Sprintf("%d 条直连规则命中危险名单:%s —— %s",
			len(rules), strings.Join(rules, "、"), summary),
		// bx direct rm(不是 remove —— 那是这条 hint 上一版的笔误,命令本身
		// 不存在)接受多个域名一次处理,一条命令覆盖全部规则。
		//
		// **两条命令都带 sudo,理由不对称,分开说:**
		//
		// `direct rm` **必须**带:editRuleAction(internal/cli/direct.go)对
		// 配置路径直接 os.ReadFile/os.WriteFile,不做任何自提权、也不经
		// Guardian 的 /v1/rules 走 peer-cred 鉴权——它就是个普通文件读写。
		// 而 `/etc/bx/config.yaml` 是 `sudo bx setup` 建的,模式 0600 属主
		// root(真机验证,2026-08-17)。不带 sudo 复制粘贴这条命令必得
		// permission denied,用户会以为自己敲错了命令而不是缺权限。
		//
		// `down`/`up` **不总是需要,但仍然加**:在 macOS 上它们经 Guardian 的
		// `/v1/up`、`/v1/down`(mutationHandler + authorizeOwnerPeer,
		// internal/guardian/localapi.go)鉴权,配置了 owner_uid(`sudo bx setup`
		// 时从 SUDO_UID 自动捕获,见 internal/setup/setup.go
		// ownerUIDFromEnv)的机器上以该用户身份跑不需要 root。但这条 hint 是
		// 跨平台共用的同一份文本:**Linux/Windows 上 upAction/downAction 根本
		// 不经 Guardian,直接 systemctl/SCM 操作服务,始终需要 root**;macOS
		// 上若 owner_uid 未配置(没走 `sudo bx setup`,如直接以 root 登录跑
		// setup),Guardian 的鉴权退化成 root-only,同样需要 sudo。多数机器
		// (Linux 是本项目的主平台)不带会直接 permission denied,与
		// `direct rm` 是同一类 bug;多带的代价只是 macOS 已配置 owner 的机器上
		// 一次不必要的密码提示——不对称,故两条命令都印 sudo。这与
		// editRuleAction 自己在同一文件里的既有措辞一致(未运行时的提示已经写的
		// 是「下次 sudo bx up 时生效」)。
		Hint: fmt.Sprintf("sudo bx direct rm %s(改完要 sudo bx down && sudo bx up)", strings.Join(quoted, " ")),
	}
}

// summarizeClass 打出「N 条 + 前三条点名」,不区分 Kind —— 除
// ClassShadowedByBuiltinList 外的三类里,同一类的话对 direct/proxy 都成立
// (冗余就是冗余、失效就是失效,不看它在哪张表),混在一条不会混淆语义。
func summarizeClass(rep rulereview.Report, class rulereview.Class, n int, tail string) string {
	var findings []rulereview.Finding
	for _, f := range rep.Findings {
		if f.Class == class {
			findings = append(findings, f)
		}
	}
	return summarizeFindings(findings, n, tail)
}

// builtinListLines 把 ClassShadowedByBuiltinList 按 **Kind** 拆成两条独立的行。
//
// **这是 Finding 1 的修复所在。** review.go 的 shadowedByBuiltinFindings 早就对
// direct/proxy 两支写了意思相反的 Summary(direct:「删了没影响」;proxy:「这是
// 生效中的例外,删了会改变流量」),但此前渲染层把两支的 finding 全塞进同一次
// summarizeClass 调用、同一个 Key——那句相反的话被扔进了从不打印的 Summary 字段,
// 一份只有 proxy 例外的配置读到的是跟「可以安全删除」同一种版式的一行。
//
// **两个计数各自独立、绝不合并到一起呈现**:rulereview.Report.ShadowedByBuiltinCount
// 本身就是两支的和(判据只按 Class 计数,不认识 Kind,这是 rulereview 包的既有
// 判定,本函数不改它),这里在渲染层按 Kind 重新分组——这是渲染层的职责,不是
// 判据的职责。
func builtinListLines(rep rulereview.Report) []doctorFinding {
	var out []doctorFinding
	if direct := classKindFindings(rep, rulereview.ClassShadowedByBuiltinList, "direct"); len(direct) > 0 {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "covered by builtin list",
			Value:  summarizeFindings(direct, len(direct), "条与内建 china 列表相关,删掉不改变任何流量") + builtinListSourceSuffix(rep),
		})
	}
	if proxy := classKindFindings(rep, rulereview.ClassShadowedByBuiltinList, "proxy"); len(proxy) > 0 {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "builtin list exception",
			Value: summarizeFindings(proxy, len(proxy),
				"条把内建 china 列表判直连的域名扳回隧道——这是生效中的例外,删掉会改变流量") + builtinListSourceSuffix(rep),
		})
	}
	// **不认识的 Kind 不许静默丢弃。**
	//
	// 上面两条线按 Kind 的字面量分支,而 `NewReport` 的计数走的是 Class、**根本
	// 不看 Kind** —— 一条 Kind 为空、或将来多出第三种 Kind 的 finding 会让计数
	// 照涨而两条线一条都不匹配:它在文本与 --json 两条路径上**同时静默消失**,
	// 用户看到「N 条与内建列表相关」而下面只列出 N-1 条,没有任何一处报错。
	//
	// 今天不可达(review.go 只产出 direct/proxy),但「今天不可达」不是「不会发生」
	// —— 这一支存在的意义是:谁加了第三种 Kind,会立刻在输出里看见它,而不是让它
	// 悄悄少一行。措辞刻意保守:两句话的方向相反(删掉不改变流量 / 删掉会改变
	// 流量),对一条我们分不出方向的 finding,只报事实不给建议。
	if rest := classFindingsExcludingKinds(rep, rulereview.ClassShadowedByBuiltinList, "direct", "proxy"); len(rest) > 0 {
		out = append(out, doctorFinding{
			Status: "info",
			Key:    "builtin list (kind unknown)",
			Value: summarizeFindings(rest, len(rest),
				"条与内建 china 列表相关,但它们所在的表未知——请核对再决定是否改动") + builtinListSourceSuffix(rep),
		})
	}
	return out
}

// classFindingsExcludingKinds 取某一类里 Kind **不在**给定集合中的 finding。
// 它与 classKindFindings 是同一件事的两面:后者按名单收,这里按名单排除,
// 于是「每条 finding 都落进恰好一条线」是由构造保证的,不靠人去对表。
func classFindingsExcludingKinds(rep rulereview.Report, class rulereview.Class, kinds ...string) []rulereview.Finding {
	known := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		known[k] = true
	}
	var out []rulereview.Finding
	for _, f := range rep.Findings {
		if f.Class == class && !known[f.Kind] {
			out = append(out, f)
		}
	}
	return out
}

// builtinListSourceSuffix 把「这次比对用的是哪一份列表」钉进每一条 finding 的文本里
// —— 一句「已被内建列表覆盖」不说清是哪一份内建列表,正是 wrong-reference-object
// 这次要消灭的歧义(见 buildRuleReviewInput 顶部注释)。文本路径与 --json 路径
// 共用同一份 doctorFinding.Value(ruleReviewDoctorLines 顶部的既有纪律),故这里
// 加一次两侧就都带上,不需要分别改。
//
// BuiltinListSource 为空(如测试直接手写 rulereview.NewReport(...) 而不经
// buildRuleReviewInput)时不加缀,不产出一句指向虚无的「依据:」。
func builtinListSourceSuffix(rep rulereview.Report) string {
	if rep.BuiltinListSource == "" {
		return ""
	}
	return "(依据:" + rep.BuiltinListSource + ")"
}

// classKindFindings 按 Class 与 Kind 两个维度筛选,顺序与 rep.Findings 一致。
func classKindFindings(rep rulereview.Report, class rulereview.Class, kind string) []rulereview.Finding {
	var out []rulereview.Finding
	for _, f := range rep.Findings {
		if f.Class == class && f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

// summarizeFindings 是 summarizeClass 与 builtinListLines 共用的格式化核心:
// 「N 条 + 前三条点名」。全部列出来会把 doctor 淹掉,一条不点名又等于没说 ——
// 折中是给数字 + 够他去配置里搜的那几条原文。
func summarizeFindings(findings []rulereview.Finding, n int, tail string) string {
	var names []string
	for _, f := range findings {
		// **CoveredBy 为空时不许留一个悬空的箭头。** 死规则那一类没有「被谁盖住」
		// 这回事(它只是从没被用到),无条件拼 `← ` 会打出
		// `*.a.example ← 、*.b.example ← ` —— 用户看到的是一句没写完的话。
		//
		// 这个 bug 是**肉眼看输出**抓到的:测试断言的是「这一行含规则原文」,
		// 而它含了,于是全绿。断言「说了什么」与「说得像句人话」是两件事。
		if f.CoveredBy == "" {
			names = append(names, f.Rule)
		} else {
			names = append(names, fmt.Sprintf("%s ← %s", f.Rule, f.CoveredBy))
		}
		if len(names) == 3 {
			break
		}
	}
	s := fmt.Sprintf("%d %s:%s", n, tail, strings.Join(names, "、"))
	if n > len(names) {
		s += fmt.Sprintf(" 等 %d 条", n)
	}
	return s
}

// ruleReviewCheckName 把人话 key 换成 JSON 里稳定的 snake_case 名 ——
// agent 与 MCP 按名字取,名字变了就是接口变了。
func ruleReviewCheckName(key string) string {
	return "rule_" + strings.ReplaceAll(key, " ", "_")
}
