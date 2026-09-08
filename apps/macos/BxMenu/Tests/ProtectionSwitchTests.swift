import Foundation

@main
struct ProtectionSwitchTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    // 开关只在「有东西可拨」的三个状态出现:开着(connected / warning)拨成关,
    // 关着(off 的两种来路)拨成开。装都没装、还没 setup、缺文件 —— 没什么可拨,
    // 那几个状态保留各自的文字动作(Set Up bx… / Install bx…)。
    static func testSwitchAppearsOnlyWhereThereIsSomethingToToggle() {
        expect(protectionSwitch(state: .connected, inFlight: nil) == .shown(isOn: true, enabled: true), "connected ⇒ 开、可拨")
        expect(protectionSwitch(state: .warning, inFlight: nil) == .shown(isOn: true, enabled: true), "warning ⇒ 保护还开着,开、可拨")
        expect(protectionSwitch(state: .offGuardianResponding, inFlight: nil) == .shown(isOn: false, enabled: true), "off(Guardian 在)⇒ 关、可拨")
        expect(protectionSwitch(state: .offServiceStopped, inFlight: nil) == .shown(isOn: false, enabled: true), "off(服务停了)⇒ 关、可拨")
        for kind: MenuStateKind in [.updateNeeded, .setupNeeded, .missing, .notInstalled] {
            expect(protectionSwitch(state: kind, inFlight: nil) == .hidden, "\(kind) 没什么可拨,不该有开关")
        }
    }

    // 拨动进行中:开关停在**目标**位置、禁用 —— 用户刚拨过去,它就该待在那儿等结果,
    // 弹回原位再跳过去是两次动画。禁用是为了不让连拨发出第二个请求
    // (performToggle 本身也挡,这里是界面那半)。
    static func testInFlightShowsTheTargetPositionDisabled() {
        expect(protectionSwitch(state: .offGuardianResponding, inFlight: .turnOn) == .shown(isOn: true, enabled: false), "正在打开 ⇒ 开、禁用")
        expect(protectionSwitch(state: .connected, inFlight: .turnOff) == .shown(isOn: false, enabled: false), "正在关闭 ⇒ 关、禁用")
        // 进行中压过状态:哪怕状态还没刷新过来,开关也按动作走。
        expect(protectionSwitch(state: .setupNeeded, inFlight: .turnOn) == .shown(isOn: true, enabled: false), "进行中一律显示开关")
    }

    // 开关的位置**永远来自状态**,不来自上一次点击:失败时状态没变,开关就弹回去,
    // 与「Last operation failed」那一行一起说清楚。这条钉的是函数没有记忆。
    static func testPositionComesFromStateNotFromTheClick() {
        _ = protectionSwitch(state: .offGuardianResponding, inFlight: .turnOn)
        expect(protectionSwitch(state: .offGuardianResponding, inFlight: nil) == .shown(isOn: false, enabled: true),
               "打开失败后状态仍是 off,开关必须弹回关")
    }

    // 拨到哪一边就是哪一个动作。
    static func testFlipMapsToTheMatchingAction() {
        expect(protectionSwitchAction(turnedOn: true) == .turnOn, "拨开 ⇒ turnOn")
        expect(protectionSwitchAction(turnedOn: false) == .turnOff, "拨关 ⇒ turnOff")
    }

    static func main() {
        testSwitchAppearsOnlyWhereThereIsSomethingToToggle()
        testInFlightShowsTheTargetPositionDisabled()
        testPositionComesFromStateNotFromTheClick()
        testFlipMapsToTheMatchingAction()
        if failures == 0 {
            print("ProtectionSwitchTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
