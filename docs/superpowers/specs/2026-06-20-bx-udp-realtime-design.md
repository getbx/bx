# bx UDP / realtime 设计

日期:2026-06-20
状态:已批准(对话中敲定),待实现计划

## 1. 背景与问题

bx 当前已经能在 TUN 层捕获 UDP,但除 DNS(`UDP:53`)外,`dialer` 会把所有 UDP 快速阻断。这个默认策略很安全,但对 Google Meet、Zoom、FaceTime、Discord、游戏、QUIC 等实时应用不友好。

用户观察到 Google Meet 中「听对面没问题,但自己说话对面听到断续」。这符合 WebRTC 的退化路径:UDP 被阻断后,浏览器回落到 TCP/TLS TURN 或其它备选路径。网页和下载还能工作,但实时上行音频对抖动和延迟更敏感。

对比当前 `compass-router` 的设计,它使用 `mihomo` TUN + fake-IP + UDP-capable proxy,节点声明 `udp: true`,因此能让实时 UDP 进入代理系统。bx 的差距不是 TUN 收不到 UDP,而是出口层还没有 UDP session/relay 语义。

## 2. 目标

- 默认继续 fail-closed:未明确开启前,非 DNS UDP 不泄漏。
- 给用户一个简单命令改善会议/实时应用体验。
- 让 `doctor/status/capabilities` 能告诉用户 UDP 当前处于什么策略,为什么会议可能受影响。
- 为后续真正 UDP over bx 预留接口,避免短期方案变成永久架构债。

非目标:

- 初版不把所有 UDP 默认放开。
- 初版不承诺 brook socks5 路径支持 UDP。当前 `x/net/proxy.SOCKS5` 主要是 TCP Dialer,不能直接删掉 UDP block。
- 初版不做复杂 UI 或每应用识别。命令保持克制。

## 3. 用户语义

新增一个显式 realtime 模式:

```bash
bx realtime status
sudo bx realtime on
sudo bx realtime off
```

语义:

- `off`:默认模式。非 DNS UDP 阻断。最安全,不泄漏。
- `on`:实时模式。非 DNS UDP 通过 bx 隧道中继,避免走本地真实网络路径。
- `status`:显示当前 UDP 策略、是否可能泄漏、最近 UDP 阻断/放行统计、建议命令。

配置保持简单:

```yaml
udp:
  mode: block # block | direct-realtime | proxy
```

`bx realtime on` 写入 `udp.mode=proxy`,重启服务后生效。`direct-realtime` 仅保留为兼容/应急配置,不作为推荐用户路径。

## 4. 两阶段架构

### 4.1 阶段一:direct-realtime

目的:尽快解决会议体验,但不把它伪装成完全代理。

数据流:

```text
TUN UDP
  -> dns UDP:53: bx DNS 处理
  -> realtime allowlist 命中: DirectDialer 直连
  -> 其它 UDP: ErrBlocked
```

allowlist 初版以端口/域名来源组合为主:

- fake-IP 反查到 Google/会议相关域名时允许 UDP。
- 典型 WebRTC/STUN/TURN UDP 端口可允许,但需要保守,避免泛放公网 UDP。
- 命中直连时在 stats 中记录为 `udp_direct_realtime`,并在 status 中显示「可能暴露真实网络路径」。

安全边界:

- 必须用户显式开启。
- `killswitch` 仍然保护代理路径;direct-realtime 是用户选择的例外。
- `doctor` 要说明 realtime 模式的泄漏含义。

### 4.2 阶段二:proxy

目的:产品级 UDP 支持,默认可安全接管会议/游戏/QUIC,不泄漏真实路径。

数据流:

```text
TUN UDP
  -> bx client SOCKS5 UDP ASSOCIATE
  -> brook/bx tunnel transport
  -> bx server 内置 brook UDP relay
  -> Internet UDP endpoint
```

关键组件:

- `internal/socks5.Dialer`:TCP 继续使用 SOCKS5 CONNECT;UDP 使用 SOCKS5 UDP ASSOCIATE。
- `dialer.UDPMode=proxy`:非 DNS UDP 进入 Proxy dialer,受 tunnel health/killswitch 保护。
- 服务端仍由 bx 启动内置 brook server,用户不需要知道 brook。
- 后续若替换协议,保持 dialer/config/status 语义不变,只替换 tunnel transport。

出口选择:

- `mode=proxy`:所有应代理的 UDP 走 bx server UDP relay。
- `mode=block`:保持现状。
- `mode=direct-realtime`:仅作为兼容/应急策略。

## 5. 组件设计

### 5.1 config

新增:

```go
type UDP struct {
    Mode string `yaml:"mode"` // block, direct-realtime, proxy
}
```

默认 `block`。未知值启动时报错。

### 5.2 CLI

新增 `realtime` 子命令:

- `bx realtime status`:只读,非 root 可用。
- `sudo bx realtime on`:切换策略并尽量热生效;不支持热生效时提示重启服务。
- `sudo bx realtime off`:回到 `block`。

`capabilities` 增加 `realtime` 和 `udp` 能力,让 LLM 能自然发现:

- 是否安全;
- 是否需要 root;
- 是否会改变网络;
- examples。

### 5.3 dialer

当前 UDP block 在 `DialWithInitial` 开头。改成策略分发:

```go
if m.UDP {
    return d.DialUDP(ctx, m)
}
```

行为:

- `block`:统计 blocked,返回 `ErrBlocked`。
- `direct-realtime`:命中 realtime allowlist 走 `DirectDialer`,否则 block。
- `proxy`:走 SOCKS5 UDP ASSOCIATE 进入 bx 隧道;若 tunnel 不健康且 killswitch 开启则 block。

### 5.4 tun engine

当前 `handleUDP` 已能把 UDP 五元组交给 `handleConn`。阶段一可复用现有 `net.Conn` relay。

当前 UDP 通过 `net.Conn` 形式接入,底层 SOCKS5 UDP dialer 保留 datagram 边界。后续如替换为 bx 自有 relay,再在 tunnel/dialer 边界引入显式 packet relay。

### 5.5 status / doctor

`bx status` 增加 UDP 行:

```text
UDP     blocked 14422  realtime 0  proxy 0  mode block
```

`bx doctor --json` 当 UDP 为 block 时给出 hint:

```json
{
  "name": "udp_policy",
  "status": "warn",
  "detail": "udp blocked",
  "hint": "Google Meet/WebRTC may stutter; use sudo bx realtime on or sudo bx down on trusted routed networks"
}
```

## 6. 推荐实现顺序

1. 先补诊断:status/doctor/capabilities 明确 UDP blocked,不改变行为。
2. 加 config `udp.mode=block` 和测试。
3. 加 `bx realtime status`。
4. 加 `direct-realtime` 作为应急策略。
5. 实现 SOCKS5 UDP ASSOCIATE,把 `realtime on` 的默认策略升级为 `proxy`。
6. 实机验证 Google Meet/WebRTC STUN/TURN 行为。

## 7. 测试策略

纯逻辑测试:

- config 默认 `udp.mode=block`;
- 非法 mode 报错;
- `block` 模式 UDP 仍阻断;
- `direct-realtime` 走 DirectDialer 并明确标注泄漏风险;
- `proxy` 模式 UDP 走 Proxy dialer;
- SOCKS5 UDP ASSOCIATE 保留目标地址和 datagram payload;
- `doctor --json` 在 block 模式给出 UDP/WebRTC hint;
- `doctor --json` 在 proxy 模式报告 UDP relay ok;
- `capabilities` 暴露 realtime 命令和 relay 说明。

集成/实机测试:

- macOS `bx up` 后 `bx status` 显示 UDP mode;
- Google Meet 或 WebRTC test 页面在 `block` 与 `realtime on` 下对比;
- `bx down` 后 DNS/路由仍恢复;
- 在 compass Wi-Fi 这种已有上游分流网络中,对比 `bx realtime on` 与 `bx down` 的会议上行质量。

## 8. 产品原则

- 默认安全,不静默泄漏 UDP。
- 命令简单,不要求用户理解 WebRTC/STUN/TURN。
- 诊断诚实,让人和 LLM 都能自然发现问题。
- 短期解决体验,长期走完整 UDP relay。
