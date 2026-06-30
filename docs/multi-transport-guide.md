# 多传输使用指南(容灾 + 按类分流)

bx 支持**六种传输引擎平级共存**,可单用、可组成容灾池、可按流量类别分流。核心原则贯穿始终:
**不泄漏是本质**——任何传输/容灾/分流,隧道不健康一律 fail-closed Block,绝不回落直连暴露真实 IP。

## 六种传输不是一个层次——按当今封锁/检测态势分三档

> ⚠ **重要**:协议越多 ≠ 越好。当今 GFW(2025-2026)主动探测 + AI DPI 升级后,各协议存活率差距巨大。
> 下表按**实际有效性**分档(数据:GFW 2025-2026 实测,见文末来源),帮你选对、而不是随便用。

### 🟢 主力档(强对抗下仍有效——优先用这两个)

| 传输 | scheme | 为什么强 | 定位 |
| --- | --- | --- | --- |
| **REALITY** | `vless://…security=reality` | 偷真实站点(如 apple.com)的 TLS 握手,主动探测去连=被转发到真站、回正常响应;**早 2026 实测 98-99% 突破率**,目前最隐蔽 | **强封锁首选(TCP 隐蔽)** |
| **hysteria2** | `hysteria2://` `hy2://` | QUIC/UDP,丢包高 RTT 链路(蜂窝/跨境)更快;**裸 QUIC 会被 SNI 识别/限速,务必配 salamander 混淆** | **速度档(UDP)** |

### 🟡 兼容档(接住你已有的节点;**强 DPI 下 2025 起基本被秒,慎用**)

| 传输 | scheme | 当今状态 |
| --- | --- | --- |
| **trojan** | `trojan://` | 2025-08 GFW 升级后 ~90% 检出(主动探测 + TLS-in-TLS 指纹) |
| **vmess** | `vmess://` | 2025-09 起 ~80% 检出 |
| **shadowsocks** | `ss://` | ~95% 检出(熵指纹 + ML),且**易被 ban server IP** |
| **brook** | `brook://` | 内嵌默认、最简;明文易被封,wss/443 可伪装 |

> 直接甩别处的 `vless://` / `hysteria2://` / `trojan://` / `ss://` / `vmess://` 分享链接,bx 都能直接吃
> (裸链接含明文凭据,建议先 `bx blink <link>` 换壳)。但 `bx setup` 贴**兼容档**链接时会提示:
> 这类协议对当今强 DPI/服务端风控(含 Claude/OpenAI/Google)较弱,**建议 server 端改用 REALITY**。

### 推荐组合:REALITY(TCP)+ hysteria2(UDP)+ brook(兜底)

这正是 bx 的**按类分流 + 容灾**要落地的形态,也是 2026 主流推荐解——既安全又有速度:

- **TCP/隐蔽** 走 REALITY(主传输),**UDP/QUIC/加速** 走 hysteria2(`udp.transport`)。
- **brook 作 fallback**:它内嵌、零额外 server 依赖,主传输健康抖动/进程挂时兜一手。
  注意 brook 抗封锁弱——若是**审查升级**导致 REALITY 被封,brook 多半也救不了;真正的抗封锁冗余应是
  **第二个 REALITY**(不同 server IP / SNI / 端口)。所以最稳的 `transports:` 顺序是
  `reality(主) › reality2(备) › brook(兜底)`。

## REALITY / hysteria2 最佳配置(bx 客户端默认已对齐 2026 实践)

**REALITY**:bx 解析 `vless://…reality` 时默认 `flow=xtls-rprx-vision`(消除长度指纹、降内层 TLS 开销)、
`fp=chrome`(uTLS 模拟 Chrome 指纹)、TCP 传输——**这三项正是 2026 推荐生产配置**,无需你手动加。
server 端要点:SNI 借一个支持 TLS1.3+H2、你又控不了、**且证书链够小**的高流量真站(如 `www.cloudflare.com`/`www.apple.com`;**别用 `www.microsoft.com`——证书过大,reality 借壳握手会失败,真机 e2e 坐实**),
端口落 443 或服务端已放行的高端口。

**hysteria2**:bx 默认不设 `up/down` 带宽 → sing-box 用 **BBR**(自适应,安全默认);
Hysteria 的 Brutal 定速算法虽猛,但**必须准确填链路真实带宽**,填错反而更差,故不默认开。
**强烈建议加 salamander 混淆**对抗 QUIC SNI 检测:链接加 `?obfs=salamander&obfs-password=<pw>`
(server 端 hysteria2 入站也配同 obfs)。UDP 被运营商限速时,优先级:中国联通(AS4837)>电信。

服务端搭建见 [reality-server-setup.md](reality-server-setup.md)(reality);hysteria2 服务端用 sing-box
hysteria2 入站(需 TLS 证书或自签 + `insecure`;建议配 salamander obfs)。

**来源(GFW 2025-2026 实测)**:[gfw.report SS 检测](https://gfw.report/blog/modified_shadowsocks/en/) ·
[USENIX'25 QUIC SNI 审查](https://gfw.report/publications/usenixsecurity25/en/) ·
[2026 协议对比](https://lilting.ch/en/articles/china-vpn-protocol-comparison) ·
[Hysteria2 抗 QUIC 限速](https://greatfirewallguide.com/lab/hysteria2)。

## 1. 单传输(最简)

一条链接即可。裸链接直接能用,但建议先换壳(裸链接含明文凭据、会留进 shell 历史):

```bash
# 直接用(会提示建议 blink)
sudo bx setup 'vless://<uuid>@<vps>:9998?security=reality&pbk=...&sid=...&sni=www.apple.com'
# 或先换壳成 bx://(分享/留存更安全)
bx blink 'vless://…'          # → bx://…
sudo bx setup 'bx://…'
sudo bx up
bx status                      # 看「传输」行显示当前走哪个
```

## 2. 多传输自动容灾(reality 主 / brook 备)

把多条链接配成**有序优先级池**,主传输持续不健康时自动切备选,全程 fail-closed:

**法一:一条 bundle link(推荐,一贴配好)**
```bash
bx blink 'vless://…reality…' 'brook://…'   # 多个 link → 一条容灾 bx://(主在前)
# → bx://<bundle>
sudo bx setup 'bx://<bundle>'              # 自动写成 transports: 列表
```

**法二:直接写配置**
```yaml
# /etc/bx/config.yaml
transports:
  - vless://<uuid>@<vps>:9998?security=reality&pbk=...&sid=...&sni=www.apple.com   # 主
  - brook://server?server=<vps>%3A9999&password=<pw>                               # 备
killswitch: true
```

行为:主健康就走主;主持续不健康(过滞回窗口)→ 自动 `swapTo` 备选;**全部不健康 → 保持当前 + Block**
(判定为网络问题,不在传输间反复横跳)。`bx status` 的「传输」行实时显示当前活跃 + 容灾列表。

> 防抖:切换有滞回(连续不健康才切)+ 冷静期(刚切过不再切),避免瞬时抖动引发横跳。

## 3. 按类分流:UDP 走 hysteria 加速,TCP 走 reality

不同协议有不同强项——让 **UDP/QUIC 走 hysteria(QUIC 原生、丢包链路快)**,其余 TCP 走主传输(reality 隐蔽):

```yaml
# /etc/bx/config.yaml
server: vless://<uuid>@<vps>:9998?security=reality&...   # 主传输(TCP 走它)
udp:
  mode: proxy                                            # 必须 proxy(否则 udp.transport 报错)
  transport: hysteria2://<pw>@<vps>:8443?sni=<域名>      # UDP/QUIC 走它
killswitch: true
```

两条隧道并行运行,各自独立 fail-closed:**UDP 传输挂 → UDP Block(不回落主传输/直连)**。`bx status`
显示 `UDP→hysteria2@<vps>`。这样**既安全(都不泄漏)又有速度(UDP 走最适合的引擎)**。

> UDP 默认 `mode: block`(QUIC 自动回落 TCP,安全)。想要 UDP 走隧道才设 `proxy`;想要 UDP 提速再加
> `udp.transport`。三档按需。

## 安全保证(不变量)

- **任何路径不泄漏**:容灾切换窗口、按类分流、所有传输——隧道不健康一律 Block,绝不直连回落。
- **私网/中国直连不受影响**:私网恒直连;非 global 模式中国 IP/域名直连(china 列表)。
- **防环**:每个传输(主+备+UDP)的服务器都自动进路由旁路 + 静态 DNS,隧道自身连服务器不成环。

## 排查

```bash
bx status     # 「传输」行:当前活跃 + 容灾列表 + UDP 专用
bx doctor     # server_link / transports / udp_transport / 连通探测
```
踩坑见 [reality-server-setup.md](reality-server-setup.md):服务端别用 443、SNI 挑稳定 TLS1.3 站、OpenWrt 测连通用 `curl` 不用 `/dev/tcp`。
