//go:build windows

// dnsleak.go 是 bx 在 vendored WireGuard firewall 包上加的**薄入口**:只装 bx 需要的三条
// WFP 过滤器封堵 Windows smart-multihomed DNS 泄漏,刻意**不装 blockAll**——bx 是分流代理,
// china/direct/bypass 流量合法走物理网卡,全封会打死。其余文件(blocker/rules/helpers/
// *_windows*)是 golang.zx2c4.com/wireguard/windows/tunnel/firewall v1.0.1 逐字 vendor(MIT,
// 见各文件 SPDX 头),仅三处 bx 适配:① 包名 firewall→winfw;② 给非 _windows 后缀文件
// (blocker/rules/helpers)补 //go:build windows;③ arch 文件约束补 windows(types_windows_64.go
// 的 `amd64||arm64`→`windows && (amd64||arm64)`,_32 同理)——上游整模块本就 windows-only 无需,
// bx 跨平台则必须,否则 Linux 构建会误编译。之所以 vendor 而非直接依赖:上游导出的
// EnableFirewall 恒带 blockAll 全封,不适配分流;所需的 blockDNS/permitTunInterface/
// permitWireGuardService 均为未导出,只能同包调用。
package winfw

import (
	"errors"
	"net/netip"
)

// BlockDNSLeak 装 WFP 动态过滤器,封堵 Windows「smart multi-homed name resolution」DNS 泄漏
// (系统并行往所有网卡发 DNS 查询 → 即便默认路由进 TUN,:53 仍可能从物理网卡漏出)。装三条:
//   - permitWireGuardService(weight 15):放行**当前进程**(bx.exe)的连接——bx 自己的 china
//     DNS resolver 经物理网卡发 :53 靠此放行(上游函数按当前 exe 的 app-id 匹配,名字虽叫
//     WireGuard,实际放行的是调用者进程)。
//   - permitTunInterface(weight 14):放行经 bx TUN(tunLUID)的一切(含 :53)——**进 TUN 的
//     DNS 必须能通**(fake-IP 解析靠它),故权重刻意 > blockDNS 的 deny。
//   - blockDNS(weightAllow 13 / weightDeny 12):封所有其余 off-TUN :53(UDP/TCP、v4/v6)。
//
// ⚠️ 权重关系是正确性核心:permitSelf(15) > permitTun(14) > blockDNS deny(12)。WFP 同
// sublayer 内最高权重的匹配过滤器胜(无 hard-block flag),故:进-TUN :53 被 permitTun 放行、
// bx 自身 :53 被 permitSelf 放行、其余 off-TUN :53 落到 blockDNS 被封。**不可照抄上游 EnableFirewall
// 的 12/14**——那里 deny(14)>permitTun(12),靠 restrictToDNSServers 例外放行隧道 DNS;bx 不依赖
// 特定 DNS 服务器 IP,要让整个进-TUN :53 都通,故必须 permitTun > deny。
//
// 动态会话(FLAG_DYNAMIC)在进程退出/崩溃时自动移除全部过滤器 → 天然 fail-safe,不会像路由
// 那样残留把机器 DNS 锁死;DisableDNSLeak 亦可主动清。permitDNSServers 传 nil 即封尽所有
// off-TUN/非本进程 :53(最紧);需额外放行特定 DNS(如内网解析器 IP)时传其地址。
func BlockDNSLeak(tunLUID uint64, permitDNSServers []netip.Addr) error {
	if wfpSession != 0 {
		return errors.New("winfw: DNS 泄漏防护已启用")
	}
	session, err := createWfpSession()
	if err != nil {
		return wrapErr(err)
	}
	installer := func(session uintptr) error {
		bo, err := registerBaseObjects(session)
		if err != nil {
			return wrapErr(err)
		}
		if err := permitWireGuardService(session, bo, 15); err != nil { // 放行 bx 自身进程 :53
			return wrapErr(err)
		}
		if err := permitTunInterface(session, bo, 14, tunLUID); err != nil { // 进 TUN 的 :53 必须通(> deny)
			return wrapErr(err)
		}
		if err := blockDNS(permitDNSServers, session, bo, 13, 12); err != nil { // 封 off-TUN :53
			return wrapErr(err)
		}
		return nil
	}
	if err := runTransaction(session, installer); err != nil {
		fwpmEngineClose0(session)
		return wrapErr(err)
	}
	wfpSession = session
	return nil
}

// DisableDNSLeak 主动移除 WFP 过滤器(关动态会话)。幂等;进程退出时会话本就自动清。
func DisableDNSLeak() { DisableFirewall() }
