//go:build !darwin && !linux

package guardian

// 其余平台维持今天的行为:install 侧对非 darwin 报「不支持」,而
// requireDaemonPlatform 在更早处就挡住了 —— 这条路今天不可达,原样保留
// 是为了不给「悄悄放开门」的人一个看起来能用的 DNS 管理器。
func newPlatformDNSManager(service string) DNSManager {
	return NewDNSManager(service)
}
