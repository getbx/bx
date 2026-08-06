package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

// 三态必须序列化成字符串。数字对 agent 无意义,而本包的全部价值在于让 agent 读懂。
func TestTristateMarshalsAsString(t *testing.T) {
	payload, err := json.Marshal(map[string]Tristate{"a": True, "b": False, "c": Unknown})
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, want := range []string{`"true"`, `"false"`, `"unknown"`} {
		if !strings.Contains(got, want) {
			t.Errorf("序列化结果缺少 %s,实际 = %s", want, got)
		}
	}
	if strings.Contains(got, ":0") || strings.Contains(got, ":1") || strings.Contains(got, ":2") {
		t.Errorf("三态不得序列化为数字,实际 = %s", got)
	}
}

// 观测结构整体序列化时,未观测的项必须显式出现为 "unknown",不得因 omitempty
// 之类的原因消失——字段缺席会被消费者读成"没问题"。
func TestObservedStateMarshalsUnknownExplicitly(t *testing.T) {
	payload, err := json.Marshal(ObservedState{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, field := range []string{"capture_ok", "barrier_present", "dns_managed", "core_socket", "tunnel_healthy"} {
		if !strings.Contains(got, `"`+field+`":"unknown"`) {
			t.Errorf("未观测的 %s 必须显式为 \"unknown\",实际 = %s", field, got)
		}
	}
}
