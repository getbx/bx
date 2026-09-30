# CLAUDE.md — apps/ios(iPhone 开发构建)

本文件只在读到 `apps/ios/` 下的文件时加载。设计与分期在
`docs/superpowers/specs/2026-09-17-mobile-client-design.md`,施工与真机结果在
`docs/superpowers/plans/2026-09-29-mobile-phase2-ios-tunnel.md`;这里只放改这块之前必须知道的判据。

**形状**:数据面全是 libbox(sing-box 进程内,Packet Tunnel 扩展里);bx 贡献的是判断 ——
配置(`internal/mobileconfig`,出站由 `tunnel.SingboxOutbound` 给,与桌面同一个生成器)、
路由翻译(`internal/singboxrules`)、explain(`mobile/bxkit`,与 Mac 同一个 `internal/routerbuild`
与 `internal/explainwords`)。**手机上的判定必须与 Mac 相同**,守卫在 Go 那边
(`TestTranslatedRulesAgreeWithRouteExplainOnEveryInput`、`TestPhoneExplainAgreesWithTheMacAndWithThePhoneDataPlane`)。

## 改之前必须知道的

- **`includeAllNetworks()` 必须报真值**(`Tunnel/PlatformInterface.swift`)。sing-tun 按它选 TCP 栈;
  报成常量 false 时 kill-switch 构建 DNS 照通、TCP 全死,非常像别的故障(2026-09-29 真机)。
  `TestIOSKillSwitchWiringStaysHonest` 钉住。
- **kill-switch 的探测只打自己的服务器**(`App/RawProbe.swift`,目标取 `expect.json` 的 `server_host`)。
  那一枪若漏出去,只许落在本来就看得见我们家 IP 的那台机器上;**不做「无隧道基线」**(那一次请求
  本身就是泄漏)。同一条守卫钉住目标与唯一调用点。
- **每个 armed 场景在同一次启动里自己 `disarm`**(关按需、删配置)。`includeAllNetworks` 可能切断
  Mac 到手机的调试通道,不能指望事后再发一条 `remove`。测试结束一律 `scripts/ios-dev.sh run remove`。
- **三个标识符散在四个文件**(bundle id、App Group、扩展 id),漂了构建与签名照过、真机起不来。
  `TestIOSIdentifiersAgreeAcrossTheProject` 钉住。
- **链接是凭据**:`apps/ios/Dev/`(gitignored,0600)由 `cmd/bx-ios-devconfig` 以 root 生成,
  要 sudo 密码,**只能所有者在自己终端跑** `scripts/ios-dev.sh config`(`!` 没有 TTY)。
  `policy.json` 只有规则与 global(测试钉住不含链接)。模拟器截图用提交进仓库的合成夹具
  (`App/Fixtures/`,`--fixture`),绝不截所有者的规则。
- **v6 在手机上一律拒绝**(mobileconfig 的前导规则),explain 如实说 blocked;tun 必须同时占住
  v4 与 v6 默认路由,否则 iOS 把 v6 从物理网卡放出去。

## App 本身(第四期,2026-09-29)

- **配置在手机上生成**:粘贴 `bx://` / `vless://` → `bxkit.Configure`(`mobile/bxkit/configure.go`)出完整
  libbox 配置;出站走 `internal/singboxout`(与桌面同一个生成器,原在 tunnel,为此下沉成纯包)。
  这一期只有 reality,别的链接**按类型拒绝并说出来**,不生成一份连不上的配置。
- **手机跑的是桌面 `bx setup` 的默认路由**(split:china 直连,其余走隧道;china 列表从包里读,
  即仓库内嵌那两份)。Explain 用 `BxkitDefaultPolicy` 问同一份意图 —— 两者不会不一致。
  Mac 上用户自己加的规则经「规则同步」到手机(见下一条;设计
  `docs/superpowers/specs/2026-09-29-policy-sync-via-own-server-design.md`)。
- **链接是凭据,放钥匙串**(`App/LinkStore.swift`,本机、首次解锁后可读);扩展从不读它,只跑 App
  写进共享容器的配置(`Shared/SharedPaths.swift` 的 `writeStartConfig`,按需重连不带启动参数)。
- **保护开着就开 kill-switch**(`App/TunnelController.swift`);关的时候**先关按需再停**,否则 iOS
  立刻把隧道拉回来。
- **`--fixture` 不碰任何持久存储**(不写共享设置、不写钥匙串、不调 VPN 框架):一次写了共享设置的
  fixture 运行让下一次启动「已有服务器」,UI 测试当场抓到。
- **规则同步(拉)**:`sync.bx.internal` 是保留名,手机自己的配置把它送进隧道、改写到 VPS 回环上的
  存储(`internal/mobileconfig` 的 `SyncHost`,排在用户规则之前,任何直连规则截不走);隧道不在就哪都
  不通。保护开着时拉(打开时一次、之后每 30 分钟),拉到更新的版本就重生成配置并经
  `sendProviderMessage("reload")` 原地重载。**换服务器 = 换链接 ⇒ 旧的同步规则作废**。「保护」页的
  「规则」一行只在真同步到之后才说「From your Mac」;之前说默认,并说出是哪一种原因。明文 HTTP
  的 ATS 例外只给这个保留名(那条流量只在隧道里走,内容本身也封过)。
- 真机上走屏幕同一条路径的无头场景:`scripts/ios-dev.sh run app`(导入 → 开 → 探测 → 关 → 忘掉)。

## 界面的检查

`scripts/ios-dev.sh snapshot` 出截图给人(和 agent)看;`scripts/ios-dev.sh uitest` 跑 `UITests/` 里的
XCUITest 做判定:答案三行在屏幕上、说的与 Mac 相同、不越出窗口。**测试按无障碍标识找行**
(`explain.goes` / `explain.because` / `explain.rule`),不按 SwiftUI 怎么合并标签与值 —— 按文字找
「Direct」时它根本不是单独的元素。两者都只用 `--fixture` 的合成规则。**每项封顶 2 分钟**:断言失败后
XCUITest 抓整棵无障碍树做排查,本机实测会挂住半小时以上。

## 构建的几个坑(都实测过)

- 签名走 **Xcode 已登录的团队账号**;App Store Connect API key 过不了 provisioning(bearer token
  认证失败),同一把 key 的公证在 CI 上正常 —— 两个服务认证不同。
- libbox 要用 **sing-box 自己的 gomobile 分叉**(`sagernet/gomobile@v0.1.13`),上游不认 `-libname`;
  Bxkit 在临时包装模块里绑,不往 bx 的 `go.mod` 加构建依赖。
- libbox 引用 UIKit 的后台任务符号:**扩展要显式链 UIKit**。
- `verify.sh` 的 `ios app typecheck` 每次重绑 Bxkit 并编一遍 App;没有 Libbox.xcframework 时明说跳过。

## 真机未验

扩展被系统杀掉那一瞬间(约一秒的窗口,抓不稳;与「起不来」同一个系统状态,后者已验);
APNs(iOS 默认 `excludeAPNs`,推送长连接绕开隧道);用所有者真实规则跑 explain(要先重跑 `config`)。
