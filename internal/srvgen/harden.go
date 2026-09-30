package srvgen

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/getbx/bx/internal/policysync"
)

// HardenedRoute 是服务端 sing-box 的路由:**不许经隧道去 VPS 自己的回环、内网与链路本地地址**。
//
// 此前服务端只有一个 direct 出站、没有路由规则,于是任何持有链接的人 —— 包括
// `bx server share` 分出去的朋友 —— 都能经隧道连到 VPS 上只听 127.0.0.1 的服务、VPS 所在
// 的内网,以及云厂商的元数据地址(169.254.169.254,常有实例凭据)。
//
// **先 resolve 再判 IP**:目的地是域名时(`localhost`,或一个解析到私网的名字),不解析的话
// IP 规则根本看不见它。harden_test.go 用真 sing-box 对 127.0.0.1 与 localhost 各验一遍,并带
// 「没有这套路由时连得上」的对照组。
func HardenedRoute() map[string]any { return hardenedRouteWithStorePort(policysync.StorePort) }

// HardenedRouteForTest is HardenedRoute with the sync-store exception on another port, so tests
// running in parallel packages do not fight over the one fixed port.
func HardenedRouteForTest(storePort int) map[string]any { return hardenedRouteWithStorePort(storePort) }

func hardenedRouteWithStorePort(storePort int) map[string]any {
	return map[string]any{
		"rules": []any{
			map[string]any{"action": "resolve"},
			// 唯一的例外:VPS 上只听回环的规则同步存储(policysync.StorePort)。持有链接的设备经
			// 隧道存取自己那团加密规则;别的回环端口照样拒。
			map[string]any{"ip_cidr": []any{"127.0.0.1/32"}, "port": []any{storePort}, "action": "route", "outbound": "direct"},
			map[string]any{"ip_is_private": true, "action": "reject"},
			map[string]any{"ip_cidr": []any{
				"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10",
				"::/128", "::1/128", "fe80::/10", "fc00::/7",
			}, "action": "reject"},
		},
		"final": "direct",
	}
}

// Harden 给一份已有的服务端配置装上 HardenedRoute。只动 route,入站(钥匙、用户)一个字节不碰;
// 已经是这套路由时 changed=false、原样返回(bx server harden 可以放心重跑)。
func Harden(configBytes []byte) ([]byte, bool, error) {
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return nil, false, err
	}
	if _, ok := cfg["inbounds"]; !ok {
		return nil, false, errors.New("not a sing-box server config (no inbounds)")
	}
	want := HardenedRoute()
	// 比较走 JSON 往返,让 []any/map[string]any 与解码出来的形状一致。
	var wantDecoded any
	b, _ := json.Marshal(want)
	_ = json.Unmarshal(b, &wantDecoded)
	if reflect.DeepEqual(cfg["route"], wantDecoded) {
		return configBytes, false, nil
	}
	cfg["route"] = want
	out, err := json.MarshalIndent(cfg, "", "  ")
	return out, true, err
}
