# Task 9 验证基建(netns harness + CI 集成 + Hijack 往返 PoC)— 设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-25)。实现走单独的 plan。

## 目标 / 背景

Task 9(真实 Linux 路由快照器 + netns 端到端)需 root/Linux/netns,本机是 macOS 验不了。本设计**不做 Task 9**,而是先铺好"不依赖物理 Mudi 就能验证特权路由代码"的基建,并用一个 PoC 证明这套方式可行。结论一句话:**架构切干净 → 交叉编译 → CI 矩阵 → VM/netns → 真机终验**;本设计交付 CI/netns 这一层 + 一个证明它行得通的 PoC。

**非目标(留给 Task 9):** 真实通用快照器(`systemsnapshot_linux.go`)、接 MCP mutating ops、改 `newSystemSnapshotter()`、bx_diagnose 快照就绪、端到端 setup→verify→commit netns 测试。本设计一概不碰。

## 交付物

### A. 本地 netns harness(Mac 上可跑)
- 一份简短文档(`docs/` 或 README 段):`colima start` 起 Linux VM → VM 内 `sudo go test -tags integration ./...`。
- netns 拓扑由测试自身在 Go 里建/拆(`ip netns`/`veth`/`dummy`),**测试自包含、可重复**,不依赖外部脚本。

### B. CI 集成 job
- `.github/workflows/ci.yml` 新增 `integration` job:`runs-on: ubuntu-latest`,步骤 `sudo go test -tags integration ./...`。
- 现有 `test` job(无 root 单测 + linux/darwin×amd64/arm64 交叉编译)**不改**。
- 每次 push/PR 自动跑 PoC,替代"手动切机"。

### C. Hijack 往返 PoC 测试(方案 ①:零新生产代码)
- 新文件 `internal/supervisor/hijack_netns_linux_test.go`,build tag `//go:build integration && linux`。
- 在一个**临时 netns** 内:
  1. 建最小拓扑(dummy 或 veth + 一条默认路由)使 `Hijack` 有可操作对象。
  2. 调 bx 现有 `Hijack(...)`(`platform_linux.go`/`router_linux.go`)。
  3. 断言策略路由就位(`ip rule` 出现 pref 100/150/200,table 100 有 default)。
  4. 调返回的 `teardown()`。
  5. 断言规则集回到第 1 步基线(干净还原)。
- 证明的两件事:① netns 内能跑特权路由操作并断言;② bx 现有还原逻辑确实干净。这退掉 Task 9 最大的"验证方式可行性"未知。

## 安全与隔离(底线)

- **门控**:测试在 build tag `integration` 后;普通 `go test ./...`(含本机 Mac、现有 CI `test` job)**永不编译/运行**它。
- **前置 skip**:测试开头检测非 root 或缺 `ip` 命令 → `t.Skip`,绝不误跑。
- **netns 隔离**:所有路由改动只在测试自建的临时 netns 内;**绝不触碰宿主 / CI runner 的真实路由**。测试结束删除该 netns(defer)。
- CI runner 是一次性的;即便如此仍坚持 netns 隔离,不在 root namespace 改路由。

### 这套 harness 能验什么 / 不能验什么(VM-on-VPN 的根本限制)
- **能验**:不发真实外网包的逻辑——路由规则装/拆、分流决策、UDP 转发逻辑等。用自包含 netns 拓扑 + 本地 mock 上游(假 socks5/echo,不连真网)。**宿主是否挂 VPN 与结果无关**(本设计 PoC 即此类:只断言 `ip rule`/`ip route` 状态,零真实流量)。
- **不能验**:真实出口 IP / 真实泄漏审计(出口是否=VPS、有无 DNS/WebRTC/IPv6 泄漏)。原因:Colima VM 的真实出网经宿主 Mac 的网络栈,**若宿主挂着 VPN/TUN 劫持,VM 出口已被污染**——在这种环境里"测出口=VPS"可能只是宿主 VPN 的功劳,不是 bx 的。**任何 VM(尤其 VPN-on-host 的开发机)都无法诚实验证"无泄漏"。**
- **推论(写给 Task 9)**:真实泄漏审计**只能在真机(Mudi)的干净真实 WAN 上做**,不可在 VM/CI 里冒充完成。这是"真机最后一公里"不可省的根因。

## 架构契合

- 复用 bx 既有"决策/IO 分离"与平台接缝;PoC 只调现有 `Hijack`/`teardown`,不新增生产代码。
- 这是 Task 9 的跑道:Task 9 写真快照器时,其 `//go:build integration` 测试自动被同一 CI job + 本地 harness 接住。

## 测试策略

- PoC 自身就是 integration 测试;它的"测试"是断言 Hijack 装/拆规则的可观察效果。
- 验证基建有效性的判据:① CI `integration` job 绿;② 本机 `go test ./...` 仍绿且**跳过** integration(不误跑);③ 在 Colima VM 内 `sudo go test -tags integration ./...` PoC 通过。

## 决策记录

- PoC = 方案 ①(验证现有 Hijack 往返,零新生产代码),不写快照器原型(那是 Task 9)。
- 三交付物:本地 netns harness 文档 + CI `integration` job + `hijack_netns_linux_test.go`。
- 严守门控 + netns 隔离;现有 `test` job 与本机单测零影响。
- 不触 Task 9 的任何生产代码(快照器/MCP 接线/newSystemSnapshotter)。

## 范围自检

单一可实现增量(文档 + 一个 CI job + 一个门控测试文件),无生产代码改动,适合一份小 plan。
