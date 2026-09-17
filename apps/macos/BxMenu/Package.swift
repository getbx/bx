// swift-tools-version: 5.9
import PackageDescription

// **这里刻意没有 test target,也没有 build-tool 插件。**
//
// 2026-08-01 到 2026-09-16 之间这里挂着一个 BxMenuTestPlugin:`swift build` 时
// 它会顺带跑 apps/macos/BxMenu/run-swift-tests.sh。那份脚本是 scripts/test-macos-menu.sh
// 的**第二份拷贝**,而当初的计划书就写着两份编译清单「人工保持一致」——
// 于是它漂了:插件那份停在 7 个套件,真正那份长到了 32 个,且它的 guardian-client
// 套件编 GuardianClient.swift 却没带 2026-08-07 才出现的 GuardianStatus.swift。
//
// 后果不是少跑几个测试,是 **`swift build` 从 2026-08-07 起一直失败**
// (`cannot find 'GuardianStatus' in scope`),连带 verify.sh 那一步与 CI 的
// macos-app job 一起红了五周 —— 而红着的腿不是严格,是等于不存在。
//
// 覆盖没有损失:插件跑的 7 个套件全是 scripts/test-macos-menu.sh 那 32 个的子集,
// 而后者由 verify.sh 与 CI 各自单独跑、且都断言收尾横幅真的打印过。
// 两步的分工写在 verify.sh 与 ci.yml 里:swift build 只编 Sources/,
// Tests/ 下的文件不属于任何 target,由 test-macos-menu.sh 用 swiftc 直编直跑。
let package = Package(
    name: "BxMenu",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "BxMenu", targets: ["BxMenu"])
    ],
    targets: [
        .executableTarget(name: "BxMenu")
    ]
)
