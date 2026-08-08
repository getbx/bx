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
