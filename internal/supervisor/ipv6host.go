package supervisor

import "net"

// ipv6HostEnabled 判断宿主是否有可用 IPv6(任一非 loopback 接口带 v6 地址 ⇒ v6 栈活跃)。
// 缺席即无 v6 可漏,跳过 v6 阻断,避免在禁用 v6 的机器上做无谓的 v6 配置。
// darwin 与 windows 的 Hijack 共用此判断(无 build tag,两平台单一真相源,避免逐字重复各自漂移)。
func ipv6HostEnabled() bool {
	enabled, err := ipv6HostEnabledWithError()
	return err == nil && enabled
}

func ipv6HostEnabledWithError() (bool, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := ipn.IP; ip.To4() == nil && ip.To16() != nil && !ip.IsLoopback() {
			return true, nil
		}
	}
	return false, nil
}
