# bx IPv6 阻断(fail-closed unreachable)设计

日期:2026-06-11
状态:已批准(对话中敲定),待实现计划

## 1. 背景与问题

bx 的核心安全不变量是 **fail-closed**:宁可断,不可漏真实 IP(隧道挂 → Proxy 决策 Block)。
但当前实现只处理 IPv4,IPv6 是一个**未被堵住的泄漏口**。把 v6 切成三层看:

| 层 | 现状 | v6 安全性 |
|---|---|---|
| **DNS**(`internal/dns/server.go:52`) | AAAA 查询 → NODATA,逼客户端回落 v4 fake-IP | ✅ 已防,但**只对走 bx 解析器的应用有效** |
| **netstack**(`internal/tun/engine.go:70`) | gVisor 已注册 `ipv6.NewProtocol`,理论能终结 v6 | ⚪ 空转 —— 包根本进不来 |
| **路由劫持**(`platform_linux.go` / `platform_darwin.go` 的 `Hijack`) | 纯 v4:Linux `ip rule` 无 `-6`、darwin 只劫 `0/1`+`128/1`;TUN 不配 v6 地址 | ❌ **全漏** —— 任何 v6 包绕过 TUN 直接走物理网卡,带真实源 IP |

**精确漏点**:DNS 那层只挡住「经 bx 解析的域名」。两类仍泄漏真实 IP:

1. **v6 字面量目标**:应用硬编码 `[2606:...]`,不经 DNS。
2. **绕过 bx 解析器的应用**:自带 DoH/DoT 或直连外部 resolver 拿到 AAAA。Happy Eyeballs 会**优先用 v6**。

此外 `route.DefaultPrivateCIDRs` 是**纯 v4**,缺 `fe80::/10`、`fc00::/7`、`::1`、`ff00::/8` —— 无论选哪个方案,v6 私网 carve-out 都得补,否则会打断局域网 / mDNS / NDP。

## 2. 决策:阻断 v6,而非走隧道

**结论:bx up 时给宿主在 v6 内核启用时装 v6 unreachable 默认路由(fail-closed),v6 私网/链路本地保持直连。** 不走隧道。

理由:

- **同构 kill-switch 哲学**:v6 无法保证「不漏」,就该 Block。这是把已有的 fail-closed 不变量延伸到 v6,而非引入新语义。
- **与 DNS 层咬合**:AAAA-NODATA 的设计意图就是「逼回 v4」。unreachable 默认路由正是给这个意图补上**字面量 / 野 resolver 的兜底**,两道闸前后呼应。
- **走隧道(Option B)是一大摊且全是新泄漏面**:brook 隧道本就是 v4 socks5、china 分流是域名 + v4 CIDR。走 v6 要补 v6 GeoIP china 列表、v6 fake-IP 池、`cidrset` 的 v6 支持、dialer v6 拨号 —— 每一处都是新的潜在泄漏点。**YAGNI**:今天几乎不存在「只有 v6、无 v4 回落」的目标。

**显式代价**:纯 v6-only 目标不可达。绝大多数目标双栈,v4 回落即可;这是可接受且**有意为之**的取舍。

非目标(YAGNI):v6 走隧道(留待将来确有 v6-only 目标);v6 分流决策(china/proxy)—— 阻断不需要分流;config 开关(本轮 v6 阻断**无条件**,bx up 即生效)。

## 3. 方案(block 的 fail-closed 四件套)

要点:① v6 全局默认 **`unreachable`**,随 `Hijack` 原子装上;② v6 私网 / 链路本地保持直连(carve-out);③ bx 自身 v6 出站防环(fwmark 旁路);④ **v6 内核禁用则整组跳过**(否则 `ip -6` 失败连累 v4 启动);⑤ teardown 与 up 对称还原。

### 3.0 丢弃路由用 `unreachable`,不用 `blackhole`(关键修正)

`ip-route(8)` 原文:`blackhole` 让本地发送者得 **`EINVAL`**(静默丢);`unreachable` 得 **`EHOSTUNREACH`**。后者才是 Happy Eyeballs / `connect()` 识别的「此地址不通,试下一个」标准信号 —— 双栈应用据此**立即回落 v4**;`EINVAL` 是「参数非法」类错误,部分应用不当成可重试失败、可能直接报死错。两者都 fail-closed(包不出网卡),但 `unreachable` 回落更干净。额外:bx 当网关转发时,`unreachable` 回 ICMPv6 unreachable 让下游客户端也快速回落 v4(`blackhole` 静默丢会让其卡 v6 超时)。**故全局 v6 默认路由用 `unreachable`。**

### 3.1 v6 私网段(平台无关)

`internal/route/router.go` 新增:

```go
// DefaultPrivateV6CIDRs 是任何模式下都内建直连的 v6 非全局段,对应 v4 的 DefaultPrivateCIDRs:
// loopback、link-local、ULA(私网)、multicast(mDNS/NDP)。阻断 v6 时必须 carve-out,
// 否则打断局域网 / 邻居发现。
var DefaultPrivateV6CIDRs = []string{
    "::1/128",   // loopback
    "fe80::/10", // link-local
    "fc00::/7",  // ULA(对应 v4 私网)
    "ff00::/8",  // multicast(mDNS / NDP / RA)
}
```

### 3.2 Linux:镜像现有 v4 策略路由,加一组 `-6` 规则(本轮实现)

`platform_linux.go` 的 `netConf` 复用同一张表(`routeTable=100`),新增两个**显式**字段(不再用「CIDR 含 `:` 判家族」的隐式 sniff):

```go
type netConf struct {
    // ... 现有 v4 字段 ...
    blockV6      bool     // 宿主 v6 内核启用时为 true:装 v6 unreachable 阻断
    mainLookupV6 []string // v6 私网/链路本地段:-6 rule 送主表(pref 150)
}
```

`upSteps()` 仅当 `blockV6` 为真时,在现有 v4 步骤后追加 v6 步骤:

```
ip -6 rule add pref 100 fwmark 0x162 table main        # bx 自身 v6 出站防环(别漏:否则自我阻断)
ip -6 rule add to <v6私网> pref 150 table main          # v6 私网/链路本地 → 主表,native 投递
ip -6 route add unreachable default table 100           # 全局 v6 默认 → 不可达(回 EHOSTUNREACH)
ip -6 rule add pref 200 table 100                       # 全量 v6 进阻断表(pref 200,被私网 150 先命中)
```

`downSteps()` 对称(同样仅 `blockV6` 时):`-6 rule del` ×3 类 + `ip -6 route flush table 100`。

**门控(I/O 在 `Hijack`,不在纯 step builder)**:`Hijack` 先探测 v6 是否启用 —— **`/proc/net/if_inet6` 存在性**(不存在 ⇒ ipv6 模块未加载 / `ipv6.disable=1` ⇒ 无 v6 可漏)。启用才置 `blockV6=true` 并把 `route.DefaultPrivateV6CIDRs` 灌进 `mainLookupV6`;此时 v6 步骤失败仍 **fatal**(fail-closed)。禁用则 `blockV6=false`,`upSteps` 不产 v6 步骤,`ip -6` 一行不跑、不连累 v4 启动。

> **防环关键**:`pref 100 fwmark` 的 v6 规则不能漏 —— bx 自身若有 v6 出站(经 SO_MARK)需绕开阻断;否则 bx 把自己锁死。(今天 bx 出站全 v4、此规则是空保险,留作防环对称。)

### 3.3 已知局限:on-link GUA 邻居(v1 不做,follow-up)

`pref 200 → unreachable` 把所有全局 v6 送阻断表,会**连带把同链路、用 GUA(`2xxx` 全局地址)寻址的邻居也阻断**(SLAAC 下 LAN 设备常拿 GUA)。`fe80::/10`(NDP/本地发现)与 `fc00::/7`(ULA)有 carve-out 不受影响,仅「bx 开着时访问同网段某机的 GUA」这一窄情形受影响。

**v1 接受此局限并文档化**。Follow-up(确有需求再做):`Hijack` 动态读 `ip -6 route show scope link`(on-link 连接路由,含 GUA /64)逐条加进 pref 150 carve-out,自适应消除此 gap。

### 3.4 macOS:对称的 v6 unreachable(本轮**仅占位/标注**,不实现)

`platform_darwin.go` 同理:`darwinDirectCIDRs` 加 v6 私网段,split-default 改 v6 不可达。但 macOS `route -inet6` 的不可达语义无法在 Linux 上验证,与现有「macOS 待真机 sudo 验证」并表 —— 本轮**不动 darwin 代码**,只在 CLAUDE.md 跨平台待办里把「IPv6 决策」从开放问题更新为「已定 = 阻断;Linux 已实现;darwin 待真机」。

## 4. 验证(全部免 root,纯逻辑)

`upSteps()`/`downSteps()` 是纯构造命令序列(不执行 `ip`),故所有断言**不碰真实网络**:

- `TestUpStepsUnreachableV6Default` —— `blockV6=true` 时 upSteps 含 v6 `unreachable` 默认 + pref 200 全量进阻断表 + v6 私网 carve-out + v6 fwmark 旁路;v4 规则不受影响仍在。
- `TestUpStepsSkipsV6WhenDisabled` —— `blockV6=false` 时 upSteps **一条 `-6` 都不产**(v6 内核禁用的机器零回归)。
- `TestDownStepsRemovesV6` —— `blockV6=true` 时 down 对称清掉 v6 规则 + flush v6 表。
- `TestDefaultPrivateV6CIDRsWiredIn` —— `route.DefaultPrivateV6CIDRs` 非空(`Hijack` 启用 v6 时灌进 `mainLookupV6`)。

DNS 层(AAAA-NODATA)不动,作为第一道闸;`unreachable` 路由是第二道兜底。

## 5. 不变量回归检查清单

- [ ] kill-switch 一致:v6 全局流量 fail-closed(unreachable),绝不漏真实 IP。
- [ ] v6 私网/链路本地恒直连(carve-out),不受阻断影响。
- [ ] bx 自身 v6 出站防环(fwmark 旁路阻断表)。
- [ ] down 与 up 对称,无残留 v6 规则/路由。
- [ ] v4 行为零回归。
