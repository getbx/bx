# 按应用看分流 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让用户在一个自己开关的悬浮窗里,一眼看到哪些应用在走隧道、哪些直连、哪些被 kill-switch 挡了。

**Architecture:** TUN 引擎把已有但被丢弃的应用侧源端口(`id.RemotePort`)带进 `route.Meta`;订阅开启时,数据面把 `(源端口, 判定, 规则)` 写进一个环形缓冲,并按源端口累加字节;后台 worker 读 macOS 的 `net.inet.{tcp,udp}.pcblist_n` 把源端口 join 成 PID 再解成应用名;结果经 Core `/v0/apps` → Guardian `/v1/apps` → 菜单悬浮窗。**分流判定一个字不改**,应用身份全程是旁观者。

**Tech Stack:** Go 1.26(`CGO_ENABLED=0`)、gVisor netstack、`golang.org/x/sys/unix`、AppKit/Swift(SwiftPM,无 XCTest,自写断言脚本)

**Spec:** `docs/superpowers/specs/2026-08-19-app-traffic-attribution-design.md`

## Global Constraints

- **平台:macOS 独占。** 非 darwin 必须编译通过,且整条链报「不支持」而不是空数据。所有平台相关文件成对出现:`*_darwin.go` + `*_other.go`(`//go:build !darwin`)。
- **`CGO_ENABLED=0` 不可破。** 禁止引入 cgo、禁止 fork `lsof`/`netstat`。只允许 `unix.SysctlRaw`。
- **分流行为零改变。** 本计划不得修改 `internal/route` 的任何判定逻辑。
- **不落盘、不进日志。** 应用名、端口、连接记录一律只在内存,且只在订阅期间存在。
- **每个 task 结束前跑 `bash scripts/verify.sh --quick`;最后一个 task 跑全量 `bash scripts/verify.sh`。判据是退出码,不是字符串匹配。**
- **TDD:先写失败测试 → 跑红 → 最小实现 → 跑绿 → 提交。** 提交信息用中文 conventional commits,结尾带 `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`。
- **凡是「守卫」性质的测试,必须做变异验证**(把被守的那行改坏,确认测试转红,再改回来),并把变异结果写进提交信息。
- 归因数据结构里,「没订阅」「订阅了但还没数据」「查不出应用」是**三种不同的状态**,不许合并。

---

## File Structure

| 文件 | 职责 |
|---|---|
| `internal/appattr/pcb.go` | 纯解析:`ParsePcbList` —— 两个字节布局陷阱锁死在这里 |
| `internal/appattr/owner.go` | 纯判据:`ChooseOwner`(委托+活性)、`DisplayName` |
| `internal/appattr/report.go` | 纯聚合:`Aggregate` —— 连接记录 + 端口映射 → 三组报告 |
| `internal/appattr/purity_test.go` | AST 守卫:本包不许 import net/os/exec/syscall/x/sys |
| `internal/appattr/testdata/pcblist_tcp.bin` | 真机采的脱敏 fixture(含 `XSO_TCPCB` 块) |
| `internal/supervisor/appsource_darwin.go` | 平台原语:两个 sysctl + `procargs2` + 活性判断 |
| `internal/supervisor/appsource_other.go` | 非 darwin 桩:恒返回 `errAppSourceUnsupported` |
| `internal/supervisor/apptraffic.go` | 订阅、环形缓冲、按端口字节数组、后台 worker |
| `internal/tun/engine.go` | 修改:`metaFromID` 带上 `SrcPort`;`relay` 把 srcPort 交给字节计数 |
| `internal/route/types.go` | 修改:`Meta` 加 `SrcPort uint16` |
| `internal/route/explain_test.go` | 新增守卫:判定对 `SrcPort` 完全不敏感 |
| `internal/supervisor/control.go` | 修改:挂 `/v0/apps` |
| `internal/guardian/apps.go` | Guardian `/v1/apps` handler + 客户端 |
| `internal/guardian/types.go` | 修改:`CapabilityApps` |
| `apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift` | 纯函数:JSON → 可渲染行 |
| `apps/macos/BxMenu/Sources/BxMenu/AppTrafficWindow.swift` | `NSWindow(level: .floating)` |
| `apps/macos/BxMenu/Tests/AppTrafficModelTests.swift` | Swift 断言(**必须登记进 `scripts/test-macos-menu.sh`**) |

---

### Task 1: 证实 join 键(spec 里唯一的残留未验项)

整个功能压在一句没验过的话上:**gVisor 给的 `id.RemotePort` 就是应用 socket 的本地端口**。pcblist 那一半 spike 已经证实了;这一半今天只是推理。**先证实它,再写别的** —— 否则可能做到最后一个 task 才发现两边对不上。

**Files:**
- Modify: `internal/route/types.go`
- Modify: `internal/tun/engine.go:metaFromID`
- Test: `internal/tun/engine_integration_test.go`

**Interfaces:**
- Produces: `route.Meta.SrcPort uint16` —— 后续所有 task 的 join 键。

- [ ] **Step 1: 写失败测试**

在 `internal/tun/engine_integration_test.go` 末尾追加。它复用同文件已有的 `captureDialer` 与 `TestEngine_TCP_DialerReceivesDestination` 的建栈方式(照抄那个测试的 setup 部分,不要另起炉灶)。

```go
// TUN 引擎看到的 id.RemotePort 必须就是应用侧 socket 的本地端口 —— 整个应用归因
// 靠它跟 macOS 的 pcblist(按 lport 索引)对上。这条关系此前只是推理:metaFromID
// 用 id.Local* 当目的地,于是 Remote* "应该"是应用侧。做到界面才发现对不上,代价
// 是整条链白写,所以第一步就钉死它。
func TestEngine_TCP_MetaCarriesApplicationSourcePort(t *testing.T) {
	const wantSrcPort = 51234

	dialer := newCaptureDialer()
	// 照抄 TestEngine_TCP_DialerReceivesDestination 的建栈与注入方式,
	// 唯一的区别是客户端源端口用 wantSrcPort 这个确定值。
	client, cleanup := newTestClient(t, dialer)
	defer cleanup()
	client.connectTCP(t, wantSrcPort, netip.MustParseAddr("198.18.0.7"), 443)

	metas := dialer.snapshot()
	if len(metas) != 1 {
		t.Fatalf("Dial 次数 = %d, want 1", len(metas))
	}
	if got := metas[0].SrcPort; got != wantSrcPort {
		t.Fatalf("Meta.SrcPort = %d, want %d —— join 键不成立,应用归因整条链无从对上", got, wantSrcPort)
	}
}
```

> 实施说明:`newTestClient` / `connectTCP` / `dialer.snapshot()` 是本 task 要顺手抽出来的**测试辅助**,内容直接来自 `TestEngine_TCP_DialerReceivesDestination` 里已有的建栈、`pipe` 端点、`header.TCPFields` 构造与 `captureDialer.metas` 读取代码。抽出来是因为 UDP 那条(Step 6)要用同一套。不要新写协议构造逻辑。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/tun/ -run TestEngine_TCP_MetaCarriesApplicationSourcePort -v`
Expected: 编译失败 —— `metas[0].SrcPort undefined (type route.Meta has no field or method SrcPort)`

- [ ] **Step 3: 给 `route.Meta` 加字段**

`internal/route/types.go`:

```go
type Meta struct {
	Domain string
	IP     netip.Addr
	Port   uint16
	UDP    bool

	// SrcPort 是**应用侧** socket 的本地端口(gVisor 的 id.RemotePort)。
	//
	// **它不是判据。** 加它只为让应用归因能跟 macOS 的 pcblist 对上 ——
	// 那张表按 lport 索引。Router 对它必须完全不敏感,由
	// TestExplainIgnoresSrcPort 钉住:按源端口分流会让「用户看到的分流」
	// 和「bx 实际执行的分流」出现第二个变量。
	SrcPort uint16
}
```

- [ ] **Step 4: 让 `metaFromID` 带上它**

`internal/tun/engine.go`:

```go
func metaFromID(id stack.TransportEndpointID, udp bool) route.Meta {
	return route.Meta{
		IP:      addrToNetip(id.LocalAddress),
		Port:    id.LocalPort,
		UDP:     udp,
		SrcPort: id.RemotePort, // 应用侧端口:Local* 是目的地,Remote* 是发起方
	}
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/tun/ -run TestEngine_TCP_MetaCarriesApplicationSourcePort -v`
Expected: PASS

- [ ] **Step 6: UDP 那半也钉一条**

腾讯会议的媒体流是 UDP,漏掉 UDP 等于漏掉这个功能最初的用例。照抄 Step 1 的测试,改用 `client.connectUDP`,函数名 `TestEngine_UDP_MetaCarriesApplicationSourcePort`。

Run: `go test ./internal/tun/ -run 'TestEngine_(TCP|UDP)_MetaCarriesApplicationSourcePort' -v`
Expected: 两条都 PASS

- [ ] **Step 7: 加「判定不许看源端口」的守卫**

`internal/route/explain_test.go`:

```go
// Meta 是连接元数据,不是判据全集。SrcPort 只为应用归因存在,一旦它能影响判定,
// 「用户看到的分流」和「bx 实际执行的分流」就有了第二个变量。
func TestExplainIgnoresSrcPort(t *testing.T) {
	r := &Router{
		UserDirect:  NewDomainSet([]string{"*.qq.com"}),
		ChinaDomain: NewDomainSet([]string{"example.cn"}),
		ChinaCIDR:   mustCIDRSet(t, []string{"1.2.3.0/24"}),
		PrivateDirect: mustCIDRSet(t, DefaultPrivateCIDRs),
	}
	bases := []Meta{
		{Domain: "a.qq.com"},
		{Domain: "example.cn"},
		{Domain: "claude.ai"},
		{IP: netip.MustParseAddr("1.2.3.4")},
		{IP: netip.MustParseAddr("192.168.1.5")},
		{IP: netip.MustParseAddr("8.8.8.8"), UDP: true},
	}
	for _, base := range bases {
		want, wantWhy := r.Explain(base)
		for _, port := range []uint16{0, 1, 443, 51234, 65535} {
			m := base
			m.SrcPort = port
			got, gotWhy := r.Explain(m)
			if got != want || gotWhy != wantWhy {
				t.Fatalf("%+v 在 SrcPort=%d 时判定变了: %v/%+v -> %v/%+v",
					base, port, want, wantWhy, got, gotWhy)
			}
		}
	}
}
```

> `mustCIDRSet` 若本包测试里还没有,就写一个三行的 helper(`NewCIDRSet` + `t.Fatal`)。

- [ ] **Step 8: 变异验证这条守卫**

临时在 `Explain` 开头插入 `if m.SrcPort == 443 { return Direct, Reason{Source: SourceUserDirect} }`,跑 `go test ./internal/route/ -run TestExplainIgnoresSrcPort`,确认 **FAIL**,再删掉。

- [ ] **Step 9: 跑闸门并提交**

```bash
bash scripts/verify.sh --quick
git add internal/route internal/tun
git commit -m "feat(route): Meta 带上应用侧源端口,并钉住判定不许看它

应用归因的 join 键:gVisor 的 id.RemotePort 就是应用 socket 的本地端口,而
macOS 的 pcblist 按 lport 索引。spec 里这条只是推理(metaFromID 用 id.Local*
当目的地,所以 Remote* 应该是发起方),没人实测过 —— 做到界面才发现对不上,
整条链白写,所以第一步就用真 netstack 钉死它,TCP/UDP 各一条。

同时加 TestExplainIgnoresSrcPort:Meta 是连接元数据,不是判据全集。变异验证:
在 Explain 开头按 SrcPort 返回 Direct,该测试转红。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `internal/appattr` —— pcblist 解析(两个陷阱锁死在这里)

**Files:**
- Create: `internal/appattr/pcb.go`
- Create: `internal/appattr/pcb_test.go`
- Create: `internal/appattr/testdata/pcblist_tcp.bin`
- Create: `internal/appattr/purity_test.go`

**Interfaces:**
- Produces:
  - `type PCB struct { LocalPort, RemotePort uint16; LastPID, EPID int32 }`
  - `func ParsePcbList(raw []byte) ([]PCB, error)`

- [ ] **Step 1: 采一份真实 fixture**

**合成 fixture 挡不住这两个坑**(8 字节对齐那个只在有 `XSO_TCPCB` 块时才出现),所以 fixture 必须来自真机。写一个一次性程序落到 scratchpad(**不进仓库**):

```go
package main

import ("os"; "golang.org/x/sys/unix")

func main() {
	raw, err := unix.SysctlRaw("net.inet.tcp.pcblist_n")
	if err != nil { panic(err) }
	// 脱敏:XSO_INPCB 块里 offset 20 之后是地址,整段抹零;端口(16..20)保留。
	off := int(raw[0]) | int(raw[1])<<8 | int(raw[2])<<16 | int(raw[3])<<24
	for off+8 <= len(raw) {
		l := int(raw[off]) | int(raw[off+1])<<8 | int(raw[off+2])<<16 | int(raw[off+3])<<24
		k := raw[off+4]
		if l < 8 || off+l > len(raw) { break }
		if k == 0x10 {
			for i := off + 20; i < off+l; i++ { raw[i] = 0 }
		}
		off += (l + 7) &^ 7
	}
	os.WriteFile("pcblist_tcp.bin", raw, 0o644)
}
```

把产物拷到 `internal/appattr/testdata/pcblist_tcp.bin`。**跑之前先确认机器上有若干 ESTABLISHED 的 TCP 连接**(`lsof -nP -i4TCP -sTCP:ESTABLISHED | wc -l` ≥ 20),否则 fixture 里可能没有 `XSO_TCPCB` 块,守卫就成了空转。同时记下当时的条数,写进测试注释。

- [ ] **Step 2: 写失败测试**

`internal/appattr/pcb_test.go`:

```go
package appattr

import (
	"os"
	"testing"
)

// fixture 是 2026-08-19 从项目所有者的 Mac 上采的真实 net.inet.tcp.pcblist_n,
// 地址字段已抹零、端口保留。**合成 fixture 挡不住下面两个坑**:8 字节对齐那个
// 只在存在 XSO_TCPCB 块时才出现,而合成数据不会有它。
func loadFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/pcblist_tcp.bin")
	if err != nil {
		t.Fatalf("读不到 fixture,守卫失去意义: %v", err)
	}
	return raw
}

// 坑一:块按 8 字节对齐,而 xso_len **不含**尾部填充。TCP 的 XSO_TCPCB 块 len=204,
// 下一块其实在 +208。不补齐则整张表在第一条之后就走飞,只解出 1 条 —— 而 UDP 没有
// 那个块,看起来完全正常,于是极易被误判成「UDP 能做、TCP 不能」。
func TestParsePcbListWalksPastEightByteAlignmentPadding(t *testing.T) {
	pcbs, err := ParsePcbList(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(pcbs) < 20 {
		t.Fatalf("只解出 %d 条 —— 真机 fixture 采集时有上百条,块遍历走飞了", len(pcbs))
	}
}

// 坑二:so_last_pid 在偏移 68、so_e_pid 在 72,**不在块尾**(其后还有 so_gencnt /
// so_flags / so_flags1 / so_usecount / so_retaincnt / xso_filter_flags)。
// 按「结构体最后两个字段」从块尾往回取,TCP 全读成 0。
func TestParsePcbListReadsPIDAtTheCorrectOffset(t *testing.T) {
	pcbs, err := ParsePcbList(loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	withPID := 0
	for _, p := range pcbs {
		if p.LastPID > 0 {
			withPID++
		}
	}
	// 真机实测 TCP 205/205 全带 PID。留出余量,但 0 条一定是偏移错了。
	if withPID*2 < len(pcbs) {
		t.Fatalf("%d/%d 条带 PID —— pid 偏移取错了(块尾是 so_retaincnt/xso_filter_flags,不是 pid)",
			withPID, len(pcbs))
	}
}

func TestParsePcbListRejectsTruncatedInput(t *testing.T) {
	if _, err := ParsePcbList([]byte{1, 2, 3}); err == nil {
		t.Fatal("截断输入必须报错 —— 悄悄返回空列表会被读成「没有连接」")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/appattr/ -v`
Expected: 编译失败 —— `undefined: ParsePcbList`

- [ ] **Step 4: 实现**

`internal/appattr/pcb.go`:

```go
// Package appattr 是「这条连接是哪个应用发起的」这件事的**纯判据**:
// 解析 macOS 的 pcblist 字节、按委托规则选出归属进程、把可执行路径变成显示名、
// 把连接记录聚合成报告。它不读文件、不联网、不跑命令 —— 取数据是调用方的事。
package appattr

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// xgen_n 的 kind。每个块自带长度与种类,所以不必知道全部字段布局。
const (
	kindSocket = 0x001
	kindInpcb  = 0x010
)

// XSO_SOCKET 块里的字段偏移。**实测得来**(2026-08-19,macOS 25.5,与 lsof 逐条对账):
//
//	[64] so_uid   [68] so_last_pid   [72] so_e_pid
//
// 其后还有 so_gencnt(8) / so_flags / so_flags1 / so_usecount / so_retaincnt /
// xso_filter_flags —— 所以**不能**从块尾往回取,那是最初写错的地方,TCP 会全读成 0。
const (
	offSoLastPID = 68
	offSoEPID    = 72
	minSocketLen = offSoEPID + 4
)

// PCB 是一条内核 socket 记录里我们关心的全部内容。
type PCB struct {
	LocalPort  uint16 // 应用侧本地端口 —— 与 route.Meta.SrcPort 的 join 键
	RemotePort uint16
	LastPID    int32 // 最后一个用过这个 socket 的进程
	EPID       int32 // 「替谁干活」;可能指向已退出的进程,取用前必须查活性
}

var errShortPcbList = errors.New("pcblist too short")

// ParsePcbList 解析 net.inet.{tcp,udp}.pcblist_n 的原始字节。
//
// 布局:开头一个 struct xinpgen(自带 xig_len),之后是一串自描述的块
// {u32 len, u32 kind, …};每个 pcb 由连续的若干块组成,XSO_INPCB 打头、
// XSO_SOCKET 紧随,TCP 还会多出 XSO_TCPCB 等。
func ParsePcbList(raw []byte) ([]PCB, error) {
	if len(raw) < 24 {
		return nil, errShortPcbList
	}
	off := int(binary.NativeEndian.Uint32(raw[0:4]))
	if off < 8 || off > len(raw) {
		return nil, fmt.Errorf("pcblist header length %d out of range", off)
	}
	var out []PCB
	var cur PCB
	var haveInpcb bool
	for off+8 <= len(raw) {
		blkLen := int(binary.NativeEndian.Uint32(raw[off : off+4]))
		kind := binary.NativeEndian.Uint32(raw[off+4 : off+8])
		if blkLen < 8 || off+blkLen > len(raw) {
			break // 尾部的 xinpgen,或截断
		}
		blk := raw[off : off+blkLen]
		switch kind {
		case kindInpcb:
			// xi_len(4) xi_kind(4) xi_inpp(8) inp_fport(2) inp_lport(2)…
			// 端口是网络字节序。
			if blkLen >= 20 {
				cur = PCB{
					RemotePort: binary.BigEndian.Uint16(blk[16:18]),
					LocalPort:  binary.BigEndian.Uint16(blk[18:20]),
				}
				haveInpcb = true
			}
		case kindSocket:
			if haveInpcb && blkLen >= minSocketLen {
				cur.LastPID = int32(binary.NativeEndian.Uint32(blk[offSoLastPID : offSoLastPID+4]))
				cur.EPID = int32(binary.NativeEndian.Uint32(blk[offSoEPID : offSoEPID+4]))
				out = append(out, cur)
				haveInpcb = false
			}
		}
		// **块按 8 字节对齐,而 xso_len 不含尾部填充。** TCP 的 XSO_TCPCB 块
		// len=204,下一块其实在 +208 —— 不补齐会读到全 0,整张 TCP 表只解出一条,
		// 而 UDP(没有那个块)看起来完全正常。
		off += (blkLen + 7) &^ 7
	}
	return out, nil
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/appattr/ -v`
Expected: 三条全 PASS

- [ ] **Step 6: 变异验证两个陷阱守卫**

1. 把 `off += (blkLen + 7) &^ 7` 改成 `off += blkLen` → `TestParsePcbListWalksPastEightByteAlignmentPadding` 必须 **FAIL**。
2. 把 `offSoLastPID` 改成 `blkLen-8` 的等价写法(直接用 `blk[blkLen-8:blkLen-4]`)→ `TestParsePcbListReadsPIDAtTheCorrectOffset` 必须 **FAIL**。

两条都确认转红后改回。

- [ ] **Step 7: 加 purity 守卫**

`internal/appattr/purity_test.go` —— 照抄 `internal/rulereview/purity_test.go` 的结构(读本包目录、AST 解析 import、命中禁令即失败、**读不到目录必须 `t.Fatal` 响亮失败**),禁令表改成:

```go
banned := map[string]string{
	"os":                  "读文件会让判据依赖运行环境;取数据是调用方的事",
	"os/exec":             "跑命令属于组装层,且 fork lsof 正是本设计要避免的",
	"net":                 "判据不许联网",
	"net/http":            "判据不许联网",
	"syscall":             "系统调用属于 appsource_darwin.go,不属于判据",
	"golang.org/x/sys":    "同上 —— 前缀匹配,x/sys/unix 也在内",
}
```

`allowedInternalDeps` 为空 map(本包不依赖任何 internal 包)。

- [ ] **Step 8: 跑闸门并提交**

```bash
go test ./internal/appattr/ -v
bash scripts/verify.sh --quick
git add internal/appattr
git commit -m "feat(appattr): pcblist 纯解析,两个字节布局陷阱各配一条真机 fixture 守卫

坑一:块按 8 字节对齐而 xso_len 不含填充(TCP 的 XSO_TCPCB len=204,下一块在
+208)。不补齐则整张 TCP 表只解出 1 条,而 UDP 没有那个块、看起来完全正常 ——
一半好一半坏,最容易被误判成「UDP 能做 TCP 不能」。
坑二:so_last_pid 在偏移 68、so_e_pid 在 72,不在块尾(其后还有 so_gencnt/
so_flags/so_flags1/so_usecount/so_retaincnt/xso_filter_flags)。按块尾取,
TCP 全读成 0。

fixture 是真机采的(地址抹零、端口保留)—— 合成数据不会有 XSO_TCPCB 块,
挡不住坑一。变异验证:两处分别改坏,对应测试各自转红。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 归因规则 —— `ChooseOwner` 与 `DisplayName`

**Files:**
- Create: `internal/appattr/owner.go`
- Create: `internal/appattr/owner_test.go`

**Interfaces:**
- Consumes: `PCB`(Task 2)
- Produces:
  - `func ChooseOwner(pcb PCB, alive func(int32) bool) (pid int32, ok bool)`
  - `func DisplayName(execPath string) string`

- [ ] **Step 1: 写失败测试**

```go
package appattr

import "testing"

func aliveSet(pids ...int32) func(int32) bool {
	live := map[int32]bool{}
	for _, p := range pids {
		live[p] = true
	}
	return func(p int32) bool { return live[p] }
}

// so_e_pid 是「替谁干活」——把 nsurlsessiond / trustd 这类代劳者的流量归回真正
// 发起的那个应用。它优先。
func TestChooseOwnerPrefersLiveDelegatingProcess(t *testing.T) {
	pid, ok := ChooseOwner(PCB{LastPID: 485, EPID: 7961}, aliveSet(485, 7961))
	if !ok || pid != 7961 {
		t.Fatalf("ChooseOwner = %d,%v; want 7961,true", pid, ok)
	}
}

// **spike 真机撞到过这一条**:trustd 的 so_e_pid 指向 7961,而 7961 早就退出了。
// 不查活性就会把流量记在一个不存在的应用上 —— 而那种错误在界面上完全看不出来。
func TestChooseOwnerFallsBackWhenDelegatorIsDead(t *testing.T) {
	pid, ok := ChooseOwner(PCB{LastPID: 485, EPID: 7961}, aliveSet(485))
	if !ok || pid != 485 {
		t.Fatalf("ChooseOwner = %d,%v; want 485,true(委托方已退出应回落 so_last_pid)", pid, ok)
	}
}

func TestChooseOwnerUsesLastPIDWhenNoDelegation(t *testing.T) {
	pid, ok := ChooseOwner(PCB{LastPID: 607, EPID: 0}, aliveSet(607))
	if !ok || pid != 607 {
		t.Fatalf("ChooseOwner = %d,%v; want 607,true", pid, ok)
	}
}

// 「问不出来」不许被压成一个具体答案(与 Tristate、WhoOwnsTheRoute 四态同源)。
func TestChooseOwnerReportsUnknownRatherThanGuessing(t *testing.T) {
	for _, p := range []PCB{
		{LastPID: 0, EPID: 0},
		{LastPID: -1, EPID: -1},
		{LastPID: 999, EPID: 0}, // last_pid 也已退出
	} {
		if pid, ok := ChooseOwner(p, aliveSet()); ok {
			t.Fatalf("%+v 应判 unknown,却给出了 pid=%d", p, pid)
		}
	}
}

func TestDisplayNameUsesBundleNameThenBasename(t *testing.T) {
	cases := map[string]string{
		"/Applications/TencentMeeting.app/Contents/MacOS/TencentMeeting": "TencentMeeting",
		"/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper": "Claude",
		"/System/Library/PrivateFrameworks/IDS.framework/identityservicesd.app/Contents/MacOS/identityservicesd": "identityservicesd",
		"/usr/libexec/trustd": "trustd",
		"/opt/homebrew/bin/limactl": "limactl",
		"": "",
	}
	for in, want := range cases {
		if got := DisplayName(in); got != want {
			t.Fatalf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
```

> 注意 `Claude Helper` 那条:期望是 **`Claude`** —— 取**最外层**的 `.app` bundle 名,helper 进程要归到它所属的应用上,否则 Chrome/Claude 这类多进程应用会散成一堆看不懂的行。而 `identityservicesd.app` 是系统私有框架里的内嵌 bundle,它没有外层 `.app`,所以取它自己。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/appattr/ -run 'TestChooseOwner|TestDisplayName' -v`
Expected: 编译失败 —— `undefined: ChooseOwner`

- [ ] **Step 3: 实现**

`internal/appattr/owner.go`:

```go
package appattr

import (
	"path"
	"strings"
)

// ChooseOwner 决定一条 socket 记录该算在哪个进程头上。
//
//	so_e_pid 有值且**进程还活着** → 用它(代劳者归回真正的发起方)
//	否则                          → so_last_pid(也要活着)
//	都不行                        → unknown
//
// **活性检查不是可选的。** spike 真机撞到过 trustd 的 so_e_pid 指向一个早已退出
// 的 7961:委托方死了之后那个字段就是个陈旧值,照用会把流量记在不存在的应用上,
// 而这种错误在界面上完全看不出来。
func ChooseOwner(pcb PCB, alive func(int32) bool) (int32, bool) {
	if alive == nil {
		return 0, false
	}
	for _, pid := range [...]int32{pcb.EPID, pcb.LastPID} {
		if pid > 0 && alive(pid) {
			return pid, true
		}
	}
	return 0, false
}

// DisplayName 把可执行路径变成用户认得的名字。
//
// 取**最外层**的 .app bundle 名:Chrome / Claude 这类多进程应用的 helper 住在
// 内层 bundle 里(…/Claude.app/Contents/Frameworks/Claude Helper.app/…),
// 取内层会让一个应用散成一堆看不懂的行。没有 .app 就取 basename。
func DisplayName(execPath string) string {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		return ""
	}
	if i := strings.Index(execPath, ".app/"); i >= 0 {
		return path.Base(execPath[:i])
	}
	if strings.HasSuffix(execPath, ".app") {
		return path.Base(strings.TrimSuffix(execPath, ".app"))
	}
	return path.Base(execPath)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/appattr/ -v`
Expected: 全 PASS

- [ ] **Step 5: 变异验证活性检查**

把 `pid > 0 && alive(pid)` 改成 `pid > 0`,确认 `TestChooseOwnerFallsBackWhenDelegatorIsDead` 与 `TestChooseOwnerReportsUnknownRatherThanGuessing` **转红**,再改回。

- [ ] **Step 6: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/appattr
git commit -m "feat(appattr): 归因规则 —— 委托优先但必须查活性,查不出就 unknown

so_e_pid(替谁干活)优先于 so_last_pid(谁拿着 fd),把 nsurlsessiond/trustd
这类代劳者的流量归回真正的应用。但**必须查活性**:spike 真机上 trustd 的
so_e_pid 指向 7961,而 7961 早已退出 —— 照用会把流量记在一个不存在的应用上,
而这种错误在界面上完全看不出来。

DisplayName 取最外层 .app bundle 名:helper 进程住在内层 bundle 里
(Claude.app/…/Claude Helper.app/…),取内层会让一个应用散成一堆看不懂的行。

变异验证:去掉 alive 检查,两条测试转红。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 聚合 —— `Aggregate` 产出三组报告

**Files:**
- Create: `internal/appattr/report.go`
- Create: `internal/appattr/report_test.go`

**Interfaces:**
- Consumes: `PCB`(Task 2)、`ChooseOwner`/`DisplayName`(Task 3)
- Produces:

```go
type Path string
const (PathTunnel Path = "tunnel"; PathDirect Path = "direct"; PathBlocked Path = "blocked")

type ConnRecord struct {
	SrcPort uint16
	Path    Path
	Source  string // route.Reason.Source 的字符串形式
	Rule    string // 用户规则原文;内建列表为空
}

type AppRow struct {
	App         string `json:"app"`          // 空串表示 unknown
	Conns       int    `json:"conns"`
	BytesUp     int64  `json:"bytes_up"`
	BytesDown   int64  `json:"bytes_down"`
	Rules       []string `json:"rules,omitempty"` // 去重后的命中规则,最多 3 条
}

type Group struct {
	Path Path     `json:"path"`
	Rows []AppRow `json:"rows"`
}

type Report struct {
	Groups []Group `json:"groups"`
}

func Aggregate(records []ConnRecord, owners map[uint16]string, bytesUp, bytesDown map[uint16]int64) Report
```

- [ ] **Step 1: 写失败测试**

```go
package appattr

import (
	"reflect"
	"testing"
)

func TestAggregateSplitsOneAppAcrossPaths(t *testing.T) {
	// Chrome 一部分域名直连、一部分走隧道,是常态。压成一行「混合」会把最有用的
	// 那一半信息扔掉 —— 腾讯会议那次要看的恰恰是「它只出现在 tunnel 组里」。
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel, Source: "default"},
		{SrcPort: 2, Path: PathDirect, Source: "user_direct", Rule: "*.qq.com"},
	}
	owners := map[uint16]string{1: "Google Chrome", 2: "Google Chrome"}
	got := Aggregate(records, owners, map[uint16]int64{1: 100, 2: 5}, map[uint16]int64{1: 900, 2: 45})

	want := Report{Groups: []Group{
		{Path: PathTunnel, Rows: []AppRow{{App: "Google Chrome", Conns: 1, BytesUp: 100, BytesDown: 900}}},
		{Path: PathDirect, Rows: []AppRow{{App: "Google Chrome", Conns: 1, BytesUp: 5, BytesDown: 45, Rules: []string{"*.qq.com"}}}},
		{Path: PathBlocked, Rows: nil},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Aggregate =\n%#v\nwant\n%#v", got, want)
	}
}

// 「问不出来是谁」不许摊进已知应用里,也不许丢弃 —— 它在自己所属的那一组里
// 单独成行。unknown 占比高本身就是「这份数据现在不可信」的信号,那是有用的信息。
func TestAggregateKeepsUnknownAsItsOwnRowInsideItsPath(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel, Source: "default"},
		{SrcPort: 2, Path: PathTunnel, Source: "default"},
	}
	owners := map[uint16]string{1: "Slack"} // 2 号端口查不出来
	got := Aggregate(records, owners, nil, nil)

	tunnel := got.Groups[0]
	if len(tunnel.Rows) != 2 {
		t.Fatalf("tunnel 组 %d 行, want 2(Slack + unknown)", len(tunnel.Rows))
	}
	var sawUnknown bool
	for _, r := range tunnel.Rows {
		if r.App == "" && r.Conns == 1 {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatal("unknown 那条被摊进已知应用或被丢弃了")
	}
}

// 三组永远都在,即使为空 —— 消费方按下标取组,组数浮动会让渲染层错位。
func TestAggregateAlwaysEmitsAllThreeGroupsInOrder(t *testing.T) {
	got := Aggregate(nil, nil, nil, nil)
	want := []Path{PathTunnel, PathDirect, PathBlocked}
	if len(got.Groups) != 3 {
		t.Fatalf("组数 = %d, want 3", len(got.Groups))
	}
	for i, p := range want {
		if got.Groups[i].Path != p {
			t.Fatalf("第 %d 组是 %q, want %q", i, got.Groups[i].Path, p)
		}
	}
}

func TestAggregateSortsRowsByBytesThenName(t *testing.T) {
	records := []ConnRecord{
		{SrcPort: 1, Path: PathTunnel}, {SrcPort: 2, Path: PathTunnel}, {SrcPort: 3, Path: PathTunnel},
	}
	owners := map[uint16]string{1: "Aardvark", 2: "Zebra", 3: "Middle"}
	got := Aggregate(records, owners, map[uint16]int64{1: 1, 2: 1000, 3: 500}, nil)
	if got.Groups[0].Rows[0].App != "Zebra" || got.Groups[0].Rows[2].App != "Aardvark" {
		t.Fatalf("排序错了: %#v", got.Groups[0].Rows)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/appattr/ -run TestAggregate -v`
Expected: 编译失败 —— `undefined: Aggregate`

- [ ] **Step 3: 实现**

`internal/appattr/report.go`:

```go
package appattr

import "sort"

type Path string

const (
	PathTunnel  Path = "tunnel"
	PathDirect  Path = "direct"
	PathBlocked Path = "blocked"
)

// orderedPaths 固定组序。**三组永远都在,即使为空** —— 消费方按下标取组,
// 组数浮动会让渲染层错位。
var orderedPaths = [...]Path{PathTunnel, PathDirect, PathBlocked}

// ConnRecord 是数据面记下的一条连接:只有源端口和判定,没有应用身份 ——
// 身份是后台 worker 事后 join 出来的。
type ConnRecord struct {
	SrcPort uint16
	Path    Path
	Source  string
	Rule    string
}

type AppRow struct {
	App       string   `json:"app"` // 空串 = unknown,消费方必须区分对待
	Conns     int      `json:"conns"`
	BytesUp   int64    `json:"bytes_up"`
	BytesDown int64    `json:"bytes_down"`
	Rules     []string `json:"rules,omitempty"`
}

type Group struct {
	Path Path     `json:"path"`
	Rows []AppRow `json:"rows"`
}

type Report struct {
	Groups []Group `json:"groups"`
}

const maxRulesPerRow = 3

// Aggregate 把连接记录、端口→应用名、按端口的字节数折成三组报告。
//
// **同一个应用可以同时出现在多组** —— Chrome 一部分域名直连、一部分走隧道是常态,
// 压成一行「混合」会把最有用的那一半信息扔掉。
func Aggregate(records []ConnRecord, owners map[uint16]string, bytesUp, bytesDown map[uint16]int64) Report {
	type key struct {
		path Path
		app  string
	}
	acc := map[key]*AppRow{}
	seenRule := map[key]map[string]bool{}
	for _, rec := range records {
		k := key{path: rec.Path, app: owners[rec.SrcPort]} // 查不到 → 空串 = unknown
		row := acc[k]
		if row == nil {
			row = &AppRow{App: k.app}
			acc[k] = row
			seenRule[k] = map[string]bool{}
		}
		row.Conns++
		row.BytesUp += bytesUp[rec.SrcPort]
		row.BytesDown += bytesDown[rec.SrcPort]
		if rec.Rule != "" && !seenRule[k][rec.Rule] && len(row.Rules) < maxRulesPerRow {
			seenRule[k][rec.Rule] = true
			row.Rules = append(row.Rules, rec.Rule)
		}
	}

	report := Report{Groups: make([]Group, 0, len(orderedPaths))}
	for _, p := range orderedPaths {
		var rows []AppRow
		for k, row := range acc {
			if k.path == p {
				rows = append(rows, *row)
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			bi := rows[i].BytesUp + rows[i].BytesDown
			bj := rows[j].BytesUp + rows[j].BytesDown
			if bi != bj {
				return bi > bj
			}
			return rows[i].App < rows[j].App
		})
		report.Groups = append(report.Groups, Group{Path: p, Rows: rows})
	}
	return report
}
```

- [ ] **Step 4: 跑测试确认通过并提交**

```bash
go test ./internal/appattr/ -v
bash scripts/verify.sh --quick
git add internal/appattr
git commit -m "feat(appattr): 三组聚合 —— 同一应用可同时出现在多组,unknown 单独成行

Chrome 一部分域名直连、一部分走隧道是常态,压成一行「混合」会把最有用的那一半
信息扔掉:腾讯会议那次要看的恰恰是「它只出现在 tunnel 组里」。

三组(tunnel/direct/blocked)永远都在、顺序固定,即使为空 —— 消费方按下标取组,
组数浮动会让渲染层错位。unknown 在它所属的那一组里单独成行,不摊进已知应用也
不丢弃:unknown 占比高本身就是「这份数据现在不可信」的信号。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 平台原语 `appsource`

**Files:**
- Create: `internal/supervisor/appsource_darwin.go`
- Create: `internal/supervisor/appsource_other.go`
- Create: `internal/supervisor/appsource_test.go`

**Interfaces:**
- Consumes: `appattr.ParsePcbList`、`appattr.ChooseOwner`、`appattr.DisplayName`
- Produces:

```go
type appSource interface {
	// OwnersByPort 现问内核一次,返回 源端口 → 应用显示名。
	// 查不出应用的端口**不出现在 map 里**(调用方据此判 unknown)。
	OwnersByPort() (map[uint16]string, error)
}
func newAppSource() appSource
var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")
```

- [ ] **Step 1: 写失败测试**

`internal/supervisor/appsource_test.go`(**无 build tag** —— 两个平台都要能编)

```go
package supervisor

import (
	"runtime"
	"testing"
)

// 非 darwin 上整条链必须报「不支持」而不是空数据:空 map 会被上层读成
// 「查过了,一个应用都没有」,而那是句自洽的假话。
func TestAppSourceIsUnsupportedOffDarwin(t *testing.T) {
	owners, err := newAppSource().OwnersByPort()
	if runtime.GOOS == "darwin" {
		if err != nil {
			t.Fatalf("darwin 上应能取到端口映射: %v", err)
		}
		if len(owners) == 0 {
			t.Fatal("darwin 上一个端口映射都没有 —— 解析或 sysctl 走飞了")
		}
		return
	}
	if err == nil {
		t.Fatal("非 darwin 必须报错,不许返回空 map 冒充「查过了没有」")
	}
	if owners != nil {
		t.Fatalf("非 darwin 不许返回 map,得到 %v", owners)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/supervisor/ -run TestAppSourceIsUnsupportedOffDarwin -v`
Expected: 编译失败 —— `undefined: newAppSource`

- [ ] **Step 3: 实现 darwin 版**

`internal/supervisor/appsource_darwin.go`:

```go
//go:build darwin

package supervisor

import (
	"bytes"
	"fmt"

	"github.com/getbx/bx/internal/appattr"
	"golang.org/x/sys/unix"
)

type darwinAppSource struct{}

func newAppSource() appSource { return darwinAppSource{} }

// OwnersByPort 读一次 TCP + UDP 的 pcblist,把源端口 join 成应用显示名。
//
// 真机实测(2026-08-19):两张表读+解析共 451µs~1.5ms,产出 ~243 条映射;
// 再解 45 个不同 PID 的进程名约 330µs。所以整个函数可以按秒级频率调用。
//
// **必须以 root 跑** —— kern.procargs2 读 root 进程要权限,非 root 会让所有
// 系统守护进程的名字变成空串。Core 本身就是 root,菜单(uid 501)不行。
func (darwinAppSource) OwnersByPort() (map[uint16]string, error) {
	owners := map[uint16]string{}
	names := map[int32]string{}
	aliveCache := map[int32]bool{}
	alive := func(pid int32) bool {
		if v, ok := aliveCache[pid]; ok {
			return v
		}
		// kill(pid, 0):ESRCH 才是「不存在」。EPERM 说明进程活着但不归我们管
		// (Core 是 root,实际不会遇到),仍算活着 —— 与 Guardian 那边
		// ErrProcessNotRunning 的判据同源:只有明确的 ESRCH 才判死。
		err := unix.Kill(int(pid), 0)
		v := err == nil || err == unix.EPERM
		aliveCache[pid] = v
		return v
	}

	for _, mib := range [...]string{"net.inet.tcp.pcblist_n", "net.inet.udp.pcblist_n"} {
		raw, err := unix.SysctlRaw(mib)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", mib, err)
		}
		pcbs, err := appattr.ParsePcbList(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", mib, err)
		}
		for _, pcb := range pcbs {
			pid, ok := appattr.ChooseOwner(pcb, alive)
			if !ok {
				continue // 查不出的端口**不进 map**,上层据此判 unknown
			}
			name, cached := names[pid]
			if !cached {
				name = appattr.DisplayName(executablePathOf(pid))
				names[pid] = name
			}
			if name != "" {
				owners[pcb.LocalPort] = name
			}
		}
	}
	return owners, nil
}

// executablePathOf 读 kern.procargs2 的第一段(可执行路径)。
// 布局与 guardian/procscan_darwin.go 的 parseProcArgs 相同:
// [4 字节 argc][可执行路径 NUL]…
func executablePathOf(pid int32) string {
	raw, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil || len(raw) < 4 {
		return ""
	}
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return ""
	}
	return string(rest[:end])
}
```

- [ ] **Step 4: 实现非 darwin 桩**

`internal/supervisor/appsource_other.go`:

```go
//go:build !darwin

package supervisor

type unsupportedAppSource struct{}

func newAppSource() appSource { return unsupportedAppSource{} }

// **返回 nil map + 错误,不是空 map。** 空 map 会被上层读成「查过了,一个应用
// 都没有」,而那是句自洽的假话 —— 与 internal/observe 拒绝把「没问过」报成
// 「不归 bx」同源。
func (unsupportedAppSource) OwnersByPort() (map[uint16]string, error) {
	return nil, errAppSourceUnsupported
}
```

接口与哨兵错误放在 `appsource_other.go` 之外的无 tag 文件里 —— 直接加在 `internal/supervisor/apptraffic.go`(Task 6 创建)。本 task 先临时放在 `appsource_test.go` 同目录的新文件 `internal/supervisor/appsource.go`:

```go
package supervisor

import "errors"

// appSource 现问系统「哪个端口属于哪个应用」。**注入点** —— 测试用假的。
type appSource interface {
	OwnersByPort() (map[uint16]string, error)
}

var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/supervisor/ -run TestAppSourceIsUnsupportedOffDarwin -v`
Expected: PASS(darwin 上走真机分支)

- [ ] **Step 6: 确认非 darwin 编得过**

Run: `GOOS=linux go build ./... && GOOS=windows go build ./...`
Expected: 两条都成功

- [ ] **Step 7: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/supervisor
git commit -m "feat(supervisor): appsource —— 纯 Go 读 pcblist 把端口 join 成应用名

CGO_ENABLED=0 下 unix.SysctlRaw 读 net.inet.{tcp,udp}.pcblist_n 即可,不需要
libproc、不需要 fork lsof。真机实测两张表读+解析 451µs~1.5ms、~243 条映射,
再解 45 个 PID 的名字约 330µs,可按秒级频率调用。

必须以 root 跑:kern.procargs2 读 root 进程要权限,非 root 会让所有系统守护
进程的名字变成空串(spike 以 uid 501 跑时,bx Core 自己的名字就是空的)。
所以归因住在 Core,不在菜单。

非 darwin 返回 nil map + 错误而不是空 map —— 空 map 会被上层读成「查过了,
一个应用都没有」,那是句自洽的假话。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: 订阅与记账 —— `internal/supervisor/apptraffic.go`

**Files:**
- Create: `internal/supervisor/apptraffic.go`
- Create: `internal/supervisor/apptraffic_test.go`
- Delete: `internal/supervisor/appsource.go`(内容并入 apptraffic.go)

**Interfaces:**
- Consumes: `appSource`(Task 5)、`appattr.ConnRecord`/`Report`(Task 4)
- Produces:

```go
type AppTraffic struct{ ... }
func NewAppTraffic(src appSource, now func() time.Time) *AppTraffic
func (t *AppTraffic) Subscribe() // 订阅/续期,30 秒 TTL
func (t *AppTraffic) Record(srcPort uint16, path appattr.Path, source, rule string)
func (t *AppTraffic) AddUp(srcPort uint16, n int64)
func (t *AppTraffic) AddDown(srcPort uint16, n int64)
func (t *AppTraffic) Snapshot() (report appattr.Report, subscribed bool, err error)
```

- [ ] **Step 1: 写失败测试**

```go
package supervisor

import (
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

type fakeAppSource struct {
	owners map[uint16]string
	err    error
	calls  int
}

func (f *fakeAppSource) OwnersByPort() (map[uint16]string, error) {
	f.calls++
	return f.owners, f.err
}

// **不变量 2**:未订阅时不做任何归因工作。一条「悄悄开始采集」的回归在性能
// 数字上未必看得出来,所以钉的是「source 一次都没被调用」这个可判定的事实,
// 不是基准 —— 基准不会让 CI 转红。
func TestAppTrafficDoesNothingWhileUnsubscribed(t *testing.T) {
	src := &fakeAppSource{owners: map[uint16]string{7: "Slack"}}
	tr := NewAppTraffic(src, time.Now)

	for i := 0; i < 1000; i++ {
		tr.Record(uint16(i), appattr.PathTunnel, "default", "")
		tr.AddUp(uint16(i), 10)
		tr.AddDown(uint16(i), 20)
	}
	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if subscribed {
		t.Fatal("没订阅却报 subscribed=true")
	}
	if src.calls != 0 {
		t.Fatalf("未订阅时调了 %d 次 appSource —— 应该一次都不调", src.calls)
	}
	for _, g := range report.Groups {
		if len(g.Rows) != 0 {
			t.Fatalf("未订阅却攒下了数据: %#v", g)
		}
	}
}

func TestAppTrafficRecordsAndAggregatesWhileSubscribed(t *testing.T) {
	src := &fakeAppSource{owners: map[uint16]string{7: "Slack", 8: "Google Chrome"}}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	tr.Record(7, appattr.PathTunnel, "default", "")
	tr.AddUp(7, 100)
	tr.AddDown(7, 900)
	tr.Record(8, appattr.PathDirect, "user_direct", "*.qq.com")

	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !subscribed {
		t.Fatal("订阅了却报 subscribed=false")
	}
	tunnel := report.Groups[0]
	if len(tunnel.Rows) != 1 || tunnel.Rows[0].App != "Slack" || tunnel.Rows[0].BytesDown != 900 {
		t.Fatalf("tunnel 组不对: %#v", tunnel.Rows)
	}
	direct := report.Groups[1]
	if len(direct.Rows) != 1 || direct.Rows[0].App != "Google Chrome" {
		t.Fatalf("direct 组不对: %#v", direct.Rows)
	}
}

// 订阅带 TTL:菜单被强杀时没人来退订,而「没人看的时候开销精确为零」是这个
// 设计的隐私前提 —— 不能靠对方守规矩来保证。
func TestAppTrafficSubscriptionExpiresAndClearsBuffers(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	src := &fakeAppSource{owners: map[uint16]string{7: "Slack"}}
	tr := NewAppTraffic(src, clock)
	tr.Subscribe()
	tr.Record(7, appattr.PathTunnel, "default", "")

	now = now.Add(appTrafficTTL + time.Second)
	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if subscribed {
		t.Fatal("TTL 过期后仍报 subscribed=true")
	}
	for _, g := range report.Groups {
		if len(g.Rows) != 0 {
			t.Fatalf("TTL 过期后缓冲没清干净: %#v", g)
		}
	}

	// 过期之后再记的东西也不许被攒下来。
	tr.Record(7, appattr.PathTunnel, "default", "")
	if _, _, _ = tr.Snapshot(); src.calls != 0 {
		t.Fatalf("过期后仍调了 %d 次 appSource", src.calls)
	}
}

// 端口复用:同一个端口上来了新连接,旧连接的残留字节必须清掉,否则会算到
// 新应用头上。这是 spec 里承认的近似,但至少要做这一层缓解。
func TestAppTrafficResetsByteCountersOnPortReuse(t *testing.T) {
	src := &fakeAppSource{owners: map[uint16]string{7: "Slack"}}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	tr.Record(7, appattr.PathTunnel, "default", "")
	tr.AddUp(7, 1000)
	tr.Record(7, appattr.PathDirect, "user_direct", "*.qq.com") // 同端口,新连接
	tr.AddUp(7, 5)

	report, _, _ := tr.Snapshot()
	var total int64
	for _, g := range report.Groups {
		for _, row := range g.Rows {
			total += row.BytesUp
		}
	}
	if total > 100 {
		t.Fatalf("端口复用后残留字节 = %d —— 新连接继承了旧连接的账", total)
	}
}

// appSource 失败要如实上报,不许退化成「一个应用都没有」。
func TestAppTrafficReportsSourceFailureRatherThanEmptyReport(t *testing.T) {
	src := &fakeAppSource{err: errAppSourceUnsupported}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()
	tr.Record(7, appattr.PathTunnel, "default", "")

	if _, _, err := tr.Snapshot(); err == nil {
		t.Fatal("appSource 报错时 Snapshot 必须报错,不许返回空报告冒充「没有应用」")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/supervisor/ -run TestAppTraffic -v`
Expected: 编译失败 —— `undefined: NewAppTraffic`

- [ ] **Step 3: 实现**

`internal/supervisor/apptraffic.go`:

```go
package supervisor

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// appSource 现问系统「哪个端口属于哪个应用」。**注入点** —— 测试用假的。
type appSource interface {
	OwnersByPort() (map[uint16]string, error)
}

var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")

// appTrafficTTL 是订阅的存活期。菜单被强杀、窗口进程崩溃时不会有人来退订,
// 而「没人看的时候开销精确为零」是这个设计的隐私前提 —— 不能靠对方守规矩来保证。
const appTrafficTTL = 30 * time.Second

// appTrafficMaxRecords 是环形缓冲容量。满了就丢最旧的:界面显示的是「此刻的
// 分流构成」,几万条之前的连接对它没有意义,而无界缓冲会在订阅期间无限长。
const appTrafficMaxRecords = 4096

// AppTraffic 按源端口记账,并在有人订阅时才工作。
//
// **热路径只做两件事**:一次 atomic 读判断有没有人在看,以及(有人看时)
// 往环形缓冲写一条记录 / 给两个数组之一加个数。归因(问内核、解进程名)
// 全部发生在 Snapshot 里,不在拨号或转发路径上。
type AppTraffic struct {
	src appSource
	now func() time.Time

	// active 是热路径唯一要读的东西。
	active atomic.Bool

	mu       sync.Mutex
	expires  time.Time
	records  []appattr.ConnRecord
	next     int  // 环形缓冲写指针
	wrapped  bool // 是否已经绕过一圈
	bytesUp  map[uint16]int64
	bytesDn  map[uint16]int64
}

func NewAppTraffic(src appSource, now func() time.Time) *AppTraffic {
	if now == nil {
		now = time.Now
	}
	return &AppTraffic{src: src, now: now}
}

// Subscribe 开启或续期采集。菜单每次拉取都会调它。
func (t *AppTraffic) Subscribe() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active.Load() {
		t.records = make([]appattr.ConnRecord, appTrafficMaxRecords)
		t.next, t.wrapped = 0, false
		t.bytesUp = make(map[uint16]int64)
		t.bytesDn = make(map[uint16]int64)
	}
	t.expires = t.now().Add(appTrafficTTL)
	t.active.Store(true)
}

// expiredLocked 在 TTL 过期时就地停掉采集并清空缓冲。
// 调用者必须持有 t.mu。
func (t *AppTraffic) expiredLocked() bool {
	if !t.active.Load() {
		return true
	}
	if t.now().Before(t.expires) {
		return false
	}
	t.active.Store(false)
	t.records, t.bytesUp, t.bytesDn = nil, nil, nil
	t.next, t.wrapped = 0, false
	return true
}

// Record 记一条连接的判定。数据面调用,**不做任何归因**。
func (t *AppTraffic) Record(srcPort uint16, path appattr.Path, source, rule string) {
	if !t.active.Load() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.expiredLocked() {
		return
	}
	// **端口复用**:同一个端口上来了新连接,把旧连接的字节账清掉,
	// 否则会算到新应用头上。spec 承认这是近似,但这一层缓解要做。
	delete(t.bytesUp, srcPort)
	delete(t.bytesDn, srcPort)
	t.records[t.next] = appattr.ConnRecord{SrcPort: srcPort, Path: path, Source: source, Rule: rule}
	t.next++
	if t.next == len(t.records) {
		t.next, t.wrapped = 0, true
	}
}

func (t *AppTraffic) AddUp(srcPort uint16, n int64)   { t.addBytes(srcPort, n, true) }
func (t *AppTraffic) AddDown(srcPort uint16, n int64) { t.addBytes(srcPort, n, false) }

func (t *AppTraffic) addBytes(srcPort uint16, n int64, up bool) {
	if !t.active.Load() || n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.expiredLocked() {
		return
	}
	if up {
		t.bytesUp[srcPort] += n
	} else {
		t.bytesDn[srcPort] += n
	}
}

// Snapshot 现问一次内核,把攒下的连接记录 join 成按应用的报告。
//
// 三态刻意分开:subscribed=false 是「没人在看」;subscribed=true + err 是
// 「在看但问不出来」;subscribed=true + 空报告是「在看,确实还没有连接」。
func (t *AppTraffic) Snapshot() (appattr.Report, bool, error) {
	t.mu.Lock()
	if t.expiredLocked() {
		t.mu.Unlock()
		return appattr.Aggregate(nil, nil, nil, nil), false, nil
	}
	records := t.liveRecordsLocked()
	up := make(map[uint16]int64, len(t.bytesUp))
	for k, v := range t.bytesUp {
		up[k] = v
	}
	dn := make(map[uint16]int64, len(t.bytesDn))
	for k, v := range t.bytesDn {
		dn[k] = v
	}
	t.mu.Unlock()

	owners, err := t.src.OwnersByPort()
	if err != nil {
		return appattr.Report{}, true, err
	}
	return appattr.Aggregate(records, owners, up, dn), true, nil
}

// liveRecordsLocked 把环形缓冲摊平成时间序。调用者必须持有 t.mu。
func (t *AppTraffic) liveRecordsLocked() []appattr.ConnRecord {
	if !t.wrapped {
		return append([]appattr.ConnRecord(nil), t.records[:t.next]...)
	}
	out := make([]appattr.ConnRecord, 0, len(t.records))
	out = append(out, t.records[t.next:]...)
	return append(out, t.records[:t.next]...)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/supervisor/ -run TestAppTraffic -v`
Expected: 五条全 PASS

- [ ] **Step 5: 删掉 Task 5 的临时文件**

```bash
rm internal/supervisor/appsource.go
go build ./...
```

- [ ] **Step 6: 变异验证「未订阅不干活」**

把 `Record` 开头的 `if !t.active.Load() { return }` 删掉,确认
`TestAppTrafficDoesNothingWhileUnsubscribed` **转红**(它会因 `t.records` 为 nil 而 panic 或断言失败),改回。

- [ ] **Step 7: 竞态检查并提交**

```bash
go test -race ./internal/supervisor/ -run TestAppTraffic
bash scripts/verify.sh --quick
git add internal/supervisor
git commit -m "feat(supervisor): 应用流量记账 —— 订阅式、30 秒 TTL、全内存

热路径只做两件事:一次 atomic 读判断有没有人在看,以及(有人看时)往环形缓冲
写一条记录或给字节账加个数。归因(问内核、解进程名)全在 Snapshot 里,不在
拨号或转发路径上。

订阅带 TTL:菜单被强杀时没人来退订,而「没人看的时候开销精确为零」是这个设计
的隐私前提,不能靠对方守规矩来保证。过期即清空缓冲。

端口复用时清掉该端口的字节账 —— 否则新连接会继承旧连接的量。spec 承认这是
近似值,这是那一层缓解。

三态分开:没人在看 / 在看但问不出来 / 在看且确实没有连接。appSource 报错
绝不退化成空报告。变异验证:去掉未订阅短路,对应测试转红。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: 接上数据面 —— dialer 记判定、engine 记字节

**Files:**
- Modify: `internal/dialer/dialer.go`
- Modify: `internal/tun/engine.go`
- Test: `internal/dialer/dialer_test.go`、`internal/tun/engine_test.go`

**Interfaces:**
- Produces:
  - `dialer.AppRecorder` 接口 + `Dialer.AppRecorder` 字段
  - `tun.ByteAttributor` 接口 + `WithByteAttribution(...)` Option

- [ ] **Step 1: 写失败测试(dialer 侧)**

```go
// 应用归因是**旁观者**:它拿到判定结果,但绝不影响判定。这条测试同时钉住
// 「记了」和「记的是判定实际走的那条路」。
func TestDialerRecordsPathAndRuleForAppAttribution(t *testing.T) {
	rec := &fakeAppRecorder{}
	d := newTestDialer(t) // 复用本文件已有的构造 helper
	d.AppRecorder = rec

	// 命中用户 direct 规则的一次拨号
	_, _ = d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 51234})

	if len(rec.calls) != 1 {
		t.Fatalf("记了 %d 次, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.srcPort != 51234 || got.path != appattr.PathDirect || got.rule != "*.qq.com" {
		t.Fatalf("记的内容不对: %+v", got)
	}
}

// kill-switch 挡掉的连接必须进 blocked 组 —— 它直接回答「为什么这个 App
// 一开 bx 就废了」,而这个问题今天完全没有答案。
func TestDialerRecordsBlockedConnections(t *testing.T) { /* 同构:构造隧道不健康的 Dialer,断言 path == appattr.PathBlocked */ }
```

> `fakeAppRecorder` 与 `newTestDialer` 按本文件既有 helper 的风格写;`fakeAppRecorder` 记录 `{srcPort uint16, path appattr.Path, source, rule string}`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dialer/ -run TestDialerRecords -v`
Expected: 编译失败 —— `d.AppRecorder undefined`

- [ ] **Step 3: 实现 dialer 侧**

在 `internal/dialer/dialer.go` 加接口与字段:

```go
// AppRecorder 收下「这条连接被判成了什么」。**旁观者** —— 它拿到结果,
// 绝不参与判定(route.Meta.SrcPort 不进 Explain,由 route 侧守卫钉住)。
// 可空:未接线或未订阅时,实现方自己短路。
type AppRecorder interface {
	Record(srcPort uint16, path appattr.Path, source, rule string)
}
```

在**每一处**已经调用 `d.Stats.RuleAttempt(source, rule)` 的地方紧邻加一行
`d.recordApp(m.SrcPort, path, source, rule)`,以及 `ErrBlocked` 的每一条返回路径上加
`path = appattr.PathBlocked` 的记录。`recordApp` 是三行的 nil 保护包装:

```go
func (d *Dialer) recordApp(srcPort uint16, path appattr.Path, source, rule string) {
	if d.AppRecorder != nil {
		d.AppRecorder.Record(srcPort, path, source, rule)
	}
}
```

> **实施提醒**:`internal/dialer/dialer.go` 里 `RuleAttempt` 的调用点不止一处
> (TCP 直连/代理、UDP 规则覆盖、UDP proxy、UDP direct-realtime、以及各条
> Block 分支)。**逐个走一遍,别只改看起来主要的那条** —— 漏掉 UDP 分支正好会
> 漏掉腾讯会议的媒体流,也就是这个功能最初的用例。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dialer/ -v`
Expected: 全 PASS

- [ ] **Step 5: 写失败测试(engine 字节侧)**

```go
// 字节按源端口记,并且只在有人订阅时才记。
func TestRelayAttributesBytesToSourcePort(t *testing.T) {
	attr := &fakeByteAttributor{}
	appSide, localSide := tcpPair(t)   // 复用 relay_idle_test.go 的 helper
	upstreamSide, serverSide := tcpPair(t)
	engine := &Engine{idleTimeout: time.Second, bytes: attr}

	go engine.relayWithSrcPort(localSide, upstreamSide, nil, 51234)
	_, _ = serverSide.Write([]byte("hello"))
	// 读到之后断言 attr 收到 (51234, down=5)
	...
}
```

- [ ] **Step 6: 实现 engine 侧**

`internal/tun/engine.go`:

```go
// ByteAttributor 把转发的字节按**应用侧源端口**记账。可空。
// 用源端口而不是连接对象:端口是 uint16,记账端可以用两张定长表做到
// 无锁 O(1);代价是端口复用带来的近似(由记账端在见到新连接时清账缓解)。
type ByteAttributor interface {
	AddUp(srcPort uint16, n int64)
	AddDown(srcPort uint16, n int64)
}

func WithByteAttribution(b ByteAttributor) Option { return func(e *Engine) { e.bytes = b } }
```

`relay` 增加 `srcPort uint16` 参数(调用点在 TCP/UDP handler,那里有 `id`),
在两个 `onWrite` 闭包里各加一次 `e.bytes.AddUp/AddDown(srcPort, n)`(nil 保护)。

- [ ] **Step 7: 在 `run.go` 里接线**

把 `NewAppTraffic` 的实例同时传给 `dialer.AppRecorder` 与 `tun.WithByteAttribution`。
**这一跳要有测试** —— 这个仓库全部的事故都在组装根上。加一条断言:`Run` 构造出来的
`Dialer.AppRecorder` 与 Engine 的 byte attributor 是**同一个** `*AppTraffic` 实例
(不同实例会让连接记录和字节账对不上,而两边各自看起来都正常)。

- [ ] **Step 8: 竞态检查并提交**

```bash
go test -race ./internal/dialer/ ./internal/tun/ ./internal/supervisor/
bash scripts/verify.sh --quick
git add internal/dialer internal/tun internal/supervisor
git commit -m "feat: 数据面接上应用归因(判定 + 按源端口的字节账)

dialer 在每一处已有 RuleAttempt 的地方紧邻记一条 (srcPort, path, source, rule),
包括全部 UDP 分支与 Block 分支 —— 漏掉 UDP 正好会漏掉腾讯会议的媒体流,也就是
这个功能最初的用例;Block 那一组直接回答「为什么这个 App 一开 bx 就废了」。

engine 的 relay 把源端口交给 onWrite 闭包,字节按端口记。用端口而不是连接对象,
是为了让记账端能用定长表做到无锁 O(1)。

run.go 那一跳单独一条测试:dialer 与 engine 必须拿到**同一个** AppTraffic 实例,
不同实例会让连接记录和字节账对不上,而两边各自看起来都正常。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Core `/v0/apps`

**Files:**
- Modify: `internal/supervisor/control.go`
- Modify: `internal/supervisor/control_client.go`
- Test: `internal/supervisor/control_test.go`

**Interfaces:**
- Produces:
  - `GET /v0/apps?subscribe=1` → `{"subscribed":bool,"report":{...},"error":"..."}`
  - `func FetchAppTraffic(sockPath string) (AppTrafficResponse, error)`

- [ ] **Step 1: 写失败测试**

```go
// 「没订阅」「订阅了但问不出来」「订阅了且确实没连接」是三种不同的状态,
// 不许合并成一个空列表 —— 空列表读作「一条都没有」是句自洽的假话。
func TestControlAppsDistinguishesUnsubscribedFromEmpty(t *testing.T) {
	// 未订阅:subscribed=false
	// subscribe=1 之后:subscribed=true 且 groups 恒为三组
	// appSource 报错:HTTP 200 + subscribed=true + error 非空(不是 500,
	//   因为「问不出来」是数据,不是服务端故障)
}
```

- [ ] **Step 2..5**: 跑红 → 按 `handleCapabilities` 的形状实现 handler(GET-only,
  `subscribe=1` 时先 `t.Subscribe()` 再 `Snapshot()`)→ 跑绿 → 写 `FetchAppTraffic` 客户端。

- [ ] **Step 6: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/supervisor
git commit -m "feat(supervisor): Core /v0/apps —— 三态分开,不用空列表冒充

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Guardian `/v1/apps` + `CapabilityApps`

**Files:**
- Create: `internal/guardian/apps.go`
- Modify: `internal/guardian/localapi.go`、`internal/guardian/types.go`、`internal/guardian/client.go`
- Test: `internal/guardian/apps_test.go`

**Interfaces:**
- Consumes: `supervisor.FetchAppTraffic`(Task 8)
- Produces:`CapabilityApps = "apps"`、`GET /v1/apps`、`Client.AppTraffic(ctx)`

- [ ] **Step 1: 写失败测试**

```go
// 与 /v1/rules、/v1/up 同一道门:能关掉保护的人已经能做更坏的事,取一致是要点。
func TestLocalAPIAppsRequiresOwnerPeer(t *testing.T) { /* 非 owner 非 root → 403 */ }

// Capabilities 刻意无 omitempty:键缺席是「这版 Guardian 没有这个概念」的唯一信号。
func TestGuardianCapabilitiesIncludeApps(t *testing.T) {
	caps := GuardianCapabilities()
	for _, c := range caps {
		if c == CapabilityApps {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q,菜单会永久隐藏这个功能: %v", CapabilityApps, caps)
}

// 「没接线」回 501,不回空列表(与 rulesHandler 同源)。
func TestLocalAPIAppsReportsNotWiredRatherThanEmpty(t *testing.T) { /* … */ }
```

- [ ] **Step 2..5**: 跑红 → 照 `rulesHandler` 的形状实现(`authorizeOwnerPeer` →
  socket 路径为空回 501 → GET 转发到 Core)→ 跑绿 → 加进 `GuardianCapabilities()`。

- [ ] **Step 6: 提交**

```bash
bash scripts/verify.sh --quick
git add internal/guardian
git commit -m "feat(guardian): /v1/apps + CapabilityApps,与 /v1/rules 同一道门

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: 菜单纯模型 `AppTrafficModel.swift`

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift`
- Create: `apps/macos/BxMenu/Tests/AppTrafficModelTests.swift`
- Modify: `scripts/test-macos-menu.sh`
- Modify: `internal/cli/cli_test.go`(套件清单守卫)

- [ ] **Step 1: 先把测试文件登记进脚本**

`Tests/` 下的文件**不属于任何 SwiftPM target**,漏登记进 `scripts/test-macos-menu.sh`
就**一次都不跑而 CI 全绿**(2026-08-10 实测:套件数 17→16,脚本照样打印收尾横幅并退 0)。
`internal/cli/cli_test.go` 里已有一条守卫钉着那份清单 —— 把 `AppTrafficModelTests.swift`
同时加进脚本和守卫的期望清单,**先跑一次确认守卫在管用**。

- [ ] **Step 2: 写失败测试**

```swift
// Swift 合成的 Decodable **不用属性默认值**,而服务端对空列表用 omitempty ——
// 「一组行都没有」这种正常状态会让整个界面解码失败。RulesModel 那次就是这么
// 抓到的,所以这里从一开始就手写 init(from:)。
func testDecodesReportWithMissingRowsArray() { … }

// 三组顺序固定,渲染层按顺序摆。
func testGroupsKeepTunnelDirectBlockedOrder() { … }

// 空串 App 是 unknown,必须显示成一句人话而不是空白行。
func testUnknownAppRendersAsExplicitLabel() { … }

// 未订阅 / 问不出来 / 确实没有,三种状态的文案必须不同。
func testThreeEmptyStatesRenderDifferently() { … }
```

- [ ] **Step 3..5**: 跑红(`bash scripts/test-macos-menu.sh`)→ 实现纯模型
  (`struct AppTrafficReport: Decodable` + 手写 `init(from:)` + `func rows() -> [Row]`)→ 跑绿。

- [ ] **Step 6: 提交**

```bash
bash scripts/test-macos-menu.sh
bash scripts/verify.sh --quick
git add apps/macos scripts internal/cli
git commit -m "feat(menu): AppTrafficModel 纯模型 + 手写 Decodable

Swift 合成的 Decodable 不用属性默认值,而服务端对空列表用 omitempty ——
「一组行都没有」这种正常状态会让整个界面解码失败(RulesModel 那次的原样重演),
所以从一开始就手写 init(from:)。

测试文件同时登记进 test-macos-menu.sh 与 cli_test.go 的清单守卫:Tests/ 下的
文件不属于任何 SwiftPM target,漏登记会一次都不跑而 CI 全绿。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: 悬浮窗与接线

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/AppTrafficWindow.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`、`GuardianClient.swift`
- Modify: `internal/cli/cli_test.go`(源码文本守卫)

- [ ] **Step 1: 写守卫测试(Go 侧,因为 main.swift 编不进 Swift 测试套件)**

```go
// 能力门控:旧 Guardian 不认识 /v1/apps 会回 404,而客户端无从区分「这版不支持」
// 与「这版支持但此刻没数据」。**绝不「试着拨一下看看」** —— status watch 那次
// 真机实测过代价:门被绕过时 CPU 常驻 26%~46%、吞吐上千次/秒。
func TestMacMenuGatesAppTrafficOnCapability(t *testing.T) {
	// 读 main.swift,断言打开窗口的那条路径上出现 capabilities 判断,
	// 且判据锚在类型/常量上而不是某个字面拼法(守卫要禁语义,不要禁拼法)。
	// 读不到源码必须 t.Fatal 响亮失败。
}

// 窗口关着就不拨(这个 task 要保住的收益),开着才按需重拉。
func TestMacMenuOnlyFetchesAppTrafficWhileWindowVisible(t *testing.T) { … }
```

- [ ] **Step 2..4**: 跑红 → 实现窗口(`NSWindow(level: .floating)`,照
  `ServersWindow.swift` 的 `show`/`refreshIfVisible`/`isVisible` 形状)+
  `appTrafficFetchInFlight` 守卫(照 `serversFetchInFlight`)+ 菜单项 → 跑绿。

- [ ] **Step 5: 界面上写明字节数是近似值**

窗口底部一行小字:`Byte counts are approximate (ports get reused).`
**不是可选的** —— spec 明写「界面不该把它显示成精确账」。

- [ ] **Step 6: 全量闸门 + 提交**

```bash
swift build --package-path apps/macos/BxMenu
bash scripts/test-macos-menu.sh
bash scripts/verify.sh          # 全量 12 步
git add apps/macos internal/cli
git commit -m "feat(menu): Traffic by App 悬浮窗

NSWindow(level: .floating),用户自己开自己关 —— 不进菜单栏常驻(与此前否掉
「Direct rules: N unreachable」常驻红字同一条判断:常态不是事件,会变墙纸)。
窗口是订阅的载体:开着才采集,关掉就完全停。

能力门控,绝不「试着拨一下看看」:旧 Guardian 回 404,客户端无从区分「不支持」
与「支持但没数据」。status watch 那次真机实测过绕过门的代价:常驻 CPU 26%~46%。

界面上明写字节数是近似值(端口复用)。

Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>"
```

---

## 收尾:文档与真机验收

- [ ] **更新 `CLAUDE.md`**:新增一节记录这个功能、两个字节布局陷阱、`so_e_pid` 必须查活性、以及「归因必须住在 Core(root)」这条实测结论。
- [ ] **真机验收清单**(要人在屏幕前):
  1. 打开悬浮窗,确认三组都在、腾讯会议出现在预期的组里。
  2. 关掉窗口,`sudo lsof -p $(pgrep -f 'bx run') | wc -l` 与 CPU 占用应回落;30 秒后再开,数据从零开始。
  3. 拔掉网线/关 Wi-Fi 制造 kill-switch 阻断,确认 BLOCKED 组出现内容。
  4. 确认 `unknown` 行占比不高(高说明 worker 太慢,那是要修的信号,不是要藏的数字)。
- [ ] **把「真机未验」写进 CLAUDE.md**,直到上面四条跑过。

---

## Self-Review 记录

- **Spec 覆盖**:§一→Task 2/3/4/5;§二→Task 1;§三→Task 6;§四→Task 3;§五→Task 6/7;§六→Task 4/7;§七→Task 8/9/10/11;不变量 1→Task 1 Step 7,2→Task 6 Step 1,3→purity + 无落盘代码,4→本计划不碰 Down/teardown,5→Task 5。
- **类型一致性**:`appattr.Path`/`ConnRecord`/`AppRow`/`Group`/`Report` 在 Task 4 定义,Task 6/7/8 按同名同签名使用;`appSource.OwnersByPort` 在 Task 5 定义,Task 6 消费。
- **已知薄弱处**:Task 8/9 的步骤密度低于 Task 1-7 —— 它们逐字照搬 `handleCapabilities` 与 `rulesHandler` 两个现成形状,实施时**先读那两个函数**再动手。
