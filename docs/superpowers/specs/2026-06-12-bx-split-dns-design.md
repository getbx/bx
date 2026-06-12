# bx split-DNS(内网域名 → 内网 DNS 解析 + 强制直连)设计

日期:2026-06-12
状态:已批准(对话中敲定),待实现计划

## 1. 背景与问题

bx 是 fake-IP 透明代理:**所有** A 查询都被 `dns.Server` 换成保留段假 IP(`198.18/15`),连接回到 TUN 时 dialer 反查域名做分流。这对「公网域名 + 中国/海外分流」很好,但对**企业内网 split-horizon 域名**(如 `*.shanghai-electric.com`,只有内网 DNS `10.0.13.23` 能正确解析、且服务器要直连够到)是错的:

- 内网 DNS 从未被咨询 —— 域名被假 IP 化,内网真实地址永远拿不到。
- 在 `global: true` 模式下,这些域名默认判 **Proxy**,被塞进加密隧道 —— 而隧道根本到不了企业内网。

更糟:用户 config 里已写的 `dns.split` 块**被静默忽略**——`config.DNS` 结构体根本没有 `split` 字段,`yaml.Unmarshal` 默认丢弃未知键、不报错。配了等于没配。

```yaml
# 用户 config 里已有、但当前完全失效:
dns:
  split:
    - domains: ["*.shanghai-electric.com"]
      server: 10.0.13.23
```

## 2. 目标

让匹配 split 规则的域名:**① 经指定的内网 DNS 解析(拿真实 IP)② 强制直连出物理网卡、绝不进代理隧道**。其余域名行为不变(fake-IP)。

并修掉根因:config 解析改严格模式,以后「配了但无效」的字段**直接报错**。

非目标(YAGNI):split 转发 IPv6/AAAA(与现有 v6-block 决策一致,只走 v4);UDP 截断的 TCP 回退;splitDirect 集的淘汰/TTL;per-rule 的代理(split 一律直连,要代理用普通规则)。

## 3. 架构与数据流

split 判定**全部收敛在 DNS 层**;「强制直连」通过一个 DNS 填、dialer 查的旁路集实现,**不污染纯逻辑 Router**(复刻 `fakeip.Pool` 的模式:共享、跨 Router 热重载存活)。

```
客户端 DNS 查询 *.shanghai-electric.com
        │ (port 53 → tun → udp forwarder)
        ▼
   dns.Server.Respond
        │ 域名匹配 split 规则?
   ┌────┴─────┐
   是          否 ──→ 现有 fake-IP 路径(198.18/15)
   ▼
 转发到 10.0.13.23:53(经 DirectDialer,防环出 eno1)
        │ 真实应答(如 A → 10.0.13.45)
        ├──→ splitDirect.Add(10.0.13.45)   // 注册为强制直连
        └──→ 把真实应答原样回客户端
        ▼
 客户端连真实 IP 10.0.13.45 → tun → dialer
        │ m.IP=10.0.13.45, m.Domain=""(非 fake)
        ▼
 dialer:splitDirect.Contains(m.IP)? → 命中 → 强制 Direct
        ▼
 DirectDialer(SO_MARK)→ 出 eno1 → 够到内网服务器 ✓
```

## 4. 组件

### 4.1 config schema(`internal/config/config.go`)

```go
type DNS struct {
    China      string      `yaml:"china"`
    FakeipCIDR string      `yaml:"fakeip_cidr"`
    Split      []SplitRule `yaml:"split"` // 新增
}

type SplitRule struct {
    Domains []string `yaml:"domains"` // 支持 *.suffix 通配
    Server  string   `yaml:"server"`  // 内网 DNS,如 "10.0.13.23"(:53 默认)
}
```

校验:每条 `SplitRule` 的 `Domains` 非空、`Server` 非空(可只给 IP,补默认 `:53`)。

**config 严格模式(修根因)**:`Parse` 从 `yaml.Unmarshal` 改为
```go
dec := yaml.NewDecoder(bytes.NewReader(b))
dec.KnownFields(true)
if err := dec.Decode(&c); err != nil { ... }
```
未知字段从此**直接报错**,杜绝「配了但静默失效」。

### 4.2 `splitdns.Set`(新包 `internal/splitdns`)

DNS 填、dialer 查的并发安全直连 IP 旁路集:

```go
type Set struct { mu sync.RWMutex; m map[netip.Addr]struct{} }
func NewSet() *Set
func (s *Set) Add(ip netip.Addr)
func (s *Set) Contains(ip netip.Addr) bool
```

不淘汰(内网 IP 少而稳);跨 Router 热重载存活(同 `fakeip.Pool`,由 supervisor 持有、注入两端)。

### 4.3 split 路由 + forwarder(`internal/dns` 或 `internal/splitdns`)

```go
// 一条编译好的 split 路由:域名匹配器 + 目标 DNS。
type SplitRoute struct {
    Match  *route.DomainSet // 复用现有域名匹配(*.suffix)
    Server string           // host:53
}

// Forwarder 把查询转发到内网 DNS 并返回应答字节。
type Forwarder interface {
    Forward(ctx context.Context, server string, query []byte) (resp []byte, err error)
}
```

forwarder 实现:经 `DirectDialer` 拨 `server`(UDP),发查询、读应答、超时(如 5s)。供 `dns.Server` 使用。

### 4.4 `dns.Server`(`internal/dns/server.go`)

新增字段:`splits []SplitRoute`、`fwd Forwarder`、`direct *splitdns.Set`。`Respond` 改为:

1. 解析 question 域名。
2. 遍历 `splits`,命中某条 →
   - `q.Type==AAAA` → **NODATA**(逼 v4,同现有 v6-block 策略;不转发)。
   - 其余类型(A 及 CNAME/SRV 等)→ `fwd.Forward(server, query)`:
     - 成功 → 解析应答、把其中**所有 A 记录** `direct.Add(ip)`(非 A 记录无 IP 可注册,仅透传);**原样返回**应答字节(保留上游 TTL/CNAME 链/其它记录,内网解析完整)。
     - 失败/超时 → 返回 **SERVFAIL**(不回退 fake-IP)。
3. 未命中任何 split → 现有 fake-IP 路径(一行不动)。

### 4.5 `dialer.Dialer`(`internal/dialer/dialer.go`)

新增字段 `SplitDirect *splitdns.Set`(可空)。`Dial` 在 fakeip 反查之后、`Decide` 之前插入:

```go
if m.Domain == "" && d.SplitDirect != nil && d.SplitDirect.Contains(m.IP) {
    dec = route.Direct // split 解析出的内网真实 IP:强制直连,跳过 Router
} else {
    dec = rt.Decide(m)
    // ... 现有 NeedResolve 处理 ...
}
```

Direct 分支已有逻辑:`m.Domain==""` 时直接用 `m.IP` 经 `d.Direct`(DirectDialer)拨号 → 出 eno1。无需改 Direct 分支本身。

### 4.6 supervisor 接线(`internal/supervisor/run.go`)

- 从 `cfg.DNS.Split` 编译 `[]SplitRoute`(每条 Domains 建一个 `route.DomainSet`)。
- 建共享 `splitDirect := splitdns.NewSet()`。
- 建 forwarder(用平台 `DirectDialer()`)。
- 注入:`dns.Server{splits, fwd, direct: splitDirect}`、`dialer.Dialer{SplitDirect: splitDirect}`。
- split 路由是启动期静态(不随 china 列表热重载;Router 热重载不碰 splitDirect)。

## 5. 不变量与边界

- **防环**:forwarder 用 `DirectDialer`;且内网 DNS 多为 10.x → pref 150 进主表,双保险不绕回 tun。
- **killswitch 豁免**:split → Direct,killswitch 只拦 Proxy → 隧道挂了内网域名照样通(符合预期)。
- **v6 一致**:split 只处理 A;AAAA→NODATA。不与 v6-block 冲突。
- **失败诚实**:内网 DNS 不可达 → SERVFAIL,不静默降级到代理。
- **kill-switch 不漏**:split 不改变「公网域名仍 fake-IP + 隧道」的主路径,不引入泄漏面。

## 6. 测试(TDD,基本全免 root 纯逻辑)

- `config`:① split 块正确解析为 `[]SplitRule`;② **严格模式:未知字段报错**(本次踩坑的回归用例);③ Server 补默认 `:53`。
- `splitdns.Set`:Add 后 Contains 命中、未加不命中。
- `dns.Server.Respond`(注入假 Forwarder 返回固定 A=10.0.13.45):
  - 匹配域名 → 调用 forwarder、`direct` 集含 10.0.13.45、返回的应答含该 A;
  - 非匹配域名 → 仍 fake-IP(不调 forwarder);
  - 匹配域名 AAAA → NODATA;
  - forwarder 报错 → 应答 RCode=SERVFAIL。
- `dialer.Dial`(注入假 Direct/Proxy 拨号器 + GlobalProxy Router):
  - `m.IP ∈ SplitDirect` → 走 Direct(即便 global);
  - `m.IP ∉ SplitDirect` 的公网 IP → 仍 Proxy(零回归)。

## 7. 实施顺序(建议)

1. config schema + 严格模式(+ 测试)。
2. `splitdns.Set`(+ 测试)。
3. forwarder + `dns.Server` split 分支(+ 测试,注入假 forwarder)。
4. `dialer` SplitDirect 钩子(+ 测试)。
5. supervisor 接线(集成,免 root 难覆盖的部分靠手动/真机)。
