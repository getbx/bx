package observe

import "encoding/json"

// MarshalJSON 让三态在 JSON 里是 "true"/"false"/"unknown" 而非数字。
// 数字对 agent 无意义,而本包的全部价值在于让 agent 读懂。
func (t Tristate) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}
