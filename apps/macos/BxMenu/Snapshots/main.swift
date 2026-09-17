import AppKit

// 菜单窗口的**离屏快照**:把真窗口渲染成 PNG,并 dump 出视图树。
//
// 这半边(AppKit)在 CI 里一行测试都盖不到,于是「按钮跑到窗口外面去了」这种
// 缺陷只能靠人盯着屏幕发现 —— 而本仓库的「真机未验」清单里,菜单那几扇窗口
// 一直是最长的一段。2026-09-17 的实测证明它不必如此:
//
//   · NSView.cacheDisplay(in:to:) 不需要窗口上屏就能渲染;
//   · .prohibited 激活策略下 makeKeyAndOrderFront 实测 occlusionState=hidden、
//     app.isActive=false —— 窗口不画到屏幕上,也不抢焦点,可以在人正常工作时跑。
//
// **两种产物,用途不同,别混**:
//   · PNG 给人(和给能读图的 agent)看 —— 布局、截断、对齐、深浅色。
//     它**不适合当闸门**:像素比对换个系统版本字体一变就全红,而一个会偶发红的
//     闸门比没有闸门更糟。
//   · tree.txt 给守卫看 —— 每个子视图的类型、frame、以及在窗口坐标系里的右边界。
//     「控件超出了内容宽度」是确定性判定,不是审美问题。
//
// 走的是**真实路径**:真实 wire JSON → 真实解码 → 真实行构造 → 真实窗口类。
// 另写一份渲染代码就是这个仓库最忌讳的「两份清单」。

let args = CommandLine.arguments
guard args.count >= 3 else {
    FileHandle.standardError.write("用法: snapshot <fixtures 目录> <输出目录>\n".data(using: .utf8)!)
    exit(2)
}
let fixtures = args[1]
let out = args[2]

let app = NSApplication.shared
app.setActivationPolicy(.prohibited)

func fail(_ message: String) -> Never {
    FileHandle.standardError.write("✗ \(message)\n".data(using: .utf8)!)
    exit(1)
}

/// 把窗口内容渲染成 PNG。
func writePNG(_ window: NSWindow, to path: String) {
    guard let content = window.contentView else { fail("窗口没有 contentView") }
    content.layoutSubtreeIfNeeded()
    guard let rep = content.bitmapImageRepForCachingDisplay(in: content.bounds),
          { content.cacheDisplay(in: content.bounds, to: rep); return true }(),
          let png = rep.representation(using: .png, properties: [:])
    else { fail("渲染 \(path) 失败") }
    do { try png.write(to: URL(fileURLWithPath: path)) } catch { fail("写 \(path): \(error)") }
}

/// dump 视图树。**`inWindow.maxX` 是给守卫用的那一列** —— 它是控件右边界在
/// 窗口坐标系里的位置,与窗口内容宽度一比就知道有没有跑到外面去。
func writeTree(_ window: NSWindow, to path: String) {
    guard let content = window.contentView else { fail("窗口没有 contentView") }
    content.layoutSubtreeIfNeeded()
    // **内边距由 dump 自己报出来,守卫不写魔法数字。**
    // 判据是「行活在内边距里面」,而不是「别超过窗口宽度」—— 后者会放过
    // 这次真实抓到的那个缺陷:按钮的 maxX 正好**等于**窗口宽度 420,
    // 贴着右边缘、被滚动条盖住半个,而它确实没有「超过」窗口。
    var rightInset = 0
    func findVerticalStack(_ view: NSView) -> NSStackView? {
        if let s = view as? NSStackView, s.orientation == .vertical, s.edgeInsets.right > 0 {
            return s
        }
        for sub in view.subviews { if let hit = findVerticalStack(sub) { return hit } }
        return nil
    }
    if let stack = findVerticalStack(content) { rightInset = Int(stack.edgeInsets.right) }
    var lines = [
        "contentWidth \(Int(content.bounds.width))",
        "rightInset \(rightInset)",
    ]
    func walk(_ view: NSView, _ depth: Int) {
        let f = view.frame
        // **量的是 alignment rect 的右边界,不是 frame 的。**
        // Auto Layout 定位用的是 alignment rect,而 NSTextField 的 frame 比它每边
        // 大 2pt(焦点环留的位置)。按 frame 量会让每一个标签都"越界 2pt",
        // 于是这条守卫恒红 —— 而恒红的闸门会被下一个人删掉。
        let maxX = view.convert(view.bounds, to: nil).maxX - view.alignmentRectInsets.right
        var label = String(describing: type(of: view))
        if let t = view as? NSTextField { label += " text=\(t.stringValue.debugDescription)" }
        if let b = view as? NSButton { label += " title=\(b.title.debugDescription)" }
        lines.append(String(
            format: "%@%@ w=%.0f h=%.0f maxX=%.0f",
            String(repeating: "  ", count: depth), label, f.width, f.height, maxX))
        for sub in view.subviews { walk(sub, depth + 1) }
    }
    walk(content, 0)
    do {
        try lines.joined(separator: "\n").appending("\n")
            .write(toFile: path, atomically: true, encoding: .utf8)
    } catch { fail("写 \(path): \(error)") }
}

func findButton(_ view: NSView, title: String, id: String) -> NSButton? {
    if let b = view as? NSButton, b.title == title, b.identifier?.rawValue == id { return b }
    for sub in view.subviews { if let hit = findButton(sub, title: title, id: id) { return hit } }
    return nil
}

// —— Routing Rules ——
//
// 覆盖面今天只有这一扇。**加一扇的成本是一份 fixture 加十来行**,
// 而四扇窗口共用同一套布局原语(MenuLayout.swift),所以这一扇量到的
// 「行有没有溢出」对另外三扇有很强的指示性 —— 但那不等于验过了,别把它读成验过了。
do {
    let data: Data
    do { data = try Data(contentsOf: URL(fileURLWithPath: fixtures + "/rules.json")) }
    catch { fail("读不到 rules.json: \(error)") }
    let list: RuleList
    do { list = try JSONDecoder().decode(RuleList.self, from: data) }
    catch { fail("rules.json 解不出 RuleList(wire 形状漂了?): \(error)") }

    let controller = RulesWindowController()
    controller.show(
        rows: ruleGroupRows(from: list, failing: []),
        ruleRows: ruleRows(from: list, failing: [], customOnly: true),
        configPath: list.configPath,
        caveatNote: nil)
    guard let window = NSApp.windows.first(where: { $0.contentView != nil }) else {
        fail("没拿到 Routing Rules 窗口")
    }
    writePNG(window, to: out + "/rules-collapsed.png")
    writeTree(window, to: out + "/rules-collapsed.tree")

    // 展开一组:**「里面有哪些域名」那条路只有点开才量得到**,
    // 而展开会把一行变成十几行,恰恰是最容易把布局撑坏的输入。
    guard let show = findButton(window.contentView!, title: "Show", id: "china-cdn") else {
        fail("找不到 China CDN 的 Show 按钮 —— 展开那条路没接上,或者判据认不出它了")
    }
    show.performClick(nil)
    writePNG(window, to: out + "/rules-expanded.png")
    writeTree(window, to: out + "/rules-expanded.tree")
}

print("macOS menu snapshots written")
