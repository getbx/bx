package guardian

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// —— 嵌套层的反射守卫(2026-08-24)——
//
// **这条补的是 2026-08-17 那次真机 soak 暴露出来的结构性窟窿。**
//
// 顶层有 TestEveryStatusFieldParticipatesInTheDigest 逼着「新加一个顶层字段
// 默认参与投影」成立;而 Core/Reconcile/Recovery **内部**哪些字段易变,靠的是
// TestVolatileNestedFieldsDoNotMoveTheDigest 里手写的一张 case 列表 ——
// **没有任何守卫会因为「某个嵌套字段没被这张列表提到」而报错**。
// `ReconcileReport.UnchangedRounds` 就是被漏看的那一个:没人为它写过一行判断,
// 直到真机挂着 `bx status --watch` 跑 10 分钟只读 soak,稳态下唤醒了 4 次才
// 把它显形。
//
// 本条测试把顶层那条守卫的形状原样搬到嵌套层:**遍历,而不是清点**。
// 判据与顶层一字不差 —— 改一个字段,投影要么动,要么这个字段在
// nestedDigestExclusions 里**写明了为什么易变**。两者都不成立就是「被静默
// 排除在 watch 之外」。
//
// 它与那张 case 列表不重复,方向相反:case 列表证明「名单上这几条确实不动
// 投影」,本条证明「不在名单上的每一条都动投影」。前者漏一条不会红,后者会。

// nestedDigestExclusions 是**嵌套**字段的排除名单:改它**不该**移动投影。
// 键是路径,值是理由 —— 一条没有理由的排除,下一个人无从判断它还该不该在
// 名单里(与顶层 digestExclusions 同一条纪律)。
var nestedDigestExclusions = map[string]string{
	"Core.LatencyMS":               "每次健康探测都抖:390 → 412 → 388",
	"Core.FailingRules[].Attempts": "每条连接都在涨;「这条规则开始成片失败」是真事件,「它又多失败了 3 次」不是",
	"Core.FailingRules[].Failures": "每条连接都在涨,与 Attempts 是同一个计数器的两半;只零计数、保留 Kind/Rule",
	"Reconcile.At":                 "recordReconcileRound 每轮都盖时间戳,不排除会跟着 30 秒–10 分钟的调谐环触发",
	"Reconcile.UnchangedRounds":    "与 At 是同一类东西的两面 —— 循环又跑了一轮的记账,不是「有什么变了」的信号。2026-08-17 真机 soak 抓到的正是它",
	"Recovery.UpdatedAt":           "恢复中每次轮询都换;菜单那个「Connecting — N 秒」计数器另有本地驱动,不靠它",
}

// nestedDigestSignals 是**嵌套**字段里的真信号:改它**必须**移动投影。
//
// **这张表看起来是纯簿记,而它恰恰是这条守卫全部的价值所在。**
// 设计草稿里那版判据是「不在名单里就必须移动投影」—— 我照着写完才发现它的
// 极性反了:`UnchangedRounds` 那个真机 bug 是「参与了投影而不该参与」,在那版
// 判据下**照样全绿**。它挡得住「有人悄悄排除一个字段」,挡不住「有人加了一个
// 每轮都涨的字段」,而后者才是 2026-08-17 真机 soak 抓到的那一种。
//
// 穷举分类才堵得上:每一个嵌套字段必须落进这两张表之一,少一个就红。于是往
// ReconcileReport / CoreRuntime / RecoverySnapshot / MaintenanceHoldStatus 里
// 加字段的人**被编译不过之外的东西逼着回答那个问题** —— 它是「真事件」还是
// 「循环又跑了一轮的记账」。CLAUDE.md 说「没有任何测试会替他问这个问题」,
// 这张表就是那个提问的人。
//
// 两张表的形状刻意不对称:排除要写理由,信号只要列名字。因为两种错误的代价
// 不对称 —— 错误地排除是菜单静默地不再对这个信号反应(安静,只有用户抱怨才
// 会发现),错误地当成信号是 watch 多触发几次(吵、当场看得见)。默认站在
// 「吵」那边,与 Class 零值取 ClassRisky、leakcheck.Section 零值取 SectionPath
// 同一条纪律。
var nestedDigestSignals = []string{
	"Core.Reachable",
	"Core.TunnelHealthy",
	"Core.RoutesInstalled",
	"Core.DNSListening",
	"Core.UDPRequired",
	"Core.UDPReady",
	"Core.Server",
	"Core.Transport",
	"Core.UDPMode",
	"Core.UDPTransport",
	"Core.DNSUpstream",
	"Core.FailingRules[].Kind",
	"Core.FailingRules[].Rule",
	// 有应用绕过 bx 以真实 IP 收发是**真事件**(一次泄漏),菜单该醒;名单不变时不自激。
	"Core.BypassingApps",
	"Reconcile.Actions",
	"Reconcile.Held",
	"Reconcile.Unobservable",
	"Reconcile.CoreScan.Measured",
	"Reconcile.CoreScan.Cores",
	"Reconcile.CoreScan.Reason",
	// ③b 的执行结果是**真事件**:调谐器动了手(或失败了),菜单该醒。
	// 同样的失败反复出现时字段值不变,不会自激。
	"Reconcile.Executed.Action",
	"Reconcile.Executed.Outcome",
	"Reconcile.Executed.Error",
	"Recovery.ID",
	"Recovery.State",
	"Recovery.Stage",
	"Recovery.Reason",
	"Recovery.Generation",
	"Recovery.ErrorCode",
	"Recovery.Detail",
	"Recovery.Attempt",
	"Recovery.StartedAt",
	"MaintenanceHold.Reason",
	"MaintenanceHold.ExpiresAt",
}

func TestEveryNestedStatusFieldParticipatesInTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影算不出来: %v", err)
	}

	probes := nestedDigestProbes(t)
	if len(probes) < 15 {
		t.Fatalf("只枚举出 %d 个嵌套字段,少得反常 —— 这条守卫可能已经读不到它要守的东西了", len(probes))
	}

	signals := map[string]bool{}
	for _, p := range nestedDigestSignals {
		if _, dup := nestedDigestExclusions[p]; dup {
			t.Fatalf("%s 同时在排除名单与信号名单里 —— 两张表对它的要求正好相反", p)
		}
		signals[p] = true
	}

	seen := map[string]bool{}
	for _, p := range probes {
		if seen[p.path] {
			t.Fatalf("路径 %s 被枚举了两次 —— 名单按路径查,重名会让其中一条静默失去守卫", p.path)
		}
		seen[p.path] = true

		mutated := representativeStatus()
		mutateLeafForDigest(t, p.path, p.resolve(reflect.ValueOf(&mutated).Elem()))
		got, err := statusDigest(mutated)
		if err != nil {
			t.Fatalf("改了 %s 之后投影算不出来: %v", p.path, err)
		}
		moved := got != baseline

		why, excluded := nestedDigestExclusions[p.path]
		switch {
		case excluded && moved:
			t.Errorf("%s 在排除名单里(%s),但改它移动了投影 —— 名单与实现不一致,"+
				"两者中有一个是错的", p.path, why)
		case excluded:
			// 名单说它不动,实现也不动:一致。
		case signals[p.path] && !moved:
			t.Errorf("%s 在信号名单里,但改它没有移动投影 —— 它被**静默排除**在 watch 之外了,"+
				"菜单永远不会因为这个字段的变化而更新,而没有任何东西会报错", p.path)
		case signals[p.path]:
			// 名单说它该动,实现也动:一致。
		default:
			t.Errorf("%s 没有被分类。**加了一个嵌套字段的人必须回答一个问题**:"+
				"它是「有什么变了」的真信号(加进 nestedDigestSignals),还是"+
				"「循环又跑了一轮」的记账(加进 nestedDigestExclusions 并写明理由,"+
				"同时在 statusDigest 里清掉它)?"+
				"ReconcileReport.UnchangedRounds 就是没人问过这个问题而溜进来的,"+
				"代价是真机上每一轮调谐都白唤醒一次菜单 —— 而当时全套测试都是绿的",
				p.path)
		}
	}

	// 反向:两张名单里都不许有指向已经不存在的字段的条目。一条陈旧的条目看起来
	// 与一条生效中的完全一样,而它什么也不守 —— 与 bx 那份「四分之三是假的
	// 缺口清单」同一个形状。
	for path := range nestedDigestExclusions {
		if !seen[path] {
			t.Errorf("排除名单里的 %s 在 Status 里已经找不到了 —— 陈旧条目什么也不守,请删掉", path)
		}
	}
	for _, path := range nestedDigestSignals {
		if !seen[path] {
			t.Errorf("信号名单里的 %s 在 Status 里已经找不到了 —— 陈旧条目什么也不守,请删掉", path)
		}
	}
}

// digestProbe 是一个嵌套叶子字段:它的路径,以及从一个 Status 根值导航到它的办法。
//
// 导航写成 resolve 闭包而不是「遍历时就地改」,是因为**每一次探测都必须从一份
// 全新的 fixture 出发** —— 就地连改会让第二个字段的结论建立在第一个字段已经
// 被改过的状态上,而那时「投影变了」可能是上一次改动的余波。
type digestProbe struct {
	path    string
	resolve func(root reflect.Value) reflect.Value
}

// nestedDigestProbes 枚举 Status 里**深度 ≥ 2** 的每一个叶子字段。
//
// 深度 1(Status 自己的标量字段)不在这里:顶层那条守卫已经逐个盯着它们,
// 两条都报会让同一个缺陷在两处红,读的人分不清是一个问题还是两个。
//
// **根不是写死的三个名字。** 走的是「Status 里每一个结构体字段」——
// 于是将来谁加第四个嵌套结构,它自动进入守卫范围,不需要谁记得来这里补一行。
func nestedDigestProbes(t *testing.T) []digestProbe {
	t.Helper()
	var out []digestProbe
	var walk func(typ reflect.Type, path string, depth int, nav []func(reflect.Value) reflect.Value)
	walk = func(typ reflect.Type, path string, depth int, nav []func(reflect.Value) reflect.Value) {
		if depth > 6 {
			t.Fatalf("在 %s 处递归深度超过 6 层 —— 结构可能有环,本守卫读不懂它", path)
		}
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			if skipJSONIgnoredField(field) {
				// 与顶层守卫同一条:静默跳过字段的守卫,和一个静默排除字段的
				// 实现,是同一个问题,所以跳过要留痕。
				t.Logf("跳过 %s.%s:json:\"-\" 使它按构造不会出现在 json 编码里", path, field.Name)
				continue
			}
			idx := i
			step := func(v reflect.Value) reflect.Value { return v.Field(idx) }
			next := append(append([]func(reflect.Value) reflect.Value{}, nav...), step)
			child := path
			if child != "" {
				child += "."
			}
			child += field.Name

			ft := field.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
				deref := func(v reflect.Value) reflect.Value {
					if v.IsNil() {
						t.Fatalf("%s 在 representativeStatus 里是 nil —— fixture 必须每个字段都非零,"+
							"否则这条守卫在它整棵子树上都是空转的", child)
					}
					return v.Elem()
				}
				next = append(next, deref)
			}

			switch {
			case ft == reflect.TypeOf(time.Time{}):
				// time.Time 是结构体,但它是叶子。放在结构体分支之前判。
				emit(&out, child, next, depth+1)
			case ft.Kind() == reflect.Struct:
				walk(ft, child, depth+1, next)
			case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
				// 切片元素里的字段(Core.FailingRules[].Attempts 就住在这儿)。
				// 切片**整体**变化(多出一条规则)由
				// TestANewFailingRuleMovesTheDigest 单独钉,不在这里重复。
				elem := func(v reflect.Value) reflect.Value {
					if v.Len() == 0 {
						t.Fatalf("%s 在 representativeStatus 里是空切片 —— 守卫钻不进它的元素字段", child)
					}
					return v.Index(0)
				}
				walk(ft.Elem(), child+"[]", depth+1, append(next, elem))
			default:
				emit(&out, child, next, depth+1)
			}
		}
	}
	walk(reflect.TypeOf(Status{}), "", 0, nil)
	return out
}

// emit 只收深度 ≥ 2 的叶子。
func emit(out *[]digestProbe, path string, nav []func(reflect.Value) reflect.Value, depth int) {
	if depth < 2 {
		return
	}
	steps := append([]func(reflect.Value) reflect.Value{}, nav...)
	*out = append(*out, digestProbe{
		path: path,
		resolve: func(root reflect.Value) reflect.Value {
			v := root
			for _, s := range steps {
				v = s(v)
			}
			return v
		},
	})
}

// mutateLeafForDigest 把一个叶子改成一个与 fixture 不同的值。
//
// **读不懂的类型必须响亮失败**(与顶层那个变异器同一条):一个遇到新 Kind
// 就放过的守卫,在最需要它的时候恰好是失效的。
func mutateLeafForDigest(t *testing.T, path string, v reflect.Value) {
	t.Helper()
	if !v.CanSet() {
		t.Fatalf("%s 改不动 —— 导航走丢了,这条探测什么也没测", path)
	}
	if t0, ok := v.Interface().(time.Time); ok {
		v.Set(reflect.ValueOf(t0.Add(time.Hour)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(v.Uint() + 1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			t.Fatalf("%s 是 %s 的切片,本守卫的变异器读不懂它 —— 请连同它一起扩写,别让它静默通过",
				path, v.Type().Elem())
		}
		v.Set(reflect.Append(v, reflect.ValueOf("nested-digest-probe")))
	default:
		t.Fatalf("%s 的类型 %s 本守卫读不懂 —— 请扩写变异器,别让它静默通过", path, v.Kind())
	}
}

// 名单里每一条理由都要真的是一句理由,不是一个占位。空理由与没进名单在
// 输出上完全一样,而前者会让下一个人以为有人想过这件事。
func TestEveryNestedExclusionCarriesAReason(t *testing.T) {
	for path, why := range nestedDigestExclusions {
		if len(strings.TrimSpace(why)) < 10 {
			t.Errorf("%s 的排除理由是 %q —— 一条没有理由的排除,下一个人无从判断它还该不该在名单里", path, why)
		}
	}
}
