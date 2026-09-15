package guardian

import "context"

// linux 没有「DNS 接管」这件事:数据面整机劫持 + engine 拦 UDP:53 到任意
// 目的地(Mudi 真机 e2e 背书),系统 resolv.conf 一个字不碰。三个方法都
// 如实答 DNSNotNeeded、nil error —— 不许为了让状态好看伪造 managed=true,
// 也不能报 Unmanaged(那会让 Up 在完全健康的机器上恒失败于 dns_verification)。
type dataPlaneDNSManager struct{}

func (dataPlaneDNSManager) EnsureManaged(context.Context) (DNSStatus, error) {
	return DNSStatus{State: DNSNotNeeded}, nil
}

func (dataPlaneDNSManager) Inspect(context.Context) (DNSStatus, error) {
	return DNSStatus{State: DNSNotNeeded}, nil
}

func (dataPlaneDNSManager) Restore(context.Context) (DNSStatus, error) {
	return DNSStatus{State: DNSNotNeeded}, nil
}

// Baseline:本平台没有「DNS 接管」这件事 —— 这不是「还没问」,是**没有可问的**。
func (dataPlaneDNSManager) Baseline() DNSState { return DNSNotNeeded }

func newPlatformDNSManager(string) DNSManager {
	return dataPlaneDNSManager{}
}
