import SwiftUI

// The protection shield — the same shape as the Mac menu-bar icon and the Windows tray, so a person
// who knows one knows all three. The coordinates below are the Mac's (MenuIcon.swift), copied
// point for point; TestIOSShieldMatchesTheMacOSOutline keeps the copies identical.
//
// The form carries the state, not the color (the Mac's rule): filled = protected, hollow = off,
// dashed = connecting, cracked = something is wrong. Color only reinforces it, so the four stay
// apart in grayscale, for color-blind people, and with Reduce Motion on.

/// Shield outline, 16×16, y down: apex → upper right → lower right → bottom tip → lower left → upper left.
let shieldOutlinePoints: [(x: Double, y: Double)] = [
    (8, 1.5), (14, 3.35), (14, 8), (11.6, 13.4), (8, 15.15), (4.4, 13.4), (2, 8), (2, 3.35),
]

/// The crack: a zigzag from apex to tip across the center line.
let shieldCrackPoints: [(x: Double, y: Double)] = [
    (8, 1.5), (6.85, 5.1), (8.95, 7.3), (7.05, 10.4), (8.45, 12.5), (8, 15.15),
]

enum ShieldForm: Equatable {
    case filled, hollow, dashed, cracked
}

/// A polyline through 16×16 points, scaled into `rect` (drawn inside the unit's 1.5…15.15 band).
private func shieldPolygon(_ points: [(x: Double, y: Double)], in rect: CGRect, close: Bool = true) -> Path {
    let scale = min(rect.width, rect.height) / 16
    let dx = rect.midX - 8 * scale
    let dy = rect.midY - 8.3 * scale
    var path = Path()
    for (i, p) in points.enumerated() {
        let q = CGPoint(x: dx + p.x * scale, y: dy + p.y * scale)
        if i == 0 { path.move(to: q) } else { path.addLine(to: q) }
    }
    if close { path.closeSubpath() }
    return path
}

struct ShieldOutline: Shape {
    func path(in rect: CGRect) -> Path { shieldPolygon(shieldOutlinePoints, in: rect) }
}

/// One half of the cracked shield: down the crack, then back up its own side (the Mac's order —
/// any other order self-intersects into a bow tie).
struct ShieldHalf: Shape {
    let left: Bool
    func path(in rect: CGRect) -> Path {
        let side = left ? Array(shieldOutlinePoints[5...]) : Array(shieldOutlinePoints[1...3].reversed())
        return shieldPolygon(shieldCrackPoints + side, in: rect)
    }
}

struct ShieldMark: View {
    let form: ShieldForm
    var tint: Color = .accentColor
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var pulsing = false

    var body: some View {
        GeometryReader { geo in
            let unit = min(geo.size.width, geo.size.height) / 16
            ZStack {
                switch form {
                case .filled:
                    ShieldOutline().fill(tint)
                case .hollow:
                    ShieldOutline().stroke(tint, style: StrokeStyle(lineWidth: unit * 1.1, lineJoin: .round))
                case .dashed:
                    ShieldOutline()
                        .stroke(tint, style: StrokeStyle(lineWidth: unit * 1.2, lineJoin: .round, dash: [unit * 2.4, unit * 1.9]))
                        .opacity(pulsing ? 0.45 : 1)
                case .cracked:
                    ShieldHalf(left: true).fill(tint).offset(x: -unit * 0.65, y: unit * 0.15)
                    ShieldHalf(left: false).fill(tint).offset(x: unit * 0.65, y: -unit * 0.3)
                }
            }
        }
        .aspectRatio(1, contentMode: .fit)
        .accessibilityHidden(true)
        .onAppear { startPulse() }
        .onChange(of: form) { _ in startPulse() }
    }

    // Connecting pulses (the person is waiting, so the screen keeps talking); every other form is still.
    private func startPulse() {
        pulsing = false
        guard form == .dashed, !reduceMotion else { return }
        withAnimation(.easeInOut(duration: 0.75).repeatForever(autoreverses: true)) { pulsing = true }
    }
}
