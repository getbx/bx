import Foundation

/// 菜单第一行那个开关的**判据**:什么时候显示、显示成开还是关、能不能拨。
/// AppKit 那半(ProtectionSwitchRow.swift)只照着摆。
///
/// 这是控制中心的形态(Wi‑Fi / 蓝牙:一行标签 + 右侧 NSSwitch),取代了
/// 「Turn Off bx」「Start Protection」两个文字项 —— 同一个动作在两个状态里
/// 有两个名字,用户要先读一遍才知道现在是开是关;开关本身就是状态。
enum ProtectionSwitchState: Equatable {
    /// 没什么可拨(没装、没 setup、缺文件):不画开关,保留原来的文字动作。
    case hidden
    case shown(isOn: Bool, enabled: Bool)
}

/// 位置**只来自状态与进行中的动作**,从不来自上一次点击:失败时状态没变,开关
/// 就弹回去,与「Last operation failed」那一行一起把事情说清。
///
/// 进行中:停在**目标**位置、禁用 —— 用户刚拨过去,它就该待在那儿等结果;
/// 弹回原位再跳过去是两次动画,而禁用是为了不让连拨发出第二个请求。
func protectionSwitch(state: MenuStateKind, inFlight: ToggleAction?) -> ProtectionSwitchState {
    if let inFlight {
        return .shown(isOn: inFlight == .turnOn, enabled: false)
    }
    switch state {
    case .connected, .warning:
        return .shown(isOn: true, enabled: true)
    case .offGuardianResponding, .offServiceStopped:
        return .shown(isOn: false, enabled: true)
    case .updateNeeded, .setupNeeded, .missing, .notInstalled:
        return .hidden
    }
}

/// 拨到哪一边就是哪一个动作 —— 与原来两个文字项接的是同两个入口。
func protectionSwitchAction(turnedOn: Bool) -> ToggleAction {
    turnedOn ? .turnOn : .turnOff
}
