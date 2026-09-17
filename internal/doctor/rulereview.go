package doctor

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/rulereview"
)

// Finding 是一行 doctor 输出的三段式,与 doctorLine / Report.AddCheck 的形参同构。
// 单独成型是为了让「说什么」可以被单测,而「怎么打印」留在 doctorAction 里。
type Finding struct {
	Status string // ok | warn | info | hint
	Key    string
	Value  string
	Hint   string
}

// DeadRulesCheckName 是死规则那一行的 check 名。
//
// **单独一个常量**:这一支已经因为同名 check 静默丢过一次安全结论 —— check 名的
// 整个存在理由是「按名字取」,重名会让消费方只拿到其中一条。仓库级重名守卫
// TestDoctorReportHasNoDuplicateCheckNames 盯着这件事。
const DeadRulesCheckName = "dead rules"

// RuleReviewLines 把体检报告翻成 doctor 的行。
//
// **干净就一个字都不说。** 只在真有问题时才占地方,是这套东西不被训练成噪声的前提
// (与按规则失败计数同一条纪律)。唯一的例外是「没查」——那必须说,因为静默的
// 「没查」与「没问题」在用户眼里长得一模一样。
//
// **文本路径与 JSON 路径共用这一份判据** —— Judge 把这些行折进同一份 Report,
// 文本路径(renderDoctorReport)与 --json 只是那一份 Report 的两种渲染,不许
// 为了保住某一侧的呈现分叉成两条判断逻辑。
func RuleReviewLines(rep rulereview.Report) []Finding {
	var out []Finding

	if f := riskyRuleFinding(rep); f != nil {
		out = append(out, *f)
	}

	if n := rep.OverriddenCount; n > 0 {
		out = append(out, Finding{
			Status: "warn",
			Key:    "rules never in effect",
			Value:  summarizeClass(rep, rulereview.ClassOverriddenByOppositeKind, n, " direct rule(s) are overridden by a broader proxy rule and have never taken effect"),
		})
	}
	if n := rep.ShadowedByUserCount; n > 0 {
		out = append(out, Finding{
			Status: "info",
			Key:    "redundant rules",
			Value:  summarizeClass(rep, rulereview.ClassShadowedByUserRule, n, " covered by a broader rule of your own; deleting them changes no traffic"),
		})
	}
	// —— 死规则 ——
	//
	// **「没查」也要说。** 这一类没查的情况比别的类多得多(Core 没在跑 / 跟踪表
	// 满过 / 门槛还没到),静默缺席会让用户以为体检查过了这一项、而且没发现问题。
	if rep.DeadChecked {
		if n := rep.DeadCount; n > 0 {
			value := summarizeClass(rep, rulereview.ClassDead, n, " rule(s) have never matched a single connection, cumulatively")
			// **跨了几个版本要说出来**,让用户对这份累计打折 —— 中间可能有几版的
			// 计数行为并不一致。
			if v := rep.DeadVersionsSpanned; v > 1 {
				value += fmt.Sprintf(" (this total spans %d bx versions — discount it accordingly)", v)
			}
			out = append(out, Finding{
				Status: "info",
				Key:    DeadRulesCheckName,
				Value:  value,
				Hint:   "Before deleting, make sure you really no longer visit that domain. To delete: " + elevate.Prefix + "bx direct rm '<rule>', then " + elevate.Prefix + "bx down && " + elevate.Prefix + "bx up",
			})
		}
	} else if rep.DeadSkipReason != "" {
		out = append(out, Finding{
			Status: "info",
			Key:    DeadRulesCheckName,
			Value:  "Not checked: " + rep.DeadSkipReason,
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
			out = append(out, Finding{
				Status: "info",
				Key:    "builtin list source",
				Value:  "the china-list comparison fell back to the embedded snapshot, not the one Core is actually using: " + rep.BuiltinListSource,
			})
		}
		out = append(out, builtinListLines(rep)...)
	} else if rep.BuiltinSkipReason != "" {
		// **「没查」不许静默。** 它与「查了没有」在用户眼里长得一样,
		// 而两者的差别正是这个功能最贵的那个教训。
		out = append(out, Finding{
			Status: "info",
			Key:    "builtin list check",
			Value:  "Not checked: " + rep.BuiltinSkipReason,
		})
	}
	return out
}

// riskyRuleFinding 把全部危险直连 finding 合并成**恰好一条** Finding。
//
// **不是每条 finding 一行** —— policy.DirectRisk 的名单有 19 个域
// (aliyuncs/myqcloud/amazonaws/cloudfront/github.io…),配置里同时有两条危险直连
// 完全现实。RuleReviewCheckName 的整个存在理由是「agent 与 MCP 按名字取」,如果
// 每条 finding 各产出一条同名 check,--json 路径会对 Report.AddCheck 同一个名字调
// 多次,产生多个同名 Check —— 按名字取的消费方(这是 JSON 路径唯一的读者)
// 只会拿到其中一条,静默丢掉其余的安全结论。一个去匿名化风险被静默丢掉,
// 方向正好是这个功能要防的那个错误的反面。
//
// **详情不截断,不同于 summarizeClass 的「前三条 + 等 N 条」**——那是给冗余/覆盖
// 这类「建议」用的折中(全列出来会把 doctor 淹掉);这一类是**安全结论**,少报一条
// 等于没报那一条。hint 同理:必须能一次处理全部规则,不能只给第一条的命令——
// 那会让用户以为删掉那一条就完了。
func riskyRuleFinding(rep rulereview.Report) *Finding {
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
	return &Finding{
		Status: "warn",
		Key:    "risky direct rule",
		Value: fmt.Sprintf("%d direct rule(s) hit the hazard list: %s — %s",
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
		Hint: fmt.Sprintf(""+elevate.Prefix+"bx direct rm %s (then "+elevate.Prefix+"bx down && "+elevate.Prefix+"bx up)", strings.Join(quoted, " ")),
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
func builtinListLines(rep rulereview.Report) []Finding {
	var out []Finding
	if direct := classKindFindings(rep, rulereview.ClassShadowedByBuiltinList, "direct"); len(direct) > 0 {
		out = append(out, Finding{
			Status: "info",
			Key:    "covered by builtin list",
			Value:  summarizeFindings(direct, len(direct), " already covered by bx's built-in china list; deleting them changes no traffic") + builtinListSourceSuffix(rep),
		})
	}
	if proxy := classKindFindings(rep, rulereview.ClassShadowedByBuiltinList, "proxy"); len(proxy) > 0 {
		out = append(out, Finding{
			Status: "info",
			Key:    "builtin list exception",
			Value: summarizeFindings(proxy, len(proxy),
				" pull domains that the built-in china list sends direct back through the tunnel — an exception in force; deleting them changes traffic") + builtinListSourceSuffix(rep),
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
		out = append(out, Finding{
			Status: "info",
			Key:    "builtin list (kind unknown)",
			Value: summarizeFindings(rest, len(rest),
				" relate to the built-in china list, but which list they live in is unknown — check before changing anything") + builtinListSourceSuffix(rep),
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
// 共用同一份 Finding.Value(RuleReviewLines 顶部的既有纪律),故这里
// 加一次两侧就都带上,不需要分别改。
//
// BuiltinListSource 为空(如测试直接手写 rulereview.NewReport(...) 而不经
// buildRuleReviewInput)时不加缀,不产出一句指向虚无的「依据:」。
func builtinListSourceSuffix(rep rulereview.Report) string {
	if rep.BuiltinListSource == "" {
		return ""
	}
	return " (source: " + rep.BuiltinListSource + ")"
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
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}

// RuleReviewCheckName 把人话 key 换成 JSON 里稳定的 snake_case 名 ——
// agent 与 MCP 按名字取,名字变了就是接口变了。
func RuleReviewCheckName(key string) string {
	return "rule_" + strings.ReplaceAll(key, " ", "_")
}
