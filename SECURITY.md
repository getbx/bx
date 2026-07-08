# 安全说明 / Security

bx 是一个隐私优先的透明全局代理。作为一个"防止真实 IP 泄漏"的工具,它的价值取决于安全属性是否**可审计**——本文诚实列出 bx 保证什么、不保证什么、以及残留风险,便于你据实评估是否信任它。

> One-line threat model: bx keeps a device's real IP/traffic from leaking to the open internet when a device should be using the encrypted tunnel — enforced at the network layer, fail-closed. It is **not** anonymity against a global adversary, and it does not hide your traffic from your own VPS.

## 1. 安全属性(不变量)

这些是代码里刻意维持的不变量,改动时必须保住(见 `CLAUDE.md`「防环/安全不变量」),也有回归测试守护:

- **网络层全接管**:整机 TCP/UDP 经 TUN(gVisor netstack)终结,应用无法绕过代理设置直连——这是相比"浏览器挂 SOCKS"能防住 **WebRTC/STUN 泄漏**的根本原因(UDP 也走隧道)。
- **kill-switch / fail-closed**:隧道不健康时,本应走代理的连接**直接 Block,绝不降级为直连**。宁可断网,不漏真实 IP。
- **私网恒直连**:`10/8`、`172.16/12`、`192.168/16`、CGNAT、link-local、loopback 永远直连,不受 global 影响(不把内网流量误送进隧道)。
- **IPv6 fail-closed**:默认对全局 IPv6 装黑洞路由(不走隧道就不出),避免"v4 走隧道、v6 裸奔"这一常见泄漏。
- **服务器防环 / bx 自身出站防环**:到代理服务器的连接与 bx 自身出站都绕过 TUN,不会自环。
- **改动类命令 peer-cred 门控**:控制面的 mutation(换传输、重劫持等)需 root 或配置的业主 uid。

## 2. 信任边界(你必须信任谁)

- **你的 VPS / 代理服务器**:隧道在服务器出口**解密**,服务器运营商能看到你的境外明文流量与出口 IP。bx 保护的是"真实 IP 不泄漏给公网目标",**不是**"对你自己的 VPS 保密"。请用你自己控制的服务器。
- **内嵌的 brook / sing-box**:bx 内嵌这两个上游二进制(均 GPL-3.0)。它们的安全性即 bx 隧道的安全性。二进制随仓库提交、由 CI 从上游同 tag 源码自建(见 `CLAUDE.md`「内嵌资产」),可对照上游复核。
- **DNS(仅直连路径)**:境外域名走 fake-IP、不做真实解析;但**被判为直连的域名**(白名单或国内)仍走真实 DNS 解析——若该 DNS 应答被投毒,可能把直连引到攻击者的 IP(见下)。

## 3. 残留风险与缓解(诚实的局限)

- **白名单/分流本质上会用真实 IP 访问"被判为直连"的目的地**。这是分流的定义,不是 bug:
  - 你直连的域名(白名单)/国内目的地**看得到你的真实 IP**——对它们你本就是本地用户。
  - **公有云存储/CDN 去匿名化**:若把 `aliyuncs.com`/`myqcloud.com`/`*.amazonaws.com`/`github.io` 等**任何人可注册子域**的域名加进白名单,攻击者能用一个子域让你的真实 IP 暴露。bx 的 `bx direct add` 对这类域名**会警告并默认拒绝**(`--force` 才加),但请只白名单**品牌自控**的顶级域。
  - **白名单域名的 DNS 被投毒**:on-path/GFW 级攻击者若能篡改你白名单域名(如 `www.baidu.com`)的直连 DNS 应答,可把你的真实 IP 引到冒牌服务器。缓解:直连解析尽量用可信/加密 DNS;要"对任何人都不暴露"就用 **global 全量走隧道**。
  - **裸 IP 直连**:分流模式下,应用直接连一个中国 IP 字面量(无域名)会命中 geoip 直连。global 模式关掉此路径。
- **不是匿名工具**:bx 不提供对抗全局观测者的匿名性(不是 Tor)。它防的是"真实 IP 泄漏给公网目标 / 明文旁路",不是流量分析、时序关联或你 VPS 被取证。
- **传输的抗封锁性取决于协议与配置**:REALITY 借壳 SNI 抗 DPI;弱协议(明文 brook 等)在强审查网络下更易被识别/干扰。协议选择见 `docs/multi-transport-guide.md`。

## 4. 数据与隐私

- **无遥测、无回连**:bx 不向作者/任何第三方上报数据。唯一的对外连接是你配置的隧道服务器,以及你显式触发的 `bx update`(拉 GitHub release)、china 列表刷新(经隧道)。
- **凭据处理**:配置/分享里的密钥以 `0600` 存放;日志与 `bx status`/`doctor` 输出对凭据做脱敏;裸凭据链接会提示先用 `bx blink` 换壳再分享。
- **本地日志**:仅本机诊断用,不外传。

## 5. 报告漏洞

请**私密**披露,不要开公开 issue:

- 用 GitHub 的 **Private vulnerability reporting**(仓库 → **Security** → **Report a vulnerability**)。

请附:复现步骤、影响面(尤其"真实 IP/流量泄漏"类)、以及你测试的版本(`bx --version`)。我们会尽快确认并修复;涉及泄漏的问题优先处理。
