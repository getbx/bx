package guardian

import (
	"reflect"
	"testing"
	"time"
)

// representativeStatus 是一份**每个字段都非零**的 Status。
//
// 非零是承重的:反射守卫靠「改一个字段、看投影变不变」工作,而从零值改成非零
// 与从非零改成另一个非零,在一个写错了的 statusDigest 面前不等价
// (比如一个只在指针非 nil 时才纳入嵌套内容的实现)。
func representativeStatus() Status {
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	return Status{
		SchemaVersion:     1,
		Desired:           DesiredOn,
		Phase:             PhaseCommitted,
		CorePID:           4321,
		CoreVersion:       "0.3.0",
		Protection:        ProtectionProtected,
		NetworkGeneration: "gen-7",
		Recovery: RecoverySnapshot{
			ID: "rec-1", State: "idle", Stage: "idle", Reason: "manual",
			Generation: "gen-7", ErrorCode: "", Detail: "d",
			Attempt: 2, StartedAt: at, UpdatedAt: at,
		},
		LastError:       "boom",
		GuardianVersion: "g1",
		RuntimeVersion:  "r1",
		DNSState:        DNSManaged,
		DNSManaged:      true,
		DNSService:      "Wi-Fi",
		DNSServers:      []string{"127.0.0.1"},
		Core: &CoreRuntime{
			Reachable: true, TunnelHealthy: true, LatencyMS: 390,
			Server: "vps", Transport: "reality", UDPMode: "proxy",
			UDPTransport: "hysteria2://x", DNSUpstream: "223.5.5.5",
			FailingRules: []FailingRule{{Kind: "direct", Rule: "*.qq.com", Attempts: 100, Failures: 99}},
		},
		Capabilities: []string{CapabilityRules},
		Reconcile: &ReconcileReport{
			At: at, Actions: []string{"start_core"},
			Executed: &ReconcileExecution{Action: "restore_dns", Outcome: "failed", Error: "boom"},
		},
		MaintenanceHold: &MaintenanceHoldStatus{Reason: "upgrade", ExpiresAt: at},
		// LastErrorGeneration 是 Status 结构体里 brief fixture 原文没填的字段
		// (grep `type Status struct` 核对后补的):它是内部一致性计数器,
		// json:"-" 使它天生不进 JSON 编码,因此天生不可能参与靠 json.Marshal
		// 实现的投影 —— 这条测试的反射循环按 json 标签自动跳过它(见下方
		// TestEveryStatusFieldParticipatesInTheDigest 里的 skipJSONIgnoredField),
		// 不需要进 digestExclusions。这里给非零值只是遵循「fixture 每个字段
		// 都非零」的纪律,对这个字段本身的测试结果没有影响。
		LastErrorGeneration: 7,
	}
}

// digestExclusions 是**顶层**字段的排除名单,**只装易变字段**——那些
// statusDigest 里靠一行代码(比如 `s.StatusGeneration = 0`)主动清掉、
// 因为它们持续在变的字段。值是理由 —— 一条没有理由的排除,下一个人无从判断
// 它还该不该在名单里。
//
// **`json:"-"` 那一类字段不进这张名单**,由下面反射循环里的
// skipJSONIgnoredField 按标签自动识别、跳过。二者是两个不同的问题:名单回答
// 「这个字段为什么易变」,而 `json:"-"` 是「这个字段按构造就不可能出现在
// json.Marshal 的输出里」—— 前者需要 statusDigest 里有对应代码才成立(是
// **我们的选择**),后者零行代码就成立(是**语言保证**)。把二者混进同一张
// 名单,会要求有人在增删 json:"-" 标签时手动同步维护名单条目 —— 那正是
// 这个投影设计成「整个 Status 减一张名单」所要避免的手工簿记。
//
// 嵌套的易变字段(Core.LatencyMS 等)也不在这里,由
// TestVolatileNestedFieldsDoNotMoveTheDigest 单独钉。
var digestExclusions = map[string]string{
	"StatusGeneration": "代际号自己进投影就会让每次 bump 都让下一次比对不同,永久自激",
}

// skipJSONIgnoredField 报告一个顶层字段是否被 `json:"-"` 标记为绝不编码。
//
// **判据是 tag == "-",不是 strings.HasPrefix(tag, "-")。** `json:"-,"`
// 在 encoding/json 里表示「字段名字面就叫 -」,那种字段**是会被编码的**;
// 用前缀判断会把一个真正参与投影的字段静默跳过,而那正是这条守卫存在的
// 理由。
//
// **为什么这样比进 digestExclusions 更好,而不是等价的另一种写法**:它
// 自我纠正 —— 谁哪天去掉某个字段的 json:"-" 标签,那个字段就自动开始被
// json.Marshal 编码、自动开始参与投影,反射测试照样要求它要么移动投影、
// 要么被显式加进 digestExclusions 并写明「为什么易变」。没有一个需要记得
// 删除或新增的名单条目 —— 判据锚在标签本身,不是锚在某个人记不记得更新
// 一张表。
func skipJSONIgnoredField(field reflect.StructField) bool {
	return field.Tag.Get("json") == "-"
}

// **本测试是整个 watch 功能里最重要的一条。**
//
// 它钉住的不是「投影算得对」,而是「**没有字段被静默排除在 watch 之外**」:
// 将来谁往 Status 加一个字段,不改任何测试它也自动参与投影;要排除它就必须
// 显式加进 digestExclusions 并写明理由。
//
// 方向是刻意的(见 spec「投影」一节):默认参与,则加一个易变字段会让 watch
// 疯狂触发 —— 吵,但当场看得见;默认不参与,则加一个要紧字段会让菜单静默地
// 不再对它反应 —— 安静,只有用户抱怨时才发现。代价不对称。
func TestEveryStatusFieldParticipatesInTheDigest(t *testing.T) {
	base := representativeStatus()
	baseline, err := statusDigest(base)
	if err != nil {
		t.Fatalf("基准投影算不出来: %v", err)
	}

	typ := reflect.TypeOf(base)
	if typ.NumField() < 15 {
		t.Fatalf("Status 只有 %d 个字段,少得反常 —— 这条守卫可能已经读不到它要守的东西了", typ.NumField())
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		if skipJSONIgnoredField(field) {
			// 静默跳过字段的守卫,和一个静默排除字段的实现,是同一个问题 ——
			// 所以这条跳过本身要留痕。
			t.Logf("跳过 Status.%s:json:\"-\" 使它按构造不会出现在 json.Marshal 的"+
				"输出里,不需要(也不可能通过)本条守卫检验", field.Name)
			continue
		}
		mutated := representativeStatus()
		mutateFieldForDigest(t, field.Name, reflect.ValueOf(&mutated).Elem().Field(i))

		got, err := statusDigest(mutated)
		if err != nil {
			t.Fatalf("改了 %s 之后投影算不出来: %v", field.Name, err)
		}
		if why, excluded := digestExclusions[field.Name]; excluded {
			if got != baseline {
				t.Errorf("Status.%s 在排除名单里(%s),但改它改变了投影 —— 名单与实现不一致", field.Name, why)
			}
			continue
		}
		if got == baseline {
			t.Errorf("改了 Status.%s 而投影没变 —— 这个字段被**静默排除**在 watch 之外了。"+
				"要么它该参与(修 statusDigest),要么它是易变字段(加进 digestExclusions 并写明理由)。"+
				"静默不参与意味着菜单永远不会因为这个字段的变化而更新,而没有任何东西会报错", field.Name)
		}
	}
}

// mutateFieldForDigest 把一个字段改成一个与 representativeStatus 不同的值。
//
// **读不懂的类型必须响亮失败**,不许静默跳过:一个遇到新 Kind 就放过的守卫,
// 在最需要它的时候(有人加了个新类型的字段)恰好是失效的。
func mutateFieldForDigest(t *testing.T, name string, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		v.SetUint(v.Uint() + 1)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			t.Fatalf("字段 %s 是 %s 的切片,本守卫的变异器读不懂它 —— 请连同它一起扩写,别让它静默通过", name, v.Type().Elem())
		}
		v.Set(reflect.Append(v, reflect.ValueOf("digest-probe")))
	case reflect.Pointer:
		if v.IsNil() {
			t.Fatalf("字段 %s 在 representativeStatus 里是 nil —— fixture 必须每个字段都非零,否则这条守卫在它身上是空转的", name)
		}
		mutateFieldForDigest(t, name+".(elem)", v.Elem())
	case reflect.Struct:
		if t0, ok := v.Interface().(time.Time); ok {
			v.Set(reflect.ValueOf(t0.Add(time.Hour)))
			return
		}
		for i := range v.NumField() {
			f := v.Field(i)
			if f.Kind() == reflect.String && f.CanSet() {
				mutateFieldForDigest(t, name+"."+v.Type().Field(i).Name, f)
				return
			}
		}
		t.Fatalf("字段 %s 是结构体但找不到可改的字符串字段 —— 请扩写变异器", name)
	default:
		t.Fatalf("字段 %s 的类型 %s 本守卫读不懂 —— 请扩写变异器,别让它静默通过", name, v.Kind())
	}
}

// 易变字段单独改动时,投影**必须不动**。
//
// 每一条都是真机上会持续变的东西;不排除它们,watch 触发得比今天 30 秒轮询
// 还频,而这个坑在别的测试里发现不了(那些测试的 latency 是固定 fixture)。
func TestVolatileNestedFieldsDoNotMoveTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Status)
		why    string
	}{
		{"Core.LatencyMS", func(s *Status) { s.Core.LatencyMS = 412 }, "每次健康探测都抖"},
		{"Core.FailingRules[].Attempts", func(s *Status) { s.Core.FailingRules[0].Attempts = 12345 }, "每条连接都在涨"},
		{"Core.FailingRules[].Failures", func(s *Status) { s.Core.FailingRules[0].Failures = 12344 }, "每条连接都在涨"},
		{"Reconcile.At", func(s *Status) { s.Reconcile.At = time.Now() }, "recordReconcileRound 每轮都盖时间戳"},
		{"Reconcile.UnchangedRounds", func(s *Status) { s.Reconcile.UnchangedRounds++ }, "2026-08-17 真机 10 分钟 soak 实测:protection/desired 全程未变,watch 仍在 30s→10min 退避阶梯上精确唤醒 4 次;抓到的 status 15→16 diff 只剩 at 与 unchanged_rounds —— 循环自己数「又跑了一轮」被当成了「有什么变了」"},
		{"Recovery.UpdatedAt", func(s *Status) { s.Recovery.UpdatedAt = time.Now() }, "恢复进行中每次轮询都换"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := representativeStatus()
			tc.mutate(&s)
			got, err := statusDigest(s)
			if err != nil {
				t.Fatalf("投影: %v", err)
			}
			if got != baseline {
				t.Errorf("只改了 %s(%s)投影就变了 —— watch 会跟着它持续触发,比 30 秒轮询更差", tc.name, tc.why)
			}
		})
	}
}

// 反过来:同一条规则**开始**成片失败是真事件,必须让投影变。
// 只零计数、保留 Kind/Rule,就是为了这个。
func TestANewFailingRuleMovesTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影: %v", err)
	}
	s := representativeStatus()
	s.Core.FailingRules = append(s.Core.FailingRules, FailingRule{Kind: "proxy", Rule: "*.example.com"})
	got, err := statusDigest(s)
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	if got == baseline {
		t.Error("多了一条成片失败的规则而投影没变 —— 只该零掉计数,不该把整条规则也排除掉")
	}
}

// TestOneReconcileRoundDoesNotMoveTheDigest 是 2026-08-17 真机 soak 那个具体故障的
// 回归守卫,不是一个合成的单字段探针。
//
// soak 逮到的不是「单独改 UnchangedRounds」——那件事从来不会单独发生。一次
// recordReconcileRound 调用**同时**盖新的 At 与递增 UnchangedRounds(状态
// 15→16 抓到的 diff 逐字节就是这两个字段一起动、其余不动)。只测「单独改
// UnchangedRounds 不移动投影」测不出「两个一起改是否仍不移动」这种情况——
// 如果哪天两处排除各自实现成互相依赖的写法(比如一个在另一个非零时才生效),
// 单字段测试会绿而这条真实场景会红。
func TestOneReconcileRoundDoesNotMoveTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影: %v", err)
	}
	s := representativeStatus()
	// 模拟 recordReconcileRound 真实做的事:两个字段一起动。
	s.Reconcile.At = s.Reconcile.At.Add(30 * time.Second)
	s.Reconcile.UnchangedRounds++
	got, err := statusDigest(s)
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	if got != baseline {
		t.Error("一轮「什么都没变」的 reconcile(At 前进、UnchangedRounds 加一)移动了投影 —— " +
			"watch 会在机器完全静止时跟着调谐环每 30 秒到 10 分钟触发一次,永久唤醒,这正是真机 " +
			"soak 十分钟里抓到的那四次")
	}
}

// 上面两条排除不能把整个 Reconcile 判定成「不参与」—— Actions/Held/
// Unobservable 各自变化仍必须移动投影,否则调谐环真正想报的事件(要做什么 /
// 被什么栅栏挡住 / 观测瞎了哪一项)会跟着 At/UnchangedRounds 一起被静默吞掉。
// 没有这条测试,一次「干脆排除整个 ReconcileReport」的简化会让上面两条测试
// 全绿、而这才是真正的回归。
func TestReconcileSignalFieldsStillMoveTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Status)
	}{
		{"Actions", func(s *Status) { s.Reconcile.Actions = append(s.Reconcile.Actions, "restore_dns") }},
		{"Held", func(s *Status) { s.Reconcile.Held = "maintenance_hold" }},
		{"Unobservable", func(s *Status) { s.Reconcile.Unobservable = append(s.Reconcile.Unobservable, "capture_ok") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := representativeStatus()
			tc.mutate(&s)
			got, err := statusDigest(s)
			if err != nil {
				t.Fatalf("投影: %v", err)
			}
			if got == baseline {
				t.Errorf("改了 Reconcile.%s 而投影没变 —— 这是真事件,不该被 At/UnchangedRounds 的排除连累", tc.name)
			}
		})
	}
}

// **statusDigest 绝不许改到调用方那份 Status。**
//
// Core/Reconcile 是指针、FailingRules 是切片:复制 Status 只复制切片头,
// 在「副本」里清零改的是同一个底层数组,于是**真正发布出去的**那份 Status 里
// 计数变成 0。一个会污染它所要度量的东西的 digest,比没有 digest 更糟。
func TestStatusDigestDoesNotMutateItsInput(t *testing.T) {
	s := representativeStatus()
	if _, err := statusDigest(s); err != nil {
		t.Fatalf("投影: %v", err)
	}
	if s.Core.LatencyMS != 390 {
		t.Errorf("Core.LatencyMS 被 statusDigest 改成了 %d —— 它污染了调用方的 Status", s.Core.LatencyMS)
	}
	if s.Core.FailingRules[0].Attempts != 100 || s.Core.FailingRules[0].Failures != 99 {
		t.Errorf("FailingRules 的计数被清零了(%+v)—— 切片只复制了头,清零改的是同一个底层数组",
			s.Core.FailingRules[0])
	}
	if s.Reconcile.At.IsZero() {
		t.Error("Reconcile.At 被清零了 —— 指针指向的是调用方那份")
	}
	if s.Recovery.UpdatedAt.IsZero() {
		t.Error("Recovery.UpdatedAt 被清零了")
	}
}

// 同一份 Status 算两次必须相同 —— 否则代际号会每次重算都 bump。
func TestStatusDigestIsStable(t *testing.T) {
	a, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	b, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	if a != b {
		t.Fatalf("同一份 Status 两次投影不同(%s vs %s)—— watch 会永久自激", a, b)
	}
}
