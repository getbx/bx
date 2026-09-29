// Package report 是问题上报的叶子包:报告的形状(Bundle)、脱敏(Redact)、本地留档兼
// 队列(Store)。纯逻辑 + 文件系统,不发网络 —— 发送那一跳住在 Guardian(它在隧道里)。
// 设计:docs/superpowers/specs/2026-09-29-bx-problem-reports-design.md
package report

// Bundle 是一份报告的全部内容。字段名就是收集端(tools/reports-collector/worker.js 的
// validate)认的形状;改一边必须改另一边。
type Bundle struct {
	Schema       int          `json:"schema"`
	InstallID    string       `json:"install_id"`
	BXVersion    string       `json:"bx_version"`
	OS           string       `json:"os"`
	Arch         string       `json:"arch,omitempty"`
	MacOSVersion string       `json:"macos_version,omitempty"`
	OccurredAt   string       `json:"occurred_at"`
	Signature    string       `json:"signature"`
	Failure      Failure      `json:"failure"`
	Protection   []Transition `json:"protection,omitempty"`
	Doctor       []Check      `json:"doctor,omitempty"`
	LogTail      []string     `json:"log_tail,omitempty"`
}

// Failure 全是码,不带原始错误串(Guardian 的失败码纪律)。
type Failure struct {
	Code      string `json:"code"`
	Stage     string `json:"stage,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// Transition 是一次保护状态的转换。
type Transition struct {
	At    string `json:"at"`
	State string `json:"state"`
	Code  string `json:"code,omitempty"`
}

// Check 是 doctor 的一项(名字 / 状态 / detail / hint),detail 与 hint 经脱敏。
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}
