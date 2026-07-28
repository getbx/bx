import Foundation
import PackagePlugin

@main
struct BxMenuTestPlugin: BuildToolPlugin {
    func createBuildCommands(context: PluginContext, target: Target) async throws -> [Command] {
        let package = context.package.directory
        let script = package.appending("run-swift-tests.sh")
        let inputs = [
            script,
            package.appending("Sources/BxMenu/StatusIndicator.swift"),
            package.appending("Sources/BxMenu/StatusPresentation.swift"),
            package.appending("Sources/BxMenu/UpdatePresentation.swift"),
            package.appending("Sources/BxMenu/RecoveryPresentation.swift"),
            package.appending("Sources/BxMenu/GuardianClient.swift"),
            package.appending("Tests/StatusIndicatorTests.swift"),
            package.appending("Tests/StatusPresentationTests.swift"),
            package.appending("Tests/UpdatePresentationTests.swift"),
            package.appending("Tests/RecoveryPresentationTests.swift"),
            package.appending("Tests/GuardianClientTests.swift"),
        ]
        let marker = context.pluginWorkDirectory.appending("tests-passed")
        return [
            .buildCommand(
                displayName: "Run BxMenu Swift tests",
                executable: Path("/bin/bash"),
                arguments: [script.string, marker.string],
                inputFiles: inputs,
                outputFiles: [marker]
            ),
        ]
    }
}
