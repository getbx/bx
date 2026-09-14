package leakcheck

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
	Path     string     `json:"path"` // "current" | "bypass"
	State    ReachState `json:"state"`
	// Detail 是给用户看的一句话。**绝不放原始错误** —— 它可能含内网地址、
	// 接口名、路径(与 Guardian 响应体只带失败码同一条纪律)。
	Detail string `json:"detail,omitempty"`
}
