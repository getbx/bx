import SwiftUI

/// The bx mark in the navigation bar — product identity, not state (state is the shield).
/// Light and dark come from the asset catalog (the design pack forbids the light mark on dark).
struct BrandTitle: View {
    var body: some View {
        Image("BrandMark")
            .resizable()
            .scaledToFit()
            .frame(height: 22)
            .accessibilityLabel("bx")
    }
}
