# 多传输使用指南(容灾 + 按类分流)

bx 支持**三种传输引擎平级共存**,可单用、可组成容灾池、可按流量类别分流。核心原则贯穿始终:
**不泄漏是本质**——任何传输/容灾/分流,隧道不健康一律 fail-closed Block,绝不回落直连暴露真实 IP。

## 五种传输,各有所长

| 传输 | scheme | 特点 | 适合 |
| --- | --- | --- | --- |
| **brook** | `brook://` | 内嵌、最老牌、明文 9999 易被运营商 SYN 黑洞(wss/443 可伪装) | 默认/兜底 |
| **REALITY** | `vless://…security=reality` | 伪装成访问真站(如 apple.com)的 TLS,抗 DPI,TCP | 主力抗封锁 |
| **hysteria2** | `hysteria2://` / `hy2://` | 基于 QUIC/UDP,丢包高 RTT 链路(蜂窝)更快 | UDP/速度档 |
| **trojan** | `trojan://` | 标准 TLS-in-TLS,生态广、服务端常见 | 复用现有节点 |
| **shadowsocks** | `ss://` | 老牌轻量 AEAD,几乎所有面板都给 ss:// | 复用现有节点 |

> 直接甩别处的 `vless://` / `hysteria2://` / `trojan://` / `ss://` 分享链接,bx 都能直接吃。
> 但裸链接含明文凭据(会留进 shell 历史/分享面),**建议先 `bx blink <link>` 换壳成 `bx://` 再用**。

服务端搭建见 [reality-server-setup.md](reality-server-setup.md)(reality);hysteria2 服务端用 sing-box
hysteria2 入站(需 TLS 证书或自签 + `insecure`)。

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
