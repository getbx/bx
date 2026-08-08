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

        // 周期必须参与相等性判断:仅 form/motion-kind 相同、period 不同的两个样式
        // 不能被判等,否则上面「四态两两不同」那个循环其实什么都没验证到周期。
        let sameFormDifferentPeriod = MenuIconStyle(
            form: protectedStyle.form,
            motion: .breathe(period: idle + 1))
        expect(
            sameFormDifferentPeriod != protectedStyle,
            "呼吸周期不同却被判等,== 丢弃了 period")

        // 需要注意也要呼吸(否则和已关闭只差一个裂缝,余光扫过分不出)
        expect(badStyle.motion != .still, "需要注意必须有动效")

        // 几何:裂缝必须真的穿过盾的中线,否则读起来像造型不像坏了
        expect(shieldOutlinePoints.count >= 5, "盾轮廓点太少")
        let xs = shieldCrackPoints.map(\.x)
        expect(xs.min()! < 8 && xs.max()! > 8, "裂缝必须跨过中线 x=8,实际 x 范围 \(xs.min()!)…\(xs.max()!)")
        let ys = shieldCrackPoints.map(\.y)
        expect(ys.min()! <= 2 && ys.max()! >= 14, "裂缝必须贯穿上下,实际 y 范围 \(ys.min()!)…\(ys.max()!)")

        // 几何:裂缝的每一个顶点都必须落在盾轮廓内部,否则 NSBezierPath 会画出
        // 一根戳出盾外的毛刺(Task 4 直接拿这些点描边)。盾轮廓是凸多边形,用
        // 叉积符号一致性做点在凸多边形内测试。
        for (i, p) in shieldCrackPoints.enumerated() {
            expect(
                isInsideConvexPolygon(p, shieldOutlinePoints),
                "裂缝顶点 \(i) \(p) 落在盾轮廓之外")
        }

        finish()
    }

    static func finish() {
        if failures == 0 { print("MenuIconTests passed") } else { exit(1) }
    }

    /// 判断 `point` 是否落在凸多边形 `polygon`(顶点按序,顺时针或逆时针均可)
    /// 内部或边上:沿各边求叉积,若符号全程一致(或为 0,即在边上)则在内部。
    static func isInsideConvexPolygon(
        _ point: (x: Double, y: Double), _ polygon: [(x: Double, y: Double)]
    ) -> Bool {
        var sign = 0
        for i in polygon.indices {
            let a = polygon[i]
            let b = polygon[(i + 1) % polygon.count]
            let cross = (b.x - a.x) * (point.y - a.y) - (b.y - a.y) * (point.x - a.x)
            if cross > 1e-9 {
                if sign < 0 { return false }
                sign = 1
            } else if cross < -1e-9 {
                if sign > 0 { return false }
                sign = -1
            }
        }
        return true
    }
}
