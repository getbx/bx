package srvgen

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRealityUsersLifecycle(t *testing.T) {
	rp, _ := GenerateReality("1.2.3.4", "", 443)
	cfg, _ := rp.ServerConfig()

	users, err := realityUserIDs(cfg)
	if err != nil || len(users) != 1 || users[0] != rp.UUID {
		t.Fatalf("初始应 1 用户(主 uuid): %v %v", users, err)
	}

	// 加用户
	cfg2, err := AddRealityUser(cfg, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	users, _ = realityUserIDs(cfg2)
	if len(users) != 2 || !contains(users, "11111111-2222-4333-8444-555555555555") {
		t.Fatalf("应 2 用户含新 uuid: %v", users)
	}
	// 新用户必须带 flow=xtls-rprx-vision(否则 vision 不一致连不上)
	if strings.Count(string(cfg2), "xtls-rprx-vision") != 2 {
		t.Errorf("新用户应带 vision flow:\n%s", cfg2)
	}
	// 重复加应报错
	if _, err := AddRealityUser(cfg2, "11111111-2222-4333-8444-555555555555"); err == nil {
		t.Error("重复 uuid 应报错")
	}

	// 删用户
	cfg3, err := RemoveRealityUser(cfg2, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	users, _ = realityUserIDs(cfg3)
	if len(users) != 1 || users[0] != rp.UUID {
		t.Fatalf("删后应回 1 用户: %v", users)
	}
	// 删不存在应报错
	if _, err := RemoveRealityUser(cfg3, "no-such-uuid"); err == nil {
		t.Error("删不存在 uuid 应报错")
	}
}

// 合体配置(reality + hys2 两入站)里加 reality 用户,只动 vless 入站。
func TestAddRealityUserInCombined(t *testing.T) {
	rp, _ := GenerateReality("1.2.3.4", "", 443)
	hp, _ := GenerateHysteria2("1.2.3.4", "", 443)
	cfg, _ := CombinedServerConfig(rp, hp)
	cfg2, err := AddRealityUser(cfg, "11111111-2222-4333-8444-555555555555")
	if err != nil {
		t.Fatalf("add in combined: %v", err)
	}
	users, _ := realityUserIDs(cfg2)
	if len(users) != 2 {
		t.Errorf("合体里 reality 应 2 用户: %v", users)
	}
	// hys2 入站不受影响
	if !strings.Contains(string(cfg2), `"hysteria2"`) || !strings.Contains(string(cfg2), hp.Password) {
		t.Error("不该动 hys2 入站")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// realityUserIDs 列出 reality 入站的所有 uuid —— 读回 AddRealityUser /
// RemoveRealityUser 的结果用。生产上没有「列出用户」这件事(share 只加、revoke
// 只删),所以它住在测试里;定位入站走的是生产那份 realityInbound。
func realityUserIDs(configBytes []byte) ([]string, error) {
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return nil, err
	}
	_, users, err := realityInbound(cfg)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(users))
	for _, u := range users {
		if m, ok := u.(map[string]any); ok {
			if id, _ := m["uuid"].(string); id != "" {
				out = append(out, id)
			}
		}
	}
	return out, nil
}
