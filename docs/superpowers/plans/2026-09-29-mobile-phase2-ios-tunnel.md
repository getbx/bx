# 手机端第二期:最小 iOS Packet Tunnel + libbox,连上并验 fail-closed(implementation plan)

**Goal:** 在所有者的 iPhone(`cc`,iPhone SE 3,iOS 26.6.2)上跑一个无界面的 bx iOS 构建:容器 App +
Packet Tunnel 扩展(进程内 libbox 1.14.2),经所有者当前那台服务器出去;并在真机上回答 spec §4.4
那条未验的推理 —— **代理出站连不上时,连接是失败而不是直连**。

**Spec:** `docs/superpowers/specs/2026-09-17-mobile-client-design.md`(§5 iOS 约束、§8 分期②)。
**前置:** 第一期 `internal/singboxrules`(路由规则翻译 + 一致性守卫)。

## 形状

- **配置在 Mac 上生成,不在手机上生成**(这一期)。`internal/mobileconfig`(纯)把
  `singboxrules.Translate` 的路由、fake-IP DNS、tun 入站、三个出站拼成一份完整 libbox 配置;
  出站由 `tunnel.SingboxOutbound`(与桌面 `singboxConfig` **同一个生成器**)给。开发工具
  `cmd/bx-ios-devconfig` 以 root 读 `/etc/bx/config.yaml`,把配置写进 `apps/ios/Dev/`
  (gitignored,0600,chown 给所有者):**链接是凭据,不进仓库、不进对话**。
- **DNS 照桌面的 fake-IP**:A 查询一律答 `198.18/15` 假 IP,路由按还原出的域名判(与 bx 同构,
  第一期的一致性守卫因此在手机上仍然成立);AAAA 空答(与桌面 NODATA 同向);直连出站解析真实
  地址走 `local`(扩展自己的套接字不进隧道)。
- **v6 一律 reject**(桌面 v6 是 fail-closed 阻断);tun 同时占住 v4 与 v6 默认路由,否则 iOS 会把
  v6 从物理网卡直接放出去。
- **工程**:`apps/ios/project.yml`(XcodeGen),`Libbox.xcframework` 由
  `scripts/build-libbox-ios.sh` 从 sing-box 同一 tag 源码构建(用 sing-box 自己的 gomobile 分叉),
  不进仓库。只链进扩展。Bundle `com.getbx.bx.ios` / `.tunnel`,App Group `group.com.getbx.bx`,
  Team `XXXXXXXXXX`,自动签名 + App Store Connect API key。
- **驱动**:`scripts/ios-dev.sh <scenario>` 构建、装机、以启动参数拉起 App;App 装配置、起隧道、
  跑探测、把 JSON 结果打到 stdout(`devicectl --console` 收)后退出。场景:
  `connect`(真服务器,期望出口 == 服务器 IP)、`deadserver`(代理指向 192.0.2.1,期望请求**失败**)、
  `stop`、`remove`(删掉 VPN 配置,测试结束必须跑)。
- **探测不做无隧道基线**:不在隧道外发任何请求去「取真实 IP 作对照」—— 那本身就是一次主动泄漏。
  判据是「出口 == 服务器」与「服务器不可达时一个字节都拿不到」。

## 不做(这一期)

UI、订阅/同步、`includeAllNetworks` + on-demand(隧道没起时整机 fail-closed,那是 iOS 意义上的
kill-switch,第三期)、DNS 分流、UDP 档、china 直连的国内 DNS。

## Tasks

1. `tunnel.SingboxOutbound`:把 vless 出站抽成导出函数,`singboxConfig` 改用它;守卫钉「桌面配置里的
   出站 == SingboxOutbound 给的」。非 reality 链接报错(这一期只支持 reality)。
2. `internal/mobileconfig`:Build + 纯度守卫 + 内嵌 sing-box `check` 过;fake-IP / AAAA / v6 reject /
   hijack-dns 顺序各一条断言。
3. `cmd/bx-ios-devconfig`:读配置、写三个文件 + deadserver 变体、0600、chown。
4. `apps/ios` 工程 + `scripts/build-libbox-ios.sh` + `scripts/ios-dev.sh`;真机构建装机。
5. 真机:`connect`、`deadserver`、`remove`;结论写回 spec §4.4 与本计划。
