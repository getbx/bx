# bx 多服务器 + 用户手动切换 设计

## 需求

用户要能配置多台服务器并在它们之间切换。约束是用户明说的两条:

> **只有用户可以切。平时连着的时候不会切换。**

也就是:**不要自动容灾**。换服务器 = 换出口 IP 与所在国家,自动切会让用户在毫不知情
的情况下换了身份——这与「隧道挂了要有备份」是两个不同的需求,后者今天由 kill-switch
(fail-closed 挡住,绝不降级直连)负责,已经够了。

## 先算清楚:哪些是现成的

这一节是本设计的主要价值。**绝大部分机器已经在那儿了**,缺的只有四件事。

### 已有,不要重造

| 机制 | 位置 | 说明 |
|---|---|---|
| **commit-confirmed 换传输** | `liveMutator`(`mutator.go`)+ `mutationEngine.Arm` | **生产里挂的就是它**(`run.go:492`),不是 `nopMutator` |
| Core 控制面端点 | `control.go:107-111` | `/v0/transport`(换主传输)、`/v0/rehijack`、`/v0/reconnect`、`/v0/commit`、`/v0/rollback` 全部已在,且有 `requireOwnerOrRoot` |
| **原子换隧道** | `transportSwapper.swapTo`(`failover.go`) | 建新 → 等健康 → SetTransport → 停旧。**失败时旧传输原样保留** |
| **全部服务器的 bypass** | `run.go:282-303` | 每个传输(主 + 备选 + UDP)的 server host 都已进 `serverBypass` 与静态 DNS |
| UDP 槽热切 | `dialer.SetUDPTransport` + `udpSwapper`(`run.go:474`) | 已有,但**没有控制面端点** |
| 配置外科手术 | `setup.UpdateTransports` | yaml.Node 就地改,保住用户手写的 rules 与注释 |
| Guardian 业主授权 | `authorizeOwnerPeer`(`localapi.go`) | 已守着 `/v1/up`、`/v1/down`、`/v1/recoveries` |

**结论**:「安全地换到另一条传输」这条路整条是通的、有 commit-confirmed 兜底、被
`runFailover` 用了很久。本设计**换的是谁来拉那根杆**(策略 → 用户),不是造新杆。

### 真正缺的四件事

1. **配置模型**:没有「服务器清单」这个概念,只有 `server:` 一条 + `transports:` 容灾表。
2. **UDP 槽没有控制面端点**:`/v0/transport` 只换主传输。而**一台服务器是一对链接**
   (reality 走 TCP + hysteria2 走 UDP,用户贴的两条链接指向同一台 `195.133.192.92`),
   切服务器必须两个槽一起换。
3. **`liveMutator.serverBypass` 是启动时捕获的定值**:加**新**服务器时它不含新 IP,
   直接切过去 = 隧道自己的流量被劫进 TUN → **成环**。
4. **入口**:CLI 没有 `bx server *`;菜单没有服务器列表;Guardian 没有对应端点。

## 配置模型

```yaml
servers:
  - name: hk                    # 不给 --name 时自动取主链接的主机名
    link: bx://…                # 主传输(TCP,如 reality)
    udp:  bx://…                # 可选;省略则 UDP 走主传输
  - name: tokyo
    link: bx://…
    udp:  bx://…
current: hk
```

**`current` 是意图**,与 `desired: on/off` 同类——用户显式声明的东西,住在 config 里。
**刻意不新增运行期状态文件**:本项目已有 6 份持久化状态,而
`2026-08-08-control-plane-architecture-design.md` 的诊断正是「状态只有一个主人」。

### 与旧配置共存

- **没有 `servers:` 时,`server:` + `udp.transport:` 照旧生效**,视为唯一一台,名字取主机名。
  旧配置一个字不用改。
- 第一次加第二台时,`bx setup` 把旧的那台**就地写成 `servers[0]`** 并删掉 `server:` /
  `udp.transport:`(yaml.Node 外科手术,不整份重写)。
- **`servers:` 与 `transports:` 互斥**:同时出现即**加载期报错拒绝启动**,错误里说明
  `transports:` 是自动容灾表、已被 `servers:` 取代。理由:两者都是「用哪条传输」的主人,
  并存就是这个项目刚花一整轮诊断出来的病。`transports:` 的代码**不删**——`swapTo`
  与 `runFailover` 的 policy 仍在,只是不再由本设计的路径喂给它。

### 命名规则

- `--name` 给了就用它;没给则取主链接的 authority host(`serverHostFromLink`,`ss://`/
  `vmess://` 走 `tunnel.SSHost`/`VmessHost`)。
- 名字校验:非空、无空白、限 `[A-Za-z0-9._-]`、≤ 32 字符。非法即**加载期/命令期报错**。
- 比较**大小写不敏感**(`bx server use HK` 认得 `hk`),存储保留用户给的原样。
- **匹配只按名字,不按主机**。故同一主机上的第二台(多用户 share)必须显式 `--name`,
  否则会就地更新第一台。这是刻意的单一规则:两条规则(名字 or 主机)会产生
  「我以为在加,其实在覆盖」的歧义,而单一规则的失败模式是「你需要 --name」,可教。
- `current` 必须指向清单里存在的名字,否则加载期报错。

## 切换语义

### 已知服务器之间(常见情况)

**一条路由都不改。** 清单里所有服务器的两个 host 在 Hijack 时就已经全部进了
`serverBypass` 与静态 DNS(把 `run.go:282-303` 的来源从 `cfg.Transports` 换成
`cfg.Servers` 的全部 link + udp 即可)。

切换 = **两个槽的原子配对切换**,由 Core 的**一个新端点** `POST /v0/server`
(body `{"link":"…","udp":"…"}`,`udp` 可为空)承担——**不是**对 `/v0/transport` 调两次:
两次调用之间没有共同的 Arm/undo,做不到原子。新端点复用同一个 `mutationEngine.Arm`,
把「换主 + 换 UDP」构造成**一对** apply/undo:

1. apply:主传输 `swapTo` 新链接(建新 → 等健康 → 原子换 → 停旧),再换 UDP 槽。
2. 目标服务器**没有** `udp:` 时把 UDP 槽清空(`SetUDPTransport(nil)`,UDP 回到主传输,
   既有语义)。
3. **任一步失败,undo 把两个槽一起换回原样**,并报错说明留在了哪台。

`mutator` 接口相应增加 `SetServer(link, udp string)`;`nopMutator` 与 fake 一并跟上。
既有 `/v0/transport` 与 `SetTransport` **原样保留**(MCP 控制面在用),不改其语义。

**不变量**:要么落到新服务器,要么留在旧的,**绝不落到「哪个都没有」**。这与
「停止永不依赖别的先成功」同源——切换失败不得让用户失去出口。

配对必须原子,是因为 bx **确实支持** TCP 与 UDP 走不同主机(按类分流),所以半切
状态是一个合法但用户从没要求过的配置,`status` 会显示
`传输 reality@tokyo UDP→hysteria2@hk`——不报错、还能用、但不是用户要的。

### 加一台新服务器(唯一棘手处)

新服务器的 IP 不在启动时算好的 `serverBypass` 里。**直接切过去就成环**。

- **bx 没在跑**:写 config、设 `current`,完事。
- **bx 正在跑**:
  1. 写 config(清单加一项)。
  2. 用**扩展后的** bypass 集合调 `/v0/rehijack`。为此 `liveMutator.serverBypass`
     必须从「启动时捕获的定值」改成**可更新的**(加一个 setter,由控制面在 rehijack
     前写入;`Rehijack()` 读当前值)。
  3. rehijack 成功才 swap 过去。
  4. **rehijack 失败 → 加进清单但不切**,明说「已加入,重连后生效」。
     **绝不在 bypass 没落实的情况下切过去。**

## 入口

### CLI

| 命令 | 语义 |
|---|---|
| `bx setup '<main>' [--udp '<udp>'] [--name X]` | **加入并切过去**;同名则就地更新 |
| `bx server list` | 清单 + 当前项标记 + 主机 |
| `bx server use <name>` | 切换 |
| `bx server rm <name>` | 删;删当前项或删到空都报错(先 `use` 别的) |
| `bx server rename <old> <new>` | 改名;`current` 同步跟着改 |

`bx server list` **只对当前那台报实测健康**(它是唯一有活隧道的);其余显示
`not connected`,**不编造延迟**——这与 `internal/observe` 的三态是同一条原则:
没测过就说没测过。

`bx setup` 的既有职责(装二进制、写配置、装服务、连通检测、**不启动**)一律不变;
唯一变化是它不再丢掉旧服务器。

### Guardian

- `/v1/status` 增加 `servers: [{name, host, current}]`(菜单要靠它渲染)。
  `host` 已经以 `server` 字段暴露在 status 里,不是新泄漏面。
- `POST /v1/servers/current`,body `{"name":"tokyo"}` → 切换。授权用
  **`authorizeOwnerPeer`**(同 `/v1/up`、`/v1/down`:改动网络的操作)。
- **顺序:先热切,成功了再写 `current`。** Guardian 转发到 Core 的 `POST /v0/server`
  做配对热切,成功后才把 `current` 写进 config。反过来(先写意图再执行)会在切换失败时
  留下「config 说 tokyo、实际跑 hk」的静默背离——而消化这种背离的调谐循环是**阶段③**
  的东西,今天还没有。等阶段③落地,这个顺序应当反过来(先声明意图,由调谐器收敛),
  届时这一段要重写。
- Core 不在跑时只改 config,回报「下次启动生效」——**这不是失败**(没有事实可背离)。

**CLI 与菜单调的是同一个 Guardian 端点。** 这条是刻意的:阶段①刚把菜单从
第三个控制面变成瘦客户端,新功能不能再开一个改动者。

### 菜单

一级菜单加 `Servers ▸` 子菜单:列名字、当前项打勾、点一下切。切换期间该项显示进行中,
失败弹出失败原因(与既有 toggle 失败提示同一条路径)。

菜单**不做**添加/删除服务器——粘贴链接的入口已经是既有的 Set Up 动作。

## 本期不做

- **自动容灾**(用户明确选择退场)。`transports:` 仍能解析,与 `servers:` 互斥。
- 订阅 / 机场格式导入、延迟排序、自动选最快、按地区分组。
- 每台服务器独立的 `rules` / `dns` / `udp.mode`——清单只管「走哪条隧道」。
- 菜单里增删服务器。

## 风险

**成环是本设计最危险的失败模式,而且是静默的**:连得上、`status` 显绿,而流量在绕圈
或直接泄漏。两条缓解,都必须有测试:

1. `serverBypass` 与静态 DNS 必须覆盖清单里**每一台**服务器的**两条**链接
   ——回归守卫直接钉住这个集合。
2. 加服务器时 rehijack 失败就**不切**。

**改 config 必须用 yaml.Node 外科手术**(`setup.UpdateTransports` 同款),不整份重写。
2026-08-06 真机事故:`--force` 整份重写把用户手写的 apple/steam 直连策略冲掉了。
`current` 的改写每次切换都发生,这条比以往更要紧。

**`liveMutator.serverBypass` 改成可变**引入了一个共享可变状态。它只在控制面路径上
被写(单线程 + 互斥),但必须有测试钉住 rehijack 读到的是写入后的值——读到陈旧值
就正好是上面那个成环。

## 测试策略

- **纯逻辑**:config 解析(`servers`/`current`/迁移/互斥报错/命名校验/大小写)、
  `bx setup` 的「加入 vs 就地更新」判定、名字冲突。
- **supervisor**:bypass + staticA 集合覆盖全部服务器的全部链接(回归守卫)。
- **切换原子性**:注入一个建不起来的 fake transport,断言两个槽都留在原处且报错;
  注入 UDP 失败而主成功,断言主也回滚。
- **无 `udp:` 的服务器**:切过去必须清空 UDP 槽,而不是留着上一台的。
- **Guardian**:`/v1/servers/current` 的授权(非业主非 root 拒绝)、Core 不在时的降级答复。
- **菜单**:列表与当前项渲染;点击调 Guardian 端点而**不是** spawn(纳入既有的
  反 spawn 守卫链)。
- **真机验收**(用户执行):两台之间来回切,`api.ipify.org` 出口分别等于两台的 IP;
  切到已关掉的服务器必须留在原地并报错;运行中加第三台后切过去不成环
  (出口正确 + `netstat -rn` 里有它的 bypass)。

## 分期

两段各自可发布、可单独真机验证:

- **A 段(config + core + CLI)**:配置模型与迁移、bypass 覆盖、UDP 配对端点、
  可变 bypass、Guardian 端点、`bx server *` 与 `bx setup` 语义。**A 段完成即可用。**
- **B 段(菜单)**:`Servers ▸` 子菜单。

B 排在 A 之后,是因为菜单只是同一个 Guardian 端点的第二个消费者;A 没真机验过之前
做 B,等于在没验证的地基上加一层 UI。
