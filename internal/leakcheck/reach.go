package leakcheck

// ReachPathCurrent / ReachPathBypass 是 ReachProbe.Path 仅有的两个合法值,
// 是 internal/leakcheck 与 internal/leakserve 之间的**跨包契约**。
//
// **导出而不是各写一份字面量**:探测名(ProbeExitV4 一类)曾经就是这样漂过一次 ——
// 「两边用同一组常量」那句话一度只是注释里的一句假话,leakserve 的页面 JS 自己
// 手抄了一份,对不上时那一格永远停在「还在等」,而两侧测试都绿。`internal/udpsource`、
// `internal/barriercidr` 也是为同一个理由下沉成叶子包的:目的是让**漂移在构造上
// 不可能**,不是靠一条守卫去追一份手抄。这两个值只有两处消费方(本包的
// judgeReach、leakserve 的 ProbeReach/测试),导出它们、两边都引用,编译器本身
// 就是那道门 —— 改值两边一起变,不需要再写一条守卫。
const (
	ReachPathCurrent = "current"
	ReachPathBypass  = "bypass"
)

// ReachProbe 是一个可达性目标在一条路径上的探测结果。
//
// **类型定义放在这里而不是 internal/leakserve,是一条协调者裁决**:
// `internal/leakserve/facts_linux.go` 已经 import 本包,而 Task 5 要让
// `leakcheck.LocalFacts` 带上探测结果 —— 若 `ReachProbe` 定义在 leakserve,
// leakcheck 就得反过来 import leakserve,形成循环依赖。本类型只有 string 与
// 枚举字段,是纯数据,不做 I/O,放进这个纯判据包不破坏 purity_test.go 的纪律;
// 真正做拨号/发请求的执行逻辑(DialFunc、ProbeReach、probeOne)仍然留在
// internal/leakserve —— 那些本来就该在那里。
type ReachProbe struct {
	TargetID string     `json:"target_id"`
	Path     string     `json:"path"` // ReachPathCurrent | ReachPathBypass
	State    ReachState `json:"state"`
	// Detail 是给用户看的一句话。**绝不放原始错误** —— 它可能含内网地址、
	// 接口名、路径(与 Guardian 响应体只带失败码同一条纪律)。
	Detail string `json:"detail,omitempty"`
}
