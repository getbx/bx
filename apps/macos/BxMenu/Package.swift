// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "BxMenu",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "BxMenu", targets: ["BxMenu"])
    ],
    targets: [
        .executableTarget(name: "BxMenu"),
        .testTarget(
            name: "BxMenuTests",
            path: "SwiftPMTests",
            plugins: ["BxMenuTestPlugin"]
        ),
        .plugin(
            name: "BxMenuTestPlugin",
            capability: .buildTool(),
            path: "Plugins/BxMenuTestPlugin"
        ),
    ]
)
