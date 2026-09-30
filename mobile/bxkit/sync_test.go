package bxkit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/policysync"
	"github.com/getbx/bx/mobile/bxkit"
)

// 手机拉到的就是 Mac 封好的那一团:同一条链接(手机上可能是 bx:// 换壳)打开得了、版本与规则原样。
func TestThePhoneOpensWhatTheMacSealed(t *testing.T) {
	mac, _ := policysync.Derive(fakeVless)
	blob, _ := policysync.Seal(mac, policysync.Policy{Version: 42, UpdatedAt: "2026-09-30T08:00:00Z", Global: true, Direct: []string{"*.apple.com"}})
	raw, err := bxkit.OpenSynced(blink.Encode(fakeVless), blob)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Version int64    `json:"version"`
		Global  bool     `json:"global"`
		Direct  []string `json:"direct"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Version != 42 || !p.Global || p.Direct[0] != "*.apple.com" {
		t.Fatalf("opened %s (%v)", raw, err)
	}
	// 另一条链接封的:打不开,且错误说得出是「来自另一条链接」(App 据此给那句话)。
	other := strings.Replace(fakeVless, "11111111", "66666666", 1)
	if _, err := bxkit.OpenSynced(other, blob); err == nil || !strings.Contains(err.Error(), "different link") {
		t.Fatalf("another link's blob: err = %v", err)
	}
}

// 请求地址:保留名 + 与 Mac 推送时同一个 BlobID。
func TestSyncURLPointsAtTheSameBlobTheMacPushes(t *testing.T) {
	u, err := bxkit.SyncURL(fakeVless)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := policysync.Derive(fakeVless)
	if u != "http://sync.bx.internal/v1/blob/"+keys.BlobID {
		t.Fatalf("SyncURL = %s", u)
	}
}

// 用同步来的规则生成配置:Mac 上的 direct 规则进了手机的路由;global 时 china 列表不再引用。
func TestConfigureWithPolicyUsesTheSyncedRules(t *testing.T) {
	raw, err := bxkit.ConfigureWithPolicy(fakeVless, `{"global":true,"direct":["*.apple.com"],"proxy":[]}`,
		string(embedded.ChinaDomain()), string(embedded.ChinaCIDR()))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Config string `json:"config"`
	}
	_ = json.Unmarshal([]byte(raw), &c)
	if !strings.Contains(c.Config, `"apple.com"`) {
		t.Fatal("the synced direct rule is not in the phone's routes")
	}
	if strings.Contains(c.Config, `"rule_set":["bx-china-domain"]`) {
		t.Fatal("global: the china list is still routed")
	}
	if _, err := bxkit.ConfigureWithPolicy(fakeVless, `{"direct":`, "", ""); err == nil {
		t.Fatal("a malformed policy must be an error, not silently the defaults")
	}
}
