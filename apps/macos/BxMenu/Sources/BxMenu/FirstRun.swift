import Foundation

/// 首次启动时该主动做什么。
enum FirstRunAction: Equatable {
    /// 什么都不做 —— 装好了、配好了,或者已经问过一次了。
    case none
    /// 还没装:引导安装。
    case offerInstall
    /// 装好了但没配连接:引导设置。
    case offerSetup
}

/// firstRunAction 决定应用刚起来时要不要主动开口。
///
/// **为什么需要它:** 现在双击 Bx.app 之后什么都不发生 —— 菜单栏冒出一个小图标,
/// 而没有任何东西告诉用户下一步该点哪里。一个从 dmg 里拖进来的普通用户,
/// 到这里就卡住了;他不知道要去点菜单里的 "Install bx..."。
///
/// **两条规则的不对称是有理由的:**
///
///   - **没装 → 每次都问。** 这个状态只可能来自用户**手动打开**了这个 app:
///     登录项只有装好之后才写(ensureLoginItemIfCanonical 只在 /Applications 下动手),
///     所以走到这里就意味着他刚刚双击了它,那一刻正是他等着被告知下一步的时候。
///
///   - **装了没配 → 只问一次。** 这个状态下 app 是登录项,每次开机都会起来。
///     每次登录都弹一个框是骚扰;而用户拒绝之后菜单里那一项一直都在,不会失去入口。
func firstRunAction(state: MenuStateKind, alreadyOfferedSetup: Bool) -> FirstRunAction {
    switch state {
    case .notInstalled, .missing:
        return .offerInstall
    case .setupNeeded:
        return alreadyOfferedSetup ? .none : .offerSetup
    default:
        // 已经在用了(连着、需注意、关着、要更新)—— 主动开口只会打断他。
        return .none
    }
}
