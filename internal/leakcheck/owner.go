package leakcheck

import "strings"

// TunnelOwner 回答「一个公网包实际上归谁管」。**它是这个功能所有结论的挂靠点**:
// 一条结论说「WebRTC 绕过了隧道」之前,总得先有一条隧道。
//
// 零值是 OwnerUnknown,与 Verdict / tristate.Tristate 同一条纪律:**没问出来是零值,
// 不是某个具体答案**。
type TunnelOwner uint8

const (
	// OwnerUnknown:没问出来。**不是「没有隧道」。**
	OwnerUnknown TunnelOwner = iota
	// OwnerNone:出口接口是认得出的物理网卡 —— 没有任何隧道在管。
	OwnerNone
	// OwnerBX:出口接口就是 bx 的 TUN。
	OwnerBX
	// OwnerOther:出口接口是隧道类接口,但不是 bx 的 —— 别人的 VPN 在管。
	// 这一态是「拿 bx 检测别的 VPN」那个用法的立足点。
	OwnerOther
)

func (o TunnelOwner) String() string {
	switch o {
	case OwnerNone:
		return "none"
	case OwnerBX:
		return "bx"
	case OwnerOther:
		return "other"
	default:
		return "unknown"
	}
}

// physicalIfacePrefixes 是认得出的物理/本机接口名。**这份清单是白名单**,
// 只有落在它里面的名字才配支撑「你没有隧道」这句话。
//
// 隧道那一侧不在这里 —— 它由 describe.go 的 IsTunnelInterface 回答,**全仓唯一
// 一份**:两处各写一份隧道前缀表时会静默漂移,而 WhoOwnsTheRoute 与 isTunnelPath
// 对同一台机器必须给出一致的答案。
var physicalIfacePrefixes = []string{
	"en", "eth", "wlan", "wl", "bridge", "awdl", "llw", "anpi", "ap", "lo",
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// WhoOwnsTheRoute 判断公网流量当前归谁管。**纯函数**,只读 LocalFacts。
//
// 判据取自 DefaultRouteV4,而采集器问的是 `route -n get 1.1.1.1` —— 即「一个公网包
// 实际从哪个接口离开」。**这一点是承重的**:bx 用 split-default(0/1 + 128/1),
// 真正的 `default` 路由仍然指着物理网关,问 `default` 会在 bx 开着时报出物理网卡。
// 别家 VPN 大多也用同一招,所以这个探法对「检测别人的隧道」一并成立。
//
// **认错方向的代价不对称。** 把物理网卡误判成隧道,等于把一台裸奔的机器说成受保护 ——
// 与 ipify 那次同一类的、方向相反的谎。所以:
//
//   - 认得出是隧道 → OwnerBX / OwnerOther
//   - 认得出是物理接口 → OwnerNone
//   - **认不出 → OwnerUnknown**,既不说有也不说没有
func WhoOwnsTheRoute(local LocalFacts) TunnelOwner {
	ref := local.DefaultRouteV4
	if !ref.Known() {
		return OwnerUnknown
	}
	name := strings.ToLower(strings.TrimSpace(ref.Name))
	if name == "" {
		return OwnerUnknown
	}

	// bx 自己的 TUN 优先认:它本身就是 utun*,先比名字才分得开「bx 在管」与
	// 「别人在管」。BXTunInterface 为空表示 Guardian 没答上话,不表示 bx 没在跑 ——
	// 那种情况下这条比较自然落空,归到 OwnerOther,而 OwnerOther 的措辞不会
	// 冒认 bx 的功劳。
	if bx := strings.ToLower(strings.TrimSpace(local.BXTunInterface)); bx != "" && name == bx {
		return OwnerBX
	}
	if IsTunnelInterface(name) {
		return OwnerOther
	}
	if hasAnyPrefix(name, physicalIfacePrefixes) {
		return OwnerNone
	}
	return OwnerUnknown
}
