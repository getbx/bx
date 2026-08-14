package observe

import "fmt"

// Divergence 是一条「信念与事实不符」的记录。
//
// 它是本包给 agent(含用户自己的 AI)的核心产物:自解释、可操作。今天这个信号
// 存在于内核里但无人读取。
type Divergence struct {
	Field    string `json:"field"`
	Believed string `json:"believed"`
	Observed string `json:"observed"`
	Note     string `json:"note"`
}

// fieldCoreSocket 是 Core 控制 socket 那一项的名字。它同时出现在观测项、
// ObserveError.Item 与 Divergence.Field 三处,拼错不会有任何东西变红。
const fieldCoreSocket = "core_socket"

const fieldDirectEgress = "direct_egress"

// heldExplains 列出「有一张维护挂起武装着」这件事**本身就解释得了**的观测项。
//
// 只有 core_socket:挂起的语义就是「此刻不该有保护在跑」,于是 Core 的控制
// socket 拨不通是**预期之内**的,既不是分歧,也不是一次值得报告的观测失败。
//
// 刻意**不**把整张 Errors 表在挂起期间一律压掉:capture_ok / dns_managed /
// barrier_present 的观测走的是 route 与 networksetup,它们与 Core 在不在跑无关,
// 挂起解释不了它们失败。真那么压,一台升级期间 route 命令坏掉的机器会在 15 分钟
// 里完全不出声,而那 15 分钟恰恰是最该出声的时候。
var heldExplains = []string{fieldCoreSocket}

// Diverge 比对意图、观测与信念,产出所有不一致。一致时返回空切片。
//
// 刻意不做的事:不修正信念、不改动系统、不排序优先级。它只陈述差异。
//
// **一个项最多出一行。** 这不是去重洁癖,是正确性:同一个 field 出两行时,两行的
// Observed 可以互相矛盾(真实例子:过期挂起下 core_socket 同时报 observed=false
// 「没有保护在跑」与 observed=unknown「该项无法观测」)。下游按 field 计数
// (Swift 菜单的 anomalyCount)只能要么重复计数、要么在 UI 层自己发明一条去重
// 规则 —— 而优先级是这里的判断,不该推给消费方。**更具体的那一行赢**,而
// 「问不出来」的原因不丢:它被并进那一行的 note 里(见 reasonsByItem)。
func Diverge(intent Intent, observed ObservedState, believed Believed) []Divergence {
	var out []Divergence

	// 挂起用**观测那一刻**的时间判过期,不用 time.Now():这份判断要与它旁边
	// 那些事实说的是同一个瞬间。
	//
	// `After` 而不是 `!Before`:到期时刻恰好等于观测时刻的那张挂起已经不算数
	// (与 guardian.LoadMaintenanceHold 的 `hold.ExpiresAt.After(now)` 同一条边界,
	// 两边分叉会让 Guardian 说「还武装着」而 bx status 说「过期了」)。
	held := intent.Hold != nil && intent.Hold.ExpiresAt.After(observed.ObservedAt)

	reasons := reasonsByItem(observed.Errors)
	// covered 记「这个项已经有交代了」:要么已经出过一行,要么被挂起解释掉了。
	covered := make(map[string]bool, len(observed.Errors)+4)
	if held {
		for _, item := range heldExplains {
			covered[item] = true
		}
	}
	emit := func(d Divergence) {
		if covered[d.Field] {
			return
		}
		// 这个平台上根本不成立的问题不产生分歧 —— 否则每台健康的机器恒报一条,
		// 而那会把 divergence 训练成噪声。
		if observed.notApplicable(d.Field) {
			return
		}
		covered[d.Field] = true
		// 观测失败的原因并进这一行,而不是另起一行:那个原因是排查的全部价值
		// (「未观测到劫持」与「未观测到劫持,因为 route 命令超时」不是一回事)。
		if reason := reasons[d.Field]; reason != "" {
			d.Note = fmt.Sprintf("%s(该项无法观测:%s)", d.Note, reason)
		}
		out = append(out, d)
	}

	if believed.Protection == "protected" {
		// 声称已保护时,三项观测必须皆为 True。Unknown 不满足:不确定不等于正常。
		for _, check := range []struct {
			field string
			value Tristate
			note  string
		}{
			{"capture_ok", observed.CaptureOK, "保护状态声称已保护,但未观测到流量被劫持进 bx 的 TUN"},
			{"dns_managed", observed.DNSManaged, "保护状态声称已保护,但未观测到系统 DNS 由 bx 接管"},
			{"tunnel_healthy", observed.TunnelHealthy, "保护状态声称已保护,但未观测到隧道健康"},
		} {
			if check.value != True {
				emit(Divergence{
					Field:    check.field,
					Believed: "protected",
					Observed: check.value.String(),
					Note:     check.note,
				})
			}
		}
	}

	// 挂起武装期间不谈残留:升级正在进行,屏障与 DNS 接管此刻本来就还在。
	//
	// **可达性**(别把它当死代码删掉):`desired=off` 与一张武装着的挂起并存,
	// 是**过渡升级**的样子 —— 新 CLI 在停机之前武装挂起,而服务那次停机的旧
	// Guardian 不认识挂起、无条件写下 off(它随后会被写回 on,写不成就停在这里)。
	// 升级一台本来就不要保护的机器**不再**产生这个组合(那种停机根本不武装挂起,
	// 见 cli 的 downPurposeUpgradeUnprotected)。
	// 代价是那 15 分钟里真实的残留会被压住 —— 已知取舍,挂起过期即恢复报告。
	if intent.Desired == "off" && !held {
		if observed.BarrierPresent == True {
			emit(Divergence{
				Field:    "barrier_present",
				Believed: "off",
				Observed: "true",
				Note:     "意图是关闭,但内核里仍有 bx 装的阻断路由;它们会让整机断网且不随 bx 退出而消失",
			})
		}
		if observed.DNSManaged == True {
			emit(Divergence{
				Field:    "dns_managed",
				Believed: "off",
				Observed: "true",
				Note:     "意图是关闭,但系统 DNS 仍指向 bx;若 bx 已退出,域名解析会全部失败",
			})
		}
	}

	// 用户要保护,而 Core 的控制 socket 不应答 —— **挂起期间这是预期,过期之后
	// 这就是故障**。设计取舍五:过期不会恢复保护,它买到的是「不再压制」,于是
	// 机器不再「看起来正确地关着」,而是如实地看起来坏了。
	//
	// **判据是 `== False` 而不是 `!= True`**,与上面 protected 那三项刻意相反,
	// 而两边都对:那三项是「声称已经做到了」,未经证实就不许算数;这一条是
	// 「指控系统坏了」,没问出来就不许指控。写成 `!= True` 会让每一台观测原语
	// 缺席的机器恒吐一条「没有保护在跑」—— 正是 observerForPlatform 那道门在
	// 防的噪声。
	//
	// 注意生产里这一支**一定**伴着一条 core_socket 的 ObserveError:observeCore
	// 在 FetchRuntime 失败时同时置 False 并记错误(observer.go)。原因由 emit
	// 并进 note,不另起一行。
	// **这里刻意不再写一次 `!held`。** 挂起对这一项的豁免由 heldExplains 单点
	// 拥有(它把 core_socket 预先记进 covered,emit 因此静默丢弃这一行),再写
	// 一次不改变任何输出 —— 复审实测:去掉那个条件整个包照样全绿,于是这条路上
	// 只剩一个真正承重的判据,而那正是本行下面这条测试要盯住的东西。
	// 两处都写,则两处各自都测不到:改坏一处不会有任何东西变红。
	// **保护开着而 bx 自己的直连出不去。** 与隧道健康正交:真机上出现过隧道满速、
	// 劫持生效、屏障正常,而所有用户 direct 规则 100% 失败(IP_BOUND_IF 找不到
	// scoped 路由)—— 那时其余四项全绿,只有这一条说得出问题。
	if intent.Desired == "on" && observed.DirectEgressOK == False &&
		!observed.notApplicable(fieldDirectEgress) {
		emit(Divergence{
			Field:    fieldDirectEgress,
			Believed: "on",
			Observed: "false",
			Note: "意图是保护,但 bx 自己的直连出不去:每一条 direct rule" +
				"(用户规则 / china 列表)都会失败,而隧道看起来一切正常",
		})
	}
	if intent.Desired == "on" && observed.CoreSocket == False {
		emit(Divergence{
			Field:    fieldCoreSocket,
			Believed: "on",
			Observed: "false",
			Note:     "意图是保护,但 Core 的控制 socket 不应答:此刻没有保护在跑",
		})
	}

	// 剩下的观测失败各出一行 —— **只剩没被上面任何一条交代过的那些**。
	for _, e := range observed.Errors {
		emit(Divergence{
			Field:    e.Item,
			Believed: believed.Protection,
			Observed: "unknown",
			Note:     "该项无法观测",
		})
	}

	return out
}

// reasonsByItem 把观测失败按项名索引,好让它们并进各自那一行。
//
// 同一项有多条错误时取第一条:观测每项只跑一次,第二条不会出现;真出现了,
// 第一条是最靠近失败现场的那条。
func reasonsByItem(errs []ObserveError) map[string]string {
	reasons := make(map[string]string, len(errs))
	for _, e := range errs {
		if _, exists := reasons[e.Item]; !exists {
			reasons[e.Item] = e.Err
		}
	}
	return reasons
}
