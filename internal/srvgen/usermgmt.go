package srvgen

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// PubKeyFromPrivate 由 reality 私钥(base64url x25519)推导公钥(pbk)。
// 服务端 sbserver.json 只存 private_key,share/link 重建客户端链接时要用它算 pbk。
func PubKeyFromPrivate(priv string) (string, error) {
	pb, err := base64.RawURLEncoding.DecodeString(priv)
	if err != nil {
		return "", fmt.Errorf("私钥 base64url 解码: %w", err)
	}
	k, err := ecdh.X25519().NewPrivateKey(pb)
	if err != nil {
		return "", fmt.Errorf("私钥非合法 x25519: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// NewUUID 生成一个 RFC4122 v4 UUID(供 share 给新用户分配)。
func NewUUID() (string, error) { return uuidV4() }

// realityInboundUsers 在解析后的配置里定位 vless(reality)入站的 users 切片指针。
// 返回 inbound map、users([]any)、以及它在 inbounds 里的位置;找不到 vless 入站报错。
func realityInbound(cfg map[string]any) (map[string]any, []any, error) {
	ins, ok := cfg["inbounds"].([]any)
	if !ok {
		return nil, nil, fmt.Errorf("配置缺 inbounds")
	}
	for _, in := range ins {
		m, ok := in.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "vless" {
			users, _ := m["users"].([]any)
			return m, users, nil
		}
	}
	return nil, nil, fmt.Errorf("配置里没有 vless(reality)入站")
}

// RealityUsers 列出 reality 入站的所有 uuid。
func RealityUsers(configBytes []byte) ([]string, error) {
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

// AddRealityUser 给 reality 入站加一个用户(带 vision flow)。uuid 已存在则报错。
func AddRealityUser(configBytes []byte, uuid string) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return nil, err
	}
	in, users, err := realityInbound(cfg)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if m, ok := u.(map[string]any); ok && m["uuid"] == uuid {
			return nil, fmt.Errorf("uuid %s 已存在", uuid)
		}
	}
	in["users"] = append(users, map[string]any{"uuid": uuid, "flow": "xtls-rprx-vision"})
	return json.MarshalIndent(cfg, "", "  ")
}

// RemoveRealityUser 从 reality 入站删一个用户。uuid 不存在则报错。
func RemoveRealityUser(configBytes []byte, uuid string) ([]byte, error) {
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return nil, err
	}
	in, users, err := realityInbound(cfg)
	if err != nil {
		return nil, err
	}
	kept := make([]any, 0, len(users))
	found := false
	for _, u := range users {
		if m, ok := u.(map[string]any); ok && m["uuid"] == uuid {
			found = true
			continue
		}
		kept = append(kept, u)
	}
	if !found {
		return nil, fmt.Errorf("uuid %s 不存在", uuid)
	}
	in["users"] = kept
	return json.MarshalIndent(cfg, "", "  ")
}
