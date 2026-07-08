package supervisor

// windns.go:Windows DNS-into-TUN 的哨兵地址(无 build tag,便于对不变量做跨平台单测)。
// 真正调用 winipcfg SetDNS 的在 platform_windows.go。

// tunDNSSentinel 是给 Windows TUN 适配器设的 DNS 服务器地址。
//
// 为什么需要:Windows 系统 DNS 常指向 LAN 路由器(如 192.168.1.1)——它在私网 bypass 段、
// 走物理网卡且被 WFP 封 off-TUN :53 → 若不干预,DNS 会整个断。把 TUN 适配器的 DNS 设成一个
// **会路由进 TUN** 的公网地址后,系统的 DNS 查询就进 TUN,由 bx 的 fake-IP handler 就地应答
// (engine.go 拦截 UDP:53 到**任意**目的地,不看目的 IP)。TUN 接口 metric 已置 0(最优),
// 系统优先用它的 DNS;适配器随 closeTUN 销毁,该设置自动消失,无需持久还原。
//
// 不变量:必须是**公网、不在私网 bypass 段**的 IPv4(否则会绕过 TUN,DNS 进不了 bx)。
// 取 1.1.1.1 只为「明确公网、稳定、易识别」——它不会被真正连上(bx 就地应答 :53)。
const tunDNSSentinel = "1.1.1.1"
