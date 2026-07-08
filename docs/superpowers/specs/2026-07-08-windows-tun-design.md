# Windows 透明代理设计方向(第 3 步 · 网络层实现)

> 目的:调研当前成熟产品(sing-box / WireGuard / Xray / shadowsocks-rust)在 Windows 上做
> 透明 TUN 代理的方式,给 bx 的 `platform_windows.go` 真机实现定方向。第 1/2 步(交叉编译 +
> CI 三平台矩阵)已完成;本文是第 3 步的施工图。真机环境已就绪(`030-SJWJ-GSR-B`,
> Win10 19044,SSH 提权可达,`bx.exe` 已能执行)。

## 0. 关键结论(先看这个)

- **架构上 bx 已经很近**:bx = gVisor netstack + wireguard-go `tun.Device`(`wgbridge.go`,已覆盖
  `darwin||windows`)。这正是 sing-box 的 **gvisor stack** 路线。**Windows OpenTUN ≈ 照抄 darwin 那条**
  (都走 wgbridge),`wgtun.CreateTUN()` 在 Windows 上内部就是 wintun。**最难的 TUN 集成 90% 已就位。**
- **三个平台方法各有明确的 Windows 机制**(下面逐一):OpenTUN=wintun、DirectDialer=`IP_UNICAST_IF`、
  Hijack=路由表 + WFP(仅防泄漏那层)。
- **新增的工程量主要是**:① wintun.dll 分发;② `IP_UNICAST_IF` 防环;③ 路由劫持 + Windows 特有的
  DNS 泄漏封堵;④ Windows Service 层。

## 1. OpenTUN —— wintun(几乎白送)

- **驱动**:[wintun](https://www.wintun.net/)(WireGuard 出品,MIT,Layer-3 TUN)。**唯一受支持的分发方式 =
  签名的 `wintun.dll` 与应用同目录**(amd64/arm64/x86/arm 各一);wireguard-go 的 `tun.Device` 在
  Windows 上就是加载 `wintun.dll` 建适配器。
- **bx 落地**:`platform_windows.OpenTUN` 照 `platform_darwin` 的写法——`wgtun.CreateTUN(name, mtu)`
  → `wgbridge.NewWGEndpoint(dev, mtu)` → 交给 gVisor `tun.New`。`wgbridge.go` 已 `//go:build darwin || windows`,
  **不用改**。go.mod 里 `golang.zx2c4.com/wireguard` 已是依赖,wintun 绑定随之而来,**只差运行时的 DLL**。
- **分发 wintun.dll**:release .zip 里 `bx.exe` + `wintun.dll` 同目录;或内嵌进二进制、首次运行释放到
  `%ProgramData%\bx\wintun.dll`(参考 `provision.Ensure*` 的释放模式)。**embed + 释放**最符合 bx"单文件"哲学,
  但 DLL 是签名二进制、体积~几百 KB,内嵌可接受。
- **stack 选择**:保持 **gVisor**(userspace,跨平台一致,bx 一贯)。sing-box 的 system stack 更快但要更深耦合
  wintun;bx 不需要。注:sing-box gvisor 在 Windows 有个"按进程名分流查找失败"的 issue([#2823](https://github.com/SagerNet/sing-box/issues/2823)),
  但 **bx 不做按进程分流,N/A**。

## 2. DirectDialer —— `IP_UNICAST_IF`(防环,Windows 版 SO_MARK)

- **机制**:[`IP_UNICAST_IF`](https://github.com/XTLS/Xray-core/issues/2793) 设置出站包的出口接口——
  Windows 上等价于 Linux `SO_MARK` / macOS `IP_BOUND_IF`。**优点**:只影响出站(不碰入站)、**不需要管理员**、
  避免绕回 TUN。bx 自身出站(直连决策/DNS/socks 拨号)绑到**物理网卡的接口 index** 即防环。
- **参考实现**:**WireGuard 的 Windows bind**(`wireguard-go/conn/bind_windows.go`)——直接照抄它设
  `IP_UNICAST_IF`(v4)与 `IPV6_UNICAST_IF`(v6)的方式。
- **坑**:`IP_UNICAST_IF` 的值是接口 index,但 **IPv4 要按网络字节序**(`htonl(index)`),v6 是主机序——
  这是最常见的错;WireGuard 的实现已处理,照抄别自己拼。
- **bx 落地**:`platform_windows.DirectDialer()` 返回一个 `*net.Dialer`,其 `Control` 回调在 socket 上设
  `IP_UNICAST_IF` = 物理默认网卡 index(用 `route print` / `GetBestInterface` 探当前默认路由的接口)。

## 3. Hijack —— 路由表劫持 + WFP 防泄漏 + IPv6 fail-closed

sing-box 的 `auto_route` 在 **Windows 是直接编程路由表**(不是 WFP 做主劫持);WFP 只在
`strict_route` 那层做防泄漏。bx 照此分两层:

**(a) 主劫持 = 路由表**(参考 macOS 的 split-default,bx 已有 darwin 版思路):
- 装 `0.0.0.0/1` + `128.0.0.0/1` 两条 `/1` 经 TUN(盖住 `0/0` 又不动原 default,便于还原),低 metric。
- **bypass(必须)**:① **代理服务器 IP** → 走原网关(防环,brook/sing-box 子进程到服务器的连接);
  ② **LAN / 私网**;③ **管理网 / SSH 源**——真机联调时**务必 bypass 我的 SSH 源 IP**,否则劫路由即断连
  (和 Mudi 一样,配 `--test-timeout` 死手兜底)。
- 还原:defer 拆两条 `/1` + bypass 路由。`RehijackRoutes` 重探网关重装。

**(b) 防泄漏 = WFP + Windows DNS 特性**(这是 Windows 独有、bx 的 no-leak 保证必须处理的):
- **Windows "smart multi-homed name resolution"**:系统会**并行往所有接口发 DNS 查询** → 即使默认路由进 TUN,
  DNS 仍可能从物理网卡漏出。sing-box 用 **WFP filter 封掉非 TUN 接口的 :53**([strict_route](https://sing-box.sagernet.org/configuration/inbound/tun/))。
  bx 必须同等处理:**WFP 阻断非 bx-TUN 接口的 UDP/TCP :53**,或用 `netsh`/注册表关掉 smart-multihomed。
- **kill-switch / IPv6 fail-closed**:隧道挂 → Proxy 决策 Block(数据面已保证,平台无关);IPv6 装
  `::/1`+`8000::/1` 经 TUN 或 WFP 黑洞(对齐 Linux/mac 的 v6 fail-closed 决策)。
- `auto_redirect`(sing-box 的 nftables 重定向)**只 Linux 有,Windows 无等价**,不用管。

## 4. Windows Service 层(`bx up/down`)

- systemd/launchd 的对应物 = **Windows Service**,用 `golang.org/x/sys/windows/svc`(需加依赖)。
  `bx up` = 装 + 起服务(`svc.Install` 风格),`bx down` = 停 + 卸。服务以 LocalSystem 跑(有
  TUN/路由/驱动权限)。
- 参考:sing-box 官方多靠 GUI launcher 提权跑;bx 走原生 Service 更符合 `bx up/down` 语义。

## 5. 分发

- Release **.zip**:`bx.exe` + `wintun.dll`(amd64;arm64 备一份)。或内嵌 wintun.dll 首次释放。
- brook/sing-box 在 Windows **无内嵌**(第 1 步已设 nil 兜底)→ `provision.Ensure*` 走**下载**;
  需补 windows 的下载 URL / 或后续也内嵌 windows 版 brook/sing-box(同 linux/darwin 覆盖)。

## 6. 施工顺序(真机 SSH 迭代,每步可验、循序不威胁 SSH)

按"先不碰路由 → 再碰路由(带死手)"的安全梯度:

1. **OpenTUN**:`bx run` 建 wintun 适配器 → `ipconfig` 里看到 bx TUN、可 ping TUN 地址 → 退出干净移除。
   **不装路由**,零风险。先坐实 wintun + wgbridge 通。
2. **DirectDialer**:`IP_UNICAST_IF` 绑物理网卡,验 bx 自身出站不绕 TUN(BX_DEBUG 看直连出口)。
3. **Hijack(带 `--test-timeout` 死手 + bypass SSH 源)**:装 `/1` 路由 → 整机流量进 TUN → 验出口==VPS;
   死手到点自动还原。**这步最危险,死手 + bypass 管理 IP 是保命线**。
4. **防泄漏验证**:WFP 封 :53 off-TUN 后,验 DNS 不漏(`nslookup` 走 TUN)、WebRTC 出口==VPS
   (browserleaks),隧道挂 → fail-closed。
5. **Windows Service**:`bx up/down` 装/停服务,重启存活。

## 参考

- [Wintun – Layer 3 TUN Driver for Windows](https://www.wintun.net/) · [WireGuard/wintun](https://github.com/WireGuard/wintun)
- [wireguard-go tun_windows.go](https://github.com/WireGuard/wireguard-go/blob/master/tun/tun_windows.go)(wintun 集成)+ `conn/bind_windows.go`(`IP_UNICAST_IF` 参考实现)
- [sing-box TUN inbound](https://sing-box.sagernet.org/configuration/inbound/tun/)(auto_route/strict_route/WFP DNS 封堵)
- [Xray #2793 sockopt interface binding on Windows](https://github.com/XTLS/Xray-core/issues/2793) · [shadowsocks-rust #1266](https://github.com/shadowsocks/shadowsocks-rust/issues/1266)(`IP_UNICAST_IF` 坑)
