# 多传输架构(自动容灾 + 按类分流 + 兼容现有协议)设计

Status: IMPLEMENTED + 容灾真机已验(2026-06-29)——S1-S5 全部落地(failover.go、config transports/udp.transport、blink bundle、rawLinkRisk、hysteria2 runner+with_quic),TDD + 全绿 + code review 闭环(修了多 server bypass 等)。**容灾 e2e 真机坐实**:VPS reality@9998 主 + brook@9999 备,杀 reality → runFailover 自动切 brook、出口仍=VPS(35.9s)。按类分流(UDP→hysteria)真机待验(需 hysteria 服务端)。
原始 Status: DRAFT — 自主拟定(2026-06-29,12h 自驱开发循环;无法交互 brainstorm,判断由我做,贯彻用户产品理念)。

## 背景与定位

bx 现状:单一传输,按 server link scheme 二选一(`vless://`→reality / `brook://`→brook),无运行期自动容灾;隧道挂 → kill-switch Block(fail-closed,不漏)。`SetTransport` 热切换机制已存在并验过(brook↔reality 双向、fail-closed),但只能手动触发。

用户产品理念(本设计的宪法):
1. **不泄漏是本质** —— 任何传输 / 容灾 / 分流路径,隧道不健康一律 fail-closed Block,绝不回落直连。不可妥协,优先于一切(含速度)。
2. **配置简单、兼容现有** —— 用户甩来 `vless://`/`brook://`(将来 `hysteria2://`/`vmess://`/`trojan://`/`ss://`)能**直接用**;但**提示风险**(明文凭据入配置、可肩窥)并**建议 `bx blink` 换壳**。
3. **安全前提下要速度** —— 不同协议有不同强项;在不破坏①的前提下,让合适的流量走合适的传输换取速度。

## 目标 / 非目标

**目标**
- **G1 多传输配置 + 健康驱动自动容灾**:config 支持有序多传输(reality 主 / brook 备 / 将来 hysteria),当前传输持续不健康 → 自动 `swapTo` 下一个;切换全程 fail-closed;**强防抖**(滞回 + 冷静期 + 两者皆挂时不切)。
- **G2 兼容现有协议链接直收 + blink 建议**:`bx setup`/`bx probe` 接受裸 `vless://`/`brook://`(及将来更多 scheme),打印一行风险提示 + 建议 blink 换壳。
- **G3 按流量类别选传输**:不同 class(如 UDP/QUIC、bulk、低延迟交互)可指派不同传输(如 UDP/QUIC 走 hysteria2、其余 TCP 走 reality),每条路径独立保 fail-closed。
- **G4 单 link bundle**:blink envelope 扩成可装多传输,一条 `bx://` 一贴配好全部(救活现在 vestigial 的 `Transport` 字段)。

**非目标(本轮)**
- 不做 GUI / 自动测速选路(先静态优先级 + 健康,不做主动 RTT 探测择优——留后续)。
- 不强推 hysteria2 为默认(reality 仍是默认主传输);hysteria 作为**可选的 UDP/速度档**。
- 不碰数据面安全不变量的语义(kill-switch / 私网恒直连 / 防环),只在其上做编排。

## 架构

### A. 兼容现有协议链接(G2)—— 最低摩擦 + 安全提示
- `normalizeClientLink`(已认 brook/vless)扩成**已知 scheme 表**:`brook://`/`vless://`(现)→ 将来 `hysteria2://`/`vmess://`/`trojan://`/`ss://`(待各自 runner)。
- 裸链接 setup 时:**直接接受**(写 config / 换壳 bx://),但 stderr 打一行:
  `⚠ 裸链接含明文凭据,已存入 0600 配置;分享/留存建议先 bx blink 换壳成 bx://`。
- 原则:**直收降摩擦,提示+建议升安全**;不强制(强制 blink 会赶跑"甩个 link 就想用"的用户)。

### B. 多传输配置 + 自动容灾(G1)
- **config schema**:新增 `transports: [<link>, ...]`(有序=优先级;reality 主在前)。保留单 `server:` 为兼容(= 单元素 transports)。
- **传输引擎**:复用 `tunnel.Tunnel/Runner/socks5Health` 抽象;每个 link 一个可构造的 tunnel(`buildTunnel` 已按 scheme 派发,扩 hysteria runner 即可)。
- **容灾控制器**(新,基于现有 `transportSwapper`):
  - 维护「当前活跃传输」指针(已有 `liveTunnel`)。
  - 后台监健康:**当前传输连续不健康 ≥ `failoverAfter`(默认建议 20–30s,> 一次抖动)** → 选优先级中下一个**健康候选**,`swapTo` 之;切换沿用 swapper 的「建新→等健康→SetTransport→停旧」,**全程 Block 不漏**。
  - **防抖三件套**(吃过假抖动的亏):① 滞回——切前要求持续不健康窗口;② 冷静期——刚切过 `cooldown`(建议 60s)内不再切;③ **全挂不切**——若所有候选都不健康,判定是网络问题(非传输被封),**保持当前 + 继续 Block**,不无意义横跳。
  - 候选健康用**廉价旁路探测**(各传输独立起一个 socks5Health 轻探,或复用其 tunnel 的 health),避免"切过去才发现也挂"。
- **回切策略**:切到备用后,**不自动抢回主**(避免主端口抖动→反复横跳);主恢复且稳定一段时间后可选回切(留 flag,默认不回切,保守)。

### C. 按流量类别选传输(G3)—— 安全的速度
- 现 `dialer` 决策是 `Direct/Proxy/Block`;扩成 **Proxy 可指定"走哪个传输"**。
- **class → transport 映射**(config `route.transport_by_class` 或 udp.mode 联动):
  - `udp`/`quic`(UDP:443):若配了 hysteria → 走 hysteria(QUIC 原生、丢包链路快);否则走主传输的 UDP-over-stream(vless 支持)或按 `udp.mode` block。
  - `tcp-general`:走主传输(reality)。
  - (将来)`bulk`/大流量:可指派吞吐更好的传输。
- **安全约束(硬)**:每个 class 的传输路径**独立 fail-closed**——该 class 指派的传输挂了,该 class 的连接 Block(或按容灾切到下一个能承载该 class 的传输),**绝不因"想要速度"回落直连**。direct-realtime UDP 那种"换匿名"的模式仍受 kill-switch 管(已修)。
- hysteria 引入:作为又一个 `tunnel` runner(sing-box 同样支持 hysteria2 inbound/outbound,**已内嵌的静态 sing-box 直接能跑**,零新二进制)。服务端要多开一个 hysteria2 监听(QUIC/UDP 高端口)。

### D. 单 link bundle(G4)
- `blink` envelope 现 `{v, transport, link}` 扩成 `{v, transports:[{scheme/link}...], class_map?}`;`Encode` 可打包多传输,`Decode` 还原成 transports 列表。
- `bx blink <link1> <link2> ...` → 一条 `bx://`;`bx setup bx://…` 一次配好多传输 + 容灾。
- 兼容:旧单传输 envelope 仍解。

## 不变量(改动时务必保住)
- **不泄漏**:容灾切换窗口、按类分流、所有传输——隧道不健康一律 Block,绝不直连回落。这是验收的第一条。
- **防环**:每个传输的 server 连接经 serverBypass 走原网关(`serverHostFromLink` 扩成认所有 scheme 的 host)。
- **私网/docker 恒直连**:不受多传输影响。
- **热重载不断流**:换传输/换 china 列表沿用原子指针。

## 分期(增量 slice,每片 TDD + 可验证)
1. **S1 容灾控制器**:`transports:` 配置解析 + 健康驱动自动 failover(基于现有 swapper)+ **防抖**(滞回/冷静/全挂不切)。纯逻辑可单测(注入假 health)。← **先做这个,价值最高、机制现成。**
2. **S2 裸链接直收 + blink 建议**:normalizeClientLink 风险提示;低风险小改。
3. **S3 单 link bundle**:blink envelope 多传输;TDD round-trip。
4. **S4 hysteria runner**:tunnel 加 hysteria2 runner(sing-box 配置生成)+ 按 UDP class 选它。先在 Mudi 真机验 UDP/QUIC 提速。
5. **S5 按类分流通用化**:dialer 决策扩"走哪个传输",class_map。

## 自主决策记录(我替用户拍的,可回退)
- reality 永远是**默认主**;hysteria 是**可选 UDP/速度档**,不抢主。
- 容灾**默认不自动回切**(防横跳),保守。
- 裸链接**直收不强制 blink**,只提示(降摩擦 > 强制安全,因为这是单人/可信场景;企业分发可加 strict)。
- 不做主动测速择优(YAGNI,先静态优先级 + 健康)。
- 全挂时**保持 Block 不横跳**——把"传输被封"和"网络断"分开,后者不该触发 failover 风暴。
