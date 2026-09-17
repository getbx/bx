import AppKit

// 四扇窗口(Rules / Servers / Diagnostics / AppTraffic)此前各自抄了一份
// 「滚动视图 + 翻转文档视图 + 竖直 stack」的组装代码。四份拷贝里有同一个缺陷,
// 而它一直没人看见,因为**这半边 Go 测试一行都盖不到**:
//
//   竖直 stack 用 .leading 对齐时,每一行按**固有宽度**布局 —— 没有任何约束
//   强制它压进容器,于是一行塞不下时不是把副标题截短,而是**整行溢出到窗口
//   外面**,行尾那个按钮就变成用户点不到的东西。
//
// 2026-09-17 用离屏快照量到的原形(规则窗口,默认 420pt 宽):
// `NSButton [Show]` 的 `inWindow.maxX = 420`,正好压在窗口右边缘,
// 而 stack 的右内边距本该是 18 —— 行溢出了整整 18pt,再叠上滚动条盖住的部分,
// 那个按钮在截图上就是半个。
//
// 判据集中在这里而不是四处各修一遍:**唯一需要逐字对齐的东西就该只有一份**
// (同 internal/dirsync、internal/elevate)。

/// 一扇窗口的滚动表格:滚动视图 + 翻转文档视图 + 竖直 stack,约束已经接好。
///
/// **文档视图必须是翻转坐标系**:NSView 默认原点在左下,内容比可视区小时会
/// 沉到窗口底部 —— 看起来像刻意的留白,其实是坐标系。
func makeScrollingStack(
    insets: NSEdgeInsets,
    spacing: CGFloat
) -> (scroll: NSScrollView, stack: NSStackView) {
    let stack = NSStackView()
    stack.orientation = .vertical
    stack.alignment = .leading
    stack.spacing = spacing
    stack.edgeInsets = insets
    stack.translatesAutoresizingMaskIntoConstraints = false

    let scroll = NSScrollView()
    scroll.hasVerticalScroller = true
    scroll.drawsBackground = false
    scroll.translatesAutoresizingMaskIntoConstraints = false

    let clip = MenuFlippedView()
    clip.translatesAutoresizingMaskIntoConstraints = false
    clip.addSubview(stack)
    scroll.documentView = clip

    NSLayoutConstraint.activate([
        stack.leadingAnchor.constraint(equalTo: clip.leadingAnchor),
        stack.trailingAnchor.constraint(equalTo: clip.trailingAnchor),
        stack.topAnchor.constraint(equalTo: clip.topAnchor),
        stack.bottomAnchor.constraint(equalTo: clip.bottomAnchor),
        // **按 clip view 的宽度,不是 scroll 自己的宽度。** legacy 滚动条会占
        // 掉一条宽度,按 scroll 算出来的文档视图会宽出那一条,右边缘被压在
        // 滚动条底下。overlay 滚动条下两者相等,所以这条改动在今天多数机器上
        // 不改变任何东西 —— 它防的是「用户把滚动条设成常显」那一档。
        clip.widthAnchor.constraint(equalTo: scroll.contentView.widthAnchor),
    ])
    return (scroll, stack)
}

/// 翻转坐标系的文档视图。
final class MenuFlippedView: NSView {
    override var isFlipped: Bool { true }
}

extension NSStackView {
    /// 把一行加进表里,**并把它的宽度钉到容器的内容宽**。
    ///
    /// 这一句是上面那段注释里描述的缺陷的唯一修法:钉了宽度,压缩才会触发 ——
    /// 一行塞不下时副标题按 `.byTruncatingTail` 截短,而行尾的按钮留在窗口里。
    /// 不钉的话 AppKit 会老老实实按固有宽度把它画到窗口外面去,**而且不报错**。
    ///
    /// 用它代替裸的 `addArrangedSubview`:判据放在调用点上就会漂,
    /// 而漏掉一处的表现是那一行的行尾控件点不到,别处都正常。
    func addFullWidthRow(_ view: NSView) {
        addArrangedSubview(view)
        view.widthAnchor.constraint(
            equalTo: widthAnchor,
            constant: -(edgeInsets.left + edgeInsets.right)
        ).isActive = true
    }
}

/// 把一个视图钉满容器四边。**挂载与组装分开**:Diagnostics 是两页各建一份滚动表、
/// 之后才挂到各自的 NSTabViewItem 宿主视图上,组装那一步够不着容器。
func pinToEdges(_ view: NSView, in container: NSView) {
    container.addSubview(view)
    NSLayoutConstraint.activate([
        view.leadingAnchor.constraint(equalTo: container.leadingAnchor),
        view.trailingAnchor.constraint(equalTo: container.trailingAnchor),
        view.topAnchor.constraint(equalTo: container.topAnchor),
        view.bottomAnchor.constraint(equalTo: container.bottomAnchor),
    ])
}
