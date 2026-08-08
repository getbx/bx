# 菜单栏阶段② 实施计划:图标四形态 + 呼吸 + 头部 + 数据行

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把菜单栏图标改成靠轮廓区分四态并带两种呼吸,把菜单改成「头部判决 + 开关」加一组用户真正会瞄的数据行。

**Architecture:** 所有可判定的东西进新的纯逻辑文件(`MenuIcon.swift` 决定形态与呼吸参数、
`MenuRows.swift` 由状态生成有序数据行、`MenuCadence.swift` 决定轮询间隔),`main.swift` 只做
绘制与接线。这是阶段①已经确立的分工:`main.swift` 是 AppKit,`scripts/test-macos-menu.sh`
编不了它,所以任何带规则的东西留在里面就等于没有测试。

**Tech Stack:** Swift + AppKit(`apps/macos/BxMenu`)· 测试用 `scripts/test-macos-menu.sh`
(`swiftc` 直编直跑,非 XCTest)· Go 侧只加读源码文本的守卫测试(`internal/cli/cli_test.go`)

## Global Constraints

- **形态承担信息,颜色只作可选加强。** 去掉颜色后四态必须仍然可分。判据不得依赖颜色。
- **三态,不是两态。** 数据行的标记只有三种:`ok` / `bad` / `unknown`。**「未观测」不计入
  异常计数,也不得让图标裂开。** 把「没问出来」压成失败正是 `internal/observe` 要消灭的谎。
- **观测不覆盖信念。** 头部判决按 bx 相信的状态渲染;实测不符时另起说明行,判决那句话不改口。
- **「退出 bx」在所有状态恒在。** 这修的是已记录的缺陷(今天 `quitBxActionTitle` 只出现在
  `.connected`/`.warning` 两个分支)。
- **阶段②只上今天有数据的行。** 出口 IP/位置、IPv6 泄漏实测、WebRTC 属阶段③;它们在阶段②
  以 `unknown`(「未观测」)占位,数据到位后自然点亮——这正是三态设计的直接好处。
- `main.swift` **不新增可判定逻辑**。它只负责:把形态画出来、按参数跑呼吸、把行渲染出来。
- 不得削弱任何既有断言。阶段①有多条经变异验证的测试。
- **绝不运行 `bx`、`sudo bx`、`launchctl`、`networksetup`、`route`**,不得启动或安装 bx。
- **绝不运行 `git stash`、`git reset --hard`、`git checkout -- .`**(仓库有一条无关的既有 stash 必须存活)。
- 每个任务结束前跑:`(cd apps/macos/BxMenu && swift build)`、`bash scripts/test-macos-menu.sh`、
  `go build ./... && go vet ./... && go test ./... -count=1`,全绿才提交。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`,直接提交 `master`。

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `Sources/BxMenu/MenuIcon.swift` | 四形态选择 + 盾形几何 + 呼吸参数(纯) | **新建** |
| `Sources/BxMenu/MenuRows.swift` | 由状态生成有序数据行与异常计数(纯) | **新建** |
| `Sources/BxMenu/MenuCadence.swift` | 轮询间隔(纯) | **新建** |
| `Tests/MenuIconTests.swift` / `MenuRowsTests.swift` / `MenuCadenceTests.swift` | 对应测试 | **新建** |
| `scripts/test-macos-menu.sh` | 测试运行器 | 改:注册三个新套件 |
| `Sources/BxMenu/main.swift` | 绘制 + 接线 | 改 |
| `internal/cli/cli_test.go` | 读源码文本的守卫 | 改:加「退出恒在」与「按开合调频」两条 |

现有 12 个 `run_test` 套件不得扰动。

---

### Task 1: 图标四形态与呼吸参数(纯逻辑)

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/MenuIcon.swift`
- Create: `apps/macos/BxMenu/Tests/MenuIconTests.swift`
- Modify: `scripts/test-macos-menu.sh`

**Interfaces:**
- Consumes: 无
- Produces:
  - `enum MenuIconForm { case filled, hollow, cracked }`
  - `enum MenuIconMotion: Equatable { case still; case breathe(period: Double); case pulse(period: Double) }`
  - `struct MenuIconStyle: Equatable { let form: MenuIconForm; let motion: MenuIconMotion }`
  - `func menuIconStyle(state: MenuIconState) -> MenuIconStyle`
  - `enum MenuIconState { case protected, off, transitioning, attention }`
  - `let shieldOutlinePoints: [(x: Double, y: Double)]` 与 `let shieldCrackPoints: [(x: Double, y: Double)]`
    ——16×16 坐标系,供 `main.swift` 构造 `NSBezierPath`

**关键背景:** 判据不得依赖颜色。四态靠 `form`(填充轻重/裂开)加 `motion`(静止/慢呼吸/脉冲)
区分,**两个维度各自单独都足以区分至少两组**,合起来四态互不相同。稳态呼吸周期 4 秒、
过渡态 1.5 秒,这个差距要大到用眼睛能分辨快慢,不能取 3 秒和 2 秒。

- [ ] **Step 1: 写失败测试**

新建 `apps/macos/BxMenu/Tests/MenuIconTests.swift`:

```swift
import Foundation

@main
struct MenuIconTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        let protectedStyle = menuIconStyle(state: .protected)
        let offStyle = menuIconStyle(state: .off)
        let busyStyle = menuIconStyle(state: .transitioning)
        let badStyle = menuIconStyle(state: .attention)

        // 四态必须两两不同 —— 这是「不靠颜色也能分辨」的全部依据
        let all = [protectedStyle, offStyle, busyStyle, badStyle]
        for i in all.indices {
            for j in all.indices where j > i {
                expect(all[i] != all[j], "第 \(i) 态与第 \(j) 态样式相同,去掉颜色后无法区分")
            }
        }

        expect(protectedStyle.form == .filled, "已保护应为实心盾")
        expect(offStyle.form == .hollow, "已关闭应为空心盾")
        expect(badStyle.form == .cracked, "需要注意应为裂开的盾")

        // 已关闭必须完全静止:它是唯一「什么都没在发生」的状态
        expect(offStyle.motion == .still, "已关闭不得有动画,实际 \(offStyle.motion)")

        // 两种呼吸的快慢差距必须一眼可辨
        guard case .breathe(let idle) = protectedStyle.motion else {
            expect(false, "已保护应为慢呼吸,实际 \(protectedStyle.motion)"); finish(); return
        }
        guard case .pulse(let busy) = busyStyle.motion else {
            expect(false, "切换中应为脉冲,实际 \(busyStyle.motion)"); finish(); return
        }
        expect(idle >= 3.5, "稳态呼吸要足够慢才不分心,实际 \(idle) 秒")
        expect(idle >= busy * 2, "两种呼吸的周期差距要一眼可辨,实际 \(idle) vs \(busy)")

        // 需要注意也要呼吸(否则和已关闭只差一个裂缝,余光扫过分不出)
        expect(badStyle.motion != .still, "需要注意必须有动效")

        // 几何:裂缝必须真的穿过盾的中线,否则读起来像造型不像坏了
        expect(shieldOutlinePoints.count >= 5, "盾轮廓点太少")
        let xs = shieldCrackPoints.map(\.x)
        expect(xs.min()! < 8 && xs.max()! > 8, "裂缝必须跨过中线 x=8,实际 x 范围 \(xs.min()!)…\(xs.max()!)")
        let ys = shieldCrackPoints.map(\.y)
        expect(ys.min()! <= 2 && ys.max()! >= 14, "裂缝必须贯穿上下,实际 y 范围 \(ys.min()!)…\(ys.max()!)")

        finish()
    }

    static func finish() {
        if failures == 0 { print("MenuIconTests passed") } else { exit(1) }
    }
}
```

- [ ] **Step 2: 注册并确认失败**

在 `scripts/test-macos-menu.sh` 末尾 `echo "macOS menu tests passed"` **之前**追加:

```bash
run_test menu-icon \
  "$MENU/Sources/BxMenu/MenuIcon.swift" \
  "$MENU/Tests/MenuIconTests.swift"
```

Run: `bash scripts/test-macos-menu.sh`
Expected: 在 `menu-icon` 处失败,报 `MenuIcon.swift` 不存在。

- [ ] **Step 3: 实现**

新建 `apps/macos/BxMenu/Sources/BxMenu/MenuIcon.swift`:

```swift
import Foundation

/// 菜单栏图标要表达的四种状态。
///
/// 与 `BxState` 不是一一对应:菜单有八种状态,但图标只需要区分「保护中 / 没开 /
/// 正在切换 / 出问题了」这四类——菜单栏是余光扫过的地方,分得太细等于分不出。
enum MenuIconState {
    case protected
    case off
    case transitioning
    case attention
}

/// 轮廓形态。**这是主要的信息载体**,颜色只作可选加强。
enum MenuIconForm {
    /// 实心盾:重、压得住,一眼看出「在保护」
    case filled
    /// 空心盾:细描边,视觉上退到背景里
    case hollow
    /// 沿中线裂开的盾:轮廓本身破了
    case cracked
}

enum MenuIconMotion: Equatable {
    case still
    /// 极慢呼吸,只动透明度。用途是瞟一眼确认它活着,不是吸引注意。
    case breathe(period: Double)
    /// 明显脉冲。用户在等的时候界面必须持续说话。
    case pulse(period: Double)
}

struct MenuIconStyle: Equatable {
    let form: MenuIconForm
    let motion: MenuIconMotion
}

extension MenuIconForm: Equatable {}

/// 稳态呼吸周期。取 4 秒是为了「注意不到但确实在动」——低于 3 秒就开始分心。
let menuIconIdlePeriod: Double = 4
/// 过渡态脉冲周期。与稳态差 2.7 倍,快慢一眼可辨。
let menuIconBusyPeriod: Double = 1.5

func menuIconStyle(state: MenuIconState) -> MenuIconStyle {
    switch state {
    case .protected:
        return MenuIconStyle(form: .filled, motion: .breathe(period: menuIconIdlePeriod))
    case .off:
        // 唯一完全静止的状态:什么都没在发生,图标就不该动
        return MenuIconStyle(form: .hollow, motion: .still)
    case .transitioning:
        return MenuIconStyle(form: .filled, motion: .pulse(period: menuIconBusyPeriod))
    case .attention:
        // 也呼吸:只靠一道裂缝与「已关闭」区分,余光扫过太弱
        return MenuIconStyle(form: .cracked, motion: .breathe(period: menuIconIdlePeriod))
    }
}

/// 盾形轮廓,16×16 坐标系,y 向下(与 NSBezierPath 翻转后一致)。
/// 顺序即描边顺序:顶点 → 右上 → 右下弧 → 底尖 → 左下弧 → 左上。
let shieldOutlinePoints: [(x: Double, y: Double)] = [
    (8, 1.5), (14, 3.35), (14, 8), (11.6, 13.4), (8, 15.15), (4.4, 13.4), (2, 8), (2, 3.35),
]

/// 裂缝:一条锯齿状的折线,从盾顶贯到盾底,横跨中线 x=8。
///
/// 走中线而非边缘是刻意的:边缘咬一个口会被读成造型,**裂穿中线只可能是「坏了」**。
/// 锯齿只用三折——段数越多,16pt 下越容易糊成一条直线。
let shieldCrackPoints: [(x: Double, y: Double)] = [
    (8, 1.5), (6.85, 5.1), (8.95, 7.3), (7.05, 10.4), (8.45, 12.5), (8, 15.15),
]
```

- [ ] **Step 4: 跑测试**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全绿,含 `MenuIconTests passed`。

- [ ] **Step 5: 变异验证**

把 `menuIconStyle(.attention)` 的 form 改成 `.filled`,跑 `bash scripts/test-macos-menu.sh`
Expected: FAIL(与 `.transitioning` 撞样式)。确认后改回。

再把 `menuIconBusyPeriod` 改成 `2.5`,跑
Expected: FAIL(周期差距不足 2 倍)。确认后改回。

- [ ] **Step 6: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/MenuIcon.swift \
        apps/macos/BxMenu/Tests/MenuIconTests.swift scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
feat(menu): 图标四形态与呼吸参数

形态承担信息,颜色只作可选加强 —— 去掉颜色后四态仍须两两可分,由测试钉住。
裂缝走中线而非边缘:边缘咬一口会被读成造型,裂穿中线只可能是「坏了」。
稳态 4 秒、过渡态 1.5 秒,差距要一眼可辨快慢。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 数据行模型(纯逻辑,三态)

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift`
- Create: `apps/macos/BxMenu/Tests/MenuRowsTests.swift`
- Modify: `scripts/test-macos-menu.sh`

**Interfaces:**
- Consumes: `BxReport`(既有,`StatusReport.swift`)
- Produces:
  - `enum MenuRowMark { case ok, bad, unknown }`
  - `struct MenuRow: Equatable { let label: String; let value: String; let mark: MenuRowMark }`
  - `struct MenuRowSet: Equatable { let rows: [MenuRow]; let anomalyCount: Int }`
  - `func menuRows(report: BxReport?, dns: String?) -> MenuRowSet`

**关键背景(实施者必读,以下均已核对过代码):**

1. 阶段②**只上今天有数据的行**。出口 IP/位置、IPv6 泄漏实测、WebRTC 属阶段③,在这里必须以
   `.unknown` +「未观测」呈现,**且不得计入 `anomalyCount`**。「没问出来」不是「有问题」——
   把它算成异常会让图标裂开,而实际上一切正常。

2. **`BxReport` 定义了自定义 `init(from decoder:)`,因此 Swift 不合成 memberwise init**
   ——测试**无法**直接 `BxReport(tunnelHealthy:…)` 构造它,必须用 `JSONDecoder` 从 JSON
   字面量解码。下面的测试就是这么写的。

3. **`BxReport` 目前没有 `server` 与 `transport` 字段,但 `bx status --json` 有**
   (`internal/stats/render.go:10` 的 `Report` 里 `Server string json:"server"`、
   `Transport string json:"transport,omitempty"`,经 `clientStatusReport` 嵌入导出)。
   本任务需要给 `BxReport` **补上这两个字段**(`decodeIfPresent`,可选),否则「线路」行没有来源。

4. `udpMode` 的类型是 **`String?`** 不是 `String`。

- [ ] **Step 1: 写失败测试**

新建 `apps/macos/BxMenu/Tests/MenuRowsTests.swift`:

```swift
import Foundation

@main
struct MenuRowsTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func row(_ set: MenuRowSet, _ label: String) -> MenuRow? {
        set.rows.first { $0.label == label }
    }

    /// BxReport 有自定义 init(from:),Swift 因此不合成 memberwise init ——
    /// 只能从 JSON 解码。这反而是好事:测试吃的是真实的 `bx status --json` 形状。
    static func decode(_ json: String) -> BxReport {
        try! JSONDecoder().decode(BxReport.self, from: Data(json.utf8))
    }

    static func main() {
        let healthy = decode("""
        {"tunnel_healthy":true,"latency_ms":390,"active":47,
         "server":"vps","transport":"reality@vps","udp_mode":"hysteria2"}
        """)
        let set = menuRows(report: healthy, dns: "127.0.0.1")

        // 阶段②能上的行
        expect(row(set, "线路")?.value.contains("reality") == true,
               "线路行应显示传输,实际 \(String(describing: row(set, "线路")))")
        expect(row(set, "延迟")?.value == "390 ms",
               "延迟行,实际 \(String(describing: row(set, "延迟")?.value))")
        expect(row(set, "UDP 中继")?.value == "hysteria2",
               "UDP 中继行,实际 \(String(describing: row(set, "UDP 中继")?.value))")
        expect(row(set, "DNS")?.mark == .ok, "DNS 已知时应为 ok")

        // 阶段③的行:必须在场、必须是 unknown、必须不算异常
        for pending in ["出口位置", "IPv6 泄漏", "WebRTC"] {
            guard let r = row(set, pending) else {
                expect(false, "\(pending) 行必须在场(阶段③点亮),现在缺席"); continue
            }
            expect(r.mark == .unknown, "\(pending) 在阶段②必须是 unknown,实际 \(r.mark)")
            expect(r.value == "未观测", "\(pending) 的占位文案应为「未观测」,实际 \(r.value)")
        }
        expect(set.anomalyCount == 0,
               "全部正常 + 三行未观测时异常数必须为 0,实际 \(set.anomalyCount) —— 未观测不是异常")

        // 隧道不健康是真异常
        let unhealthy = decode("""
        {"tunnel_healthy":false,"latency_ms":0,"active":0,
         "server":"vps","transport":"reality@vps","udp_mode":"hysteria2"}
        """)
        let bad = menuRows(report: unhealthy, dns: "127.0.0.1")
        expect(bad.anomalyCount >= 1, "隧道不健康必须计入异常,实际 \(bad.anomalyCount)")
        expect(bad.rows.contains { $0.mark == .bad }, "必须有一行标记为 bad")

        // DNS 未知不是异常,只是未观测
        let noDNS = menuRows(report: healthy, dns: nil)
        expect(row(noDNS, "DNS")?.mark == .unknown, "DNS 取不到时应为 unknown 而非 bad")
        expect(noDNS.anomalyCount == 0, "DNS 未观测不得计入异常,实际 \(noDNS.anomalyCount)")

        // 完全没有报告(bx 没跑)时不得崩,也不得谎报正常
        let none = menuRows(report: nil, dns: nil)
        expect(none.rows.allSatisfy { $0.mark == .unknown },
               "没有报告时所有行都应是 unknown")
        expect(none.anomalyCount == 0, "没有报告不等于有异常")

        if failures == 0 { print("MenuRowsTests passed") } else { exit(1) }
    }
}
```

- [ ] **Step 2: 注册并确认失败**

`scripts/test-macos-menu.sh` 在 `echo "macOS menu tests passed"` 之前追加:

```bash
run_test menu-rows \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/StatusReport.swift" \
  "$MENU/Sources/BxMenu/MenuRows.swift" \
  "$MENU/Tests/MenuRowsTests.swift"
```

Run: `bash scripts/test-macos-menu.sh`
Expected: 在 `menu-rows` 处失败。

- [ ] **Step 3: 实现**

**先改 `StatusReport.swift`**:给 `BxReport` 补两个字段(三处都要改——属性、`CodingKeys`、
`init(from:)`,漏一处编译就过不了):

```swift
    let server: String?
    let transport: String?
```
```swift
        case server, transport
```
```swift
        server = try container.decodeIfPresent(String.self, forKey: .server)
        transport = try container.decodeIfPresent(String.self, forKey: .transport)
```

然后新建 `apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift`:

```swift
import Foundation

/// 行标记。**三态,不是两态。**
///
/// `unknown` 是「没问出来」,与 `bad`(问了,答案是坏的)必须分开:把前者压成后者,
/// 就是重新制造 internal/observe 专门要消灭的那个谎 —— 也会让图标无缘无故裂开。
enum MenuRowMark: Equatable {
    case ok
    case bad
    case unknown
}

struct MenuRow: Equatable {
    let label: String
    let value: String
    let mark: MenuRowMark
}

struct MenuRowSet: Equatable {
    let rows: [MenuRow]
    /// 仅统计 `.bad`。`.unknown` 不计入 —— 未观测不是异常。
    let anomalyCount: Int
}

/// 阶段③才有数据的行的占位文案。刻意不是空字符串:留白会被读成「没这回事」,
/// 而「未观测」如实说明我们没问过。
private let notObserved = "未观测"

func menuRows(report: BxReport?, dns: String?) -> MenuRowSet {
    var rows: [MenuRow] = []

    // ── 阶段③:数据尚未接入,占位但在场 ──
    // 在场是有意的:用户能看见 bx 打算回答哪些问题,以及哪些还没答上。
    rows.append(MenuRow(label: "出口位置", value: notObserved, mark: .unknown))

    if let report {
        let line = [report.transport, report.server]
            .compactMap { $0 }.first { !$0.isEmpty }
        rows.append(line.map { MenuRow(label: "线路", value: $0, mark: .ok) }
            ?? MenuRow(label: "线路", value: notObserved, mark: .unknown))
        rows.append(MenuRow(
            label: "延迟",
            value: report.tunnelHealthy ? "\(report.latencyMS) ms" : "隧道不健康",
            mark: report.tunnelHealthy ? .ok : .bad))
    } else {
        rows.append(MenuRow(label: "线路", value: notObserved, mark: .unknown))
        rows.append(MenuRow(label: "延迟", value: notObserved, mark: .unknown))
    }

    if let dns, !dns.isEmpty {
        rows.append(MenuRow(label: "DNS", value: dns, mark: .ok))
    } else {
        rows.append(MenuRow(label: "DNS", value: notObserved, mark: .unknown))
    }

    rows.append(MenuRow(label: "IPv6 泄漏", value: notObserved, mark: .unknown))
    rows.append(MenuRow(label: "WebRTC", value: notObserved, mark: .unknown))

    if let mode = report?.udpMode, !mode.isEmpty {
        rows.append(MenuRow(label: "UDP 中继", value: mode, mark: .ok))
    } else {
        rows.append(MenuRow(label: "UDP 中继", value: notObserved, mark: .unknown))
    }

    return MenuRowSet(rows: rows, anomalyCount: rows.filter { $0.mark == .bad }.count)
}
```

- [ ] **Step 4: 跑测试**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全绿,含 `MenuRowsTests passed`。

- [ ] **Step 5: 变异验证**

把 `anomalyCount` 改成 `rows.filter { $0.mark != .ok }.count`(即把 unknown 也算成异常),跑
Expected: FAIL(「未观测不是异常」那条转红)。确认后改回。

- [ ] **Step 6: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift \
        apps/macos/BxMenu/Tests/MenuRowsTests.swift scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
feat(menu): 数据行模型,三态且未观测不计异常

阶段③才有数据的三行(出口位置/IPv6 泄漏/WebRTC)在场但标 unknown ——
在场是有意的,用户能看见 bx 打算回答哪些问题、哪些还没答上;不计入异常
则是因为「没问出来」不是「有问题」,否则图标会无缘无故裂开。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 轮询调频 + 退出入口恒在

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/MenuCadence.swift`
- Create: `apps/macos/BxMenu/Tests/MenuCadenceTests.swift`
- Modify: `scripts/test-macos-menu.sh`
- Modify: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func menuPollInterval(menuOpen: Bool) -> TimeInterval`、
  `let menuPollOpenSeconds: TimeInterval`、`let menuPollClosedSeconds: TimeInterval`

**关键背景:** 今天菜单**每 5 秒 spawn 两个进程**(`bx --version` + `bx status --json`),
而 macOS 上 `bx status --json` 已包含完整观测(两次 `route -n get` + 一次 `networksetup` +
控制 socket 往返),观测本身封顶 5 秒——几乎满占空比,且**没人看的时候照样在跑**。

- [ ] **Step 1: 写失败测试**

新建 `apps/macos/BxMenu/Tests/MenuCadenceTests.swift`:

```swift
import Foundation

@main
struct MenuCadenceTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        let open = menuPollInterval(menuOpen: true)
        let closed = menuPollInterval(menuOpen: false)

        expect(open < closed, "菜单打开时必须刷得更勤,实际 open=\(open) closed=\(closed)")
        expect(closed >= 20, "菜单关着时只有图标要更新,间隔应显著放宽,实际 \(closed) 秒")

        // status --json 在 macOS 上整轮观测封顶 5 秒;间隔不得低于它,
        // 否则上一次还没回来下一次就发起了,等于常驻满占空比。
        expect(open >= 2, "打开时的间隔不得低于 2 秒,实际 \(open) 秒")

        if failures == 0 { print("MenuCadenceTests passed") } else { exit(1) }
    }
}
```

- [ ] **Step 2: 注册并确认失败**

`scripts/test-macos-menu.sh` 追加:

```bash
run_test menu-cadence \
  "$MENU/Sources/BxMenu/MenuCadence.swift" \
  "$MENU/Tests/MenuCadenceTests.swift"
```

Run: `bash scripts/test-macos-menu.sh` → 在 `menu-cadence` 处失败。

- [ ] **Step 3: 实现**

新建 `apps/macos/BxMenu/Sources/BxMenu/MenuCadence.swift`:

```swift
import Foundation

/// 菜单打开时的刷新间隔。用户正在看,数据要新鲜。
///
/// 不低于 2 秒是硬下限:macOS 上 `bx status --json` 会跑完整观测
/// (两次 `route -n get` + 一次 `networksetup` + 控制 socket 往返),整轮封顶 5 秒。
/// 间隔比它还短就会出现上一次未回、下一次已发起。
let menuPollOpenSeconds: TimeInterval = 2

/// 菜单关着时的刷新间隔。此时只有菜单栏图标需要更新,没人在读数据行。
///
/// 原实现无论有没有人看都固定 5 秒 spawn 两个进程,是纯浪费。
let menuPollClosedSeconds: TimeInterval = 30

func menuPollInterval(menuOpen: Bool) -> TimeInterval {
    menuOpen ? menuPollOpenSeconds : menuPollClosedSeconds
}
```

- [ ] **Step 4: 跑测试**

Run: `bash scripts/test-macos-menu.sh` → 全绿,含 `MenuCadenceTests passed`。

- [ ] **Step 5: 加两条 Go 侧源码守卫**

`main.swift` 编不进测试脚本,故这两条不变量只能读源码文本来守。在
`internal/cli/cli_test.go` 末尾追加(参考同文件既有的 `TestMacMenu…` 系列写法,
它们用 `os.ReadFile` 读 `apps/macos/BxMenu/Sources/BxMenu/main.swift`):

```go
// 退出入口必须在所有状态都有。此前 quitBxActionTitle 只出现在 .connected/.warning
// 两个分支,其余状态下菜单没有任何退出入口,用户只能等下次登录随 launchd 清场。
func TestMacMenuQuitActionPresentInEveryState(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func rebuildMenu()")
	if !ok {
		t.Fatal("找不到 rebuildMenu 的函数体")
	}
	// 无条件加一次:出现多次说明它又被塞回各个 state 分支里了
	if got := strings.Count(body, "quitBxActionTitle"); got != 1 {
		t.Fatalf("退出项应在 rebuildMenu 里无条件加一次,实际出现 %d 次", got)
	}
}

// 轮询必须按菜单开合调频,不能再固定 5 秒。
func TestMacMenuPollsOnCadence(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "menuPollInterval(menuOpen:") {
		t.Fatal("刷新间隔必须来自 menuPollInterval,不得再硬编码")
	}
	if strings.Contains(text, "withTimeInterval: 5, repeats: true") {
		t.Fatal("固定 5 秒的轮询定时器仍在,调频没有生效")
	}
}
```

**已核对:** `swiftFunctionBody(source, signature string) (string, bool)` 确实存在于
`internal/cli/cli_test.go:608`;同文件既有守卫读 main.swift 的写法就是上面那个
`os.ReadFile(filepath.Join("..", "..", …))`。`rebuildMenu` 的真实签名带 `private`,
签名字符串必须与源码逐字一致,否则 `swiftFunctionBody` 找不到。

- [ ] **Step 6: 跑 Go 测试确认这两条**先红(main.swift 尚未改)

Run: `go test ./internal/cli -run 'TestMacMenuQuitActionPresentInEveryState|TestMacMenuPollsOnCadence' -count=1`
Expected: 两条都 FAIL。

- [ ] **Step 7: 提交(此时 Go 侧两条仍红,由 Task 4 转绿)**

不提交红的测试。**把 Step 5 写的两条 Go 测试留在工作区,与 Task 4 一起提交。**
本任务只提交 Swift 侧:

```bash
git add apps/macos/BxMenu/Sources/BxMenu/MenuCadence.swift \
        apps/macos/BxMenu/Tests/MenuCadenceTests.swift scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
feat(menu): 轮询按菜单开合调频

原实现不论有没有人看都固定 5 秒 spawn 两个进程,而 macOS 上
`bx status --json` 已包含完整观测(两次 route -n get + 一次 networksetup +
控制 socket 往返)、整轮封顶 5 秒 —— 几乎满占空比。现改为打开 2 秒、
关闭 30 秒。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: main.swift 接线——画形态、跑呼吸、渲染行、退出恒在

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `internal/cli/cli_test.go`(提交 Task 3 Step 5 留下的两条测试)

**Interfaces:**
- Consumes: `menuIconStyle(state:)`、`MenuIconForm`、`MenuIconMotion`、
  `shieldOutlinePoints`、`shieldCrackPoints`(Task 1);`menuRows(report:dns:)`、
  `MenuRow`、`MenuRowMark`、`MenuRowSet`(Task 2);`menuPollInterval(menuOpen:)`(Task 3)
- Produces: 无(终点任务)

**关键背景:** 本任务**不新增可判定逻辑**,只做绘制与接线;判定全在 Task 1–3 的纯函数里。
`main.swift` 编不进测试脚本,验收靠 `swift build` + Task 3 那两条读源码文本的 Go 守卫 + 真机。

现有 `compactStatusImage(for:)`(`main.swift:232`)画的是 SF Symbol 盾 + 角上一个彩色圆点,
`isTemplate = false`。本任务改为按 `MenuIconForm` 用 `NSBezierPath` 画三种轮廓;
**颜色保留但降为可选加强**,去掉颜色后仍须靠形态可分。

- [ ] **Step 1: 按形态画图标**

把 `compactStatusImage(for:)` 替换为:

```swift
    private func shieldPath(closed: Bool) -> NSBezierPath {
        let path = NSBezierPath()
        for (index, point) in shieldOutlinePoints.enumerated() {
            let p = NSPoint(x: point.x, y: 16 - point.y)   // 翻 y:数据是 y 向下
            if index == 0 { path.move(to: p) } else { path.line(to: p) }
        }
        if closed { path.close() }
        return path
    }

    /// 裂开的盾:同一条锯齿裂缝把盾切成两半,两半再各自错开一点——
    /// 读起来是「已经滑开了」,不是「画了一条线」。
    private func crackedShieldPaths() -> (left: NSBezierPath, right: NSBezierPath) {
        func half(outlineRange: [(x: Double, y: Double)]) -> NSBezierPath {
            let path = NSBezierPath()
            var points = outlineRange
            points.append(contentsOf: shieldCrackPoints.reversed())
            for (index, point) in points.enumerated() {
                let p = NSPoint(x: point.x, y: 16 - point.y)
                if index == 0 { path.move(to: p) } else { path.line(to: p) }
            }
            path.close()
            return path
        }
        // 轮廓点顺序:顶点 → 右上 → 右下弧 → 底尖 → 左下弧 → 左上
        let right = Array(shieldOutlinePoints[0...4])          // 顶点走右侧到底尖
        let left = [shieldOutlinePoints[0]] + Array(shieldOutlinePoints[5...]) + [shieldOutlinePoints[4]]
        return (half(outlineRange: left), half(outlineRange: right))
    }

    private func compactStatusImage(for style: MenuIconStyle, tint: NSColor) -> NSImage {
        let image = NSImage(size: NSSize(width: 18, height: 18))
        image.lockFocus()
        defer { image.unlockFocus() }
        tint.setFill()
        tint.setStroke()
        switch style.form {
        case .filled:
            shieldPath(closed: true).fill()
        case .hollow:
            let path = shieldPath(closed: true)
            path.lineWidth = 1.35
            NSColor(white: 0, alpha: 0).setFill()
            path.stroke()
        case .cracked:
            let halves = crackedShieldPaths()
            var shiftLeft = AffineTransform(translationByX: -0.65, byY: 0.15)
            var shiftRight = AffineTransform(translationByX: 0.65, byY: -0.3)
            halves.left.transform(using: shiftLeft)
            halves.right.transform(using: shiftRight)
            halves.left.fill()
            halves.right.fill()
        }
        image.isTemplate = false
        return image
    }
```

- [ ] **Step 2: 跑呼吸**

在 `MenuController` 属性区加:

```swift
    private var breathTimer: Timer?
    private var breathPhase: Double = 0
```

加两个方法:

```swift
    /// 呼吸靠周期性调 button.alphaValue 实现。
    /// 不用 CABasicAnimation:状态项按钮的图层由 AppKit 托管,直接动 alpha
    /// 简单且在图标被替换时不会残留动画。
    private func applyBreathing(_ motion: MenuIconMotion) {
        breathTimer?.invalidate()
        breathTimer = nil
        guard let button = statusItem.button else { return }
        let period: Double
        let floorAlpha: Double
        switch motion {
        case .still:
            button.alphaValue = 1
            return
        case .breathe(let p):
            period = p
            floorAlpha = 0.45          // 稳态:幅度小到不盯着看注意不到
        case .pulse(let p):
            period = p
            floorAlpha = 0.35          // 过渡态:更明显,用户在等
        }
        let step = 0.05
        breathTimer = Timer.scheduledTimer(withTimeInterval: step, repeats: true) { [weak self] _ in
            guard let self, let button = self.statusItem.button else { return }
            self.breathPhase += step
            let t = (self.breathPhase.truncatingRemainder(dividingBy: period)) / period
            // 余弦让两端停留久一点,读起来像呼吸而不是闪
            let eased = (1 - cos(t * 2 * Double.pi)) / 2
            button.alphaValue = floorAlpha + (1 - floorAlpha) * (1 - eased)
        }
    }
```

- [ ] **Step 3: `updateIcon` 改用形态**

```swift
    private func updateIcon() {
        guard let button = statusItem.button else { return }
        let style = menuIconStyle(state: menuIconStateNow())
        button.image = compactStatusImage(for: style, tint: iconTint())
        button.imagePosition = .imageOnly
        button.title = ""
        button.toolTip = tooltipText()
        applyBreathing(style.motion)
    }

    /// 把菜单的八个状态收敛到图标的四态。
    private func menuIconStateNow() -> MenuIconState {
        if toggleInFlight != nil { return .transitioning }
        if recoverySnapshot != nil { return .transitioning }
        switch state {
        case .connected:
            return menuRowsNow().anomalyCount > 0 ? .attention : .protected
        case .warning, .updateNeeded:
            return .attention
        case .off, .setupNeeded, .missing, .notInstalled:
            return .off
        }
    }
```

**`BxState` 的真实定义**(已核对,`main.swift`):

```swift
enum BxState {
    case connected(BxReport, version: String, dns: String?)
    case warning(String, version: String?)
    case updateNeeded(String, version: String?)
    case setupNeeded(String)
    case missing(String)
    case notInstalled(bundleVersion: String?)
    case off
}
```

注意 `.off` **没有关联值**,`.notInstalled` 的标签是 `bundleVersion:`。上面的 switch
按 case 名匹配即可(不取关联值时无需写模式),Swift 会强制穷尽。

```swift

    /// 颜色只作可选加强:去掉它四态仍靠形态可分(见 MenuIconTests)。
    private func iconTint() -> NSColor {
        switch menuIconStateNow() {
        case .protected: return .systemGreen
        case .attention: return .systemYellow
        case .transitioning, .off: return .secondaryLabelColor
        }
    }
```

**注意:** `state` 的 case 名以 `main.swift` 里 `BxState` 的真实定义为准,
上面若有出入以真实定义为准补齐,不得漏掉任何 case(Swift 会强制)。

- [ ] **Step 4: 渲染数据行,并把退出项提到无条件**

在 `rebuildMenu()` 的 `.connected` / `.warning` 分支里,把原来那一串 `addInfo` 换成:

```swift
            for row in menuRowsNow().rows {
                let suffix: String
                switch row.mark {
                case .ok: suffix = ""
                case .bad: suffix = "  ✗"
                case .unknown: suffix = ""
                }
                menu.addInfo(row.label, row.value + suffix)
            }
```

并加辅助:

```swift
    private func menuRowsNow() -> MenuRowSet {
        switch state {
        case .connected(let report, _, let dns):
            return menuRows(report: report, dns: dns)
        default:
            return menuRows(report: nil, dns: nil)
        }
    }
```

然后把 `rebuildMenu()` 里各 `state` 分支中的
`menu.addAction(quitBxActionTitle, …)` **全部删掉**,改为在函数末尾
`statusItem.menu = menu` **之前**无条件加一次:

```swift
        menu.addItem(.separator())
        menu.addAction(quitBxActionTitle, symbol: "power", target: self, action: #selector(quitBx))
```

- [ ] **Step 5: 轮询调频**

把 `applicationDidFinishLaunching` 里那个
`Timer.scheduledTimer(withTimeInterval: 5, repeats: true)` 换成按开合调频的定时器,
并在菜单打开/关闭时重建它:

```swift
    private func rescheduleRefreshTimer(menuOpen: Bool) {
        timer?.invalidate()
        timer = Timer.scheduledTimer(
            withTimeInterval: menuPollInterval(menuOpen: menuOpen), repeats: true
        ) { [weak self] _ in
            self?.refresh()
        }
    }
```

启动时调 `rescheduleRefreshTimer(menuOpen: false)`。实现 `NSMenuDelegate` 的
`menuWillOpen` / `menuDidClose`,分别调 `rescheduleRefreshTimer(menuOpen: true/false)`,
并在 `menuWillOpen` 里先 `refresh()` 一次(打开的瞬间数据要是新的)。
记得给 `menu.delegate = self` 并让类声明 `NSMenuDelegate`。

- [ ] **Step 6: 编译与全量验证**

Run:
```bash
(cd apps/macos/BxMenu && swift build) && \
bash scripts/test-macos-menu.sh && \
go build ./... && go vet ./... && go test ./... -count=1
```
Expected: 全绿,含 Task 3 Step 5 那两条 Go 守卫由红转绿。

- [ ] **Step 7: 变异验证两条守卫**

把 Step 4 的无条件退出项挪回 `.connected` 分支里,跑
`go test ./internal/cli -run TestMacMenuQuitActionPresentInEveryState -count=1`
Expected: FAIL。确认后改回。

把 Step 5 的定时器改回 `withTimeInterval: 5, repeats: true`,跑
`go test ./internal/cli -run TestMacMenuPollsOnCadence -count=1`
Expected: FAIL。确认后改回。

- [ ] **Step 8: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/main.swift internal/cli/cli_test.go
git commit -m "$(cat <<'EOF'
feat(menu): 图标改按轮廓区分四态,菜单换成数据行,退出入口恒在

图标不再是「SF Symbol 盾 + 角上彩色圆点」,改为按 MenuIconForm 画实心/
空心/裂开三种轮廓并跑对应呼吸;颜色保留但降为可选加强,去掉后四态仍可分。

菜单的信息行换成 menuRows 生成的三态数据行,阶段③才有数据的三行以「未观测」
在场且不计异常。退出项从各 state 分支提到无条件加一次 —— 此前 .off/
.setupNeeded 等状态下菜单没有任何退出入口。

轮询改为打开 2 秒、关闭 30 秒。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 真机验收(由用户执行)

1. 菜单栏图标:保护中=实心盾且极慢呼吸;关闭=空心盾且完全静止;切换中=脉冲;
   出问题=盾从中间裂开。**把系统调成浅色和深色各看一次**,确认四态在两种菜单栏上都可分。
2. 用 macOS 的「显示器 → 颜色滤镜 → 灰度」打开灰度模式,确认**去掉颜色后四态仍可分**。
3. 打开菜单:数据行显示线路/延迟/DNS/UDP 中继;出口位置、IPv6 泄漏、WebRTC 显示「未观测」。
4. 每个状态下菜单都能找到「退出 bx」。
5. 菜单关着时用 `sudo fs_usage -w -f exec | grep bx` 之类确认刷新变稀疏(30 秒一次)。

## 自查

**Spec 覆盖:** 图标四形态 → Task 1;两种呼吸 → Task 1 + 4;数据行与三态 → Task 2;
退出恒在 → Task 3(守卫)+ Task 4(实现);轮询调频 → Task 3 + 4。
头部「判决 + 出口 IP + 开关」中的**出口 IP 属阶段③**,阶段②头部只有判决与开关
(开关已在阶段①落地),出口位置以数据行占位。

**占位符:** 无 TBD/TODO;每个代码步骤都给了可粘贴的完整代码。两处显式要求实施者
以真实定义为准(`BxReport` 字段名、`BxState` 的 case 集合),那是防止本文档的猜测
覆盖代码事实,不是占位。

**类型一致:** `MenuIconStyle`/`MenuIconForm`/`MenuIconMotion`(Task 1)在 Task 4 使用;
`MenuRowSet.rows`/`.anomalyCount`(Task 2)在 Task 4 使用;`menuPollInterval(menuOpen:)`
(Task 3)在 Task 4 使用,且 Task 3 的 Go 守卫正是查这个符号名。
