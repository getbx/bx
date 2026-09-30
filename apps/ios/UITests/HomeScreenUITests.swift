import XCTest

// The Protection tab on synthetic inputs (`--fixture`: no VPN framework calls, no Keychain).
final class HomeScreenUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    private func launch(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["--fixture"] + extra
        app.launch()
        return app
    }

    func testWithoutAServerTheOnlyThingOnScreenIsHowToAddOne() {
        let app = launch()
        XCTAssertTrue(app.descendants(matching: .any)["add.link"].waitForExistence(timeout: 10), "no link field on first run")
        XCTAssertFalse(app.descendants(matching: .any)["home.protection"].exists, "a protection switch with no server to protect through")
    }

    func testWithAServerTheSwitchAndTheAddressAreShown() {
        let app = launch(["--fixture-server"])
        let toggle = app.descendants(matching: .any)["home.protection"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), "no protection switch")
        let server = app.descendants(matching: .any)["home.server"]
        let text = server.label + " " + ((server.value as? String) ?? "")
        XCTAssertTrue(text.contains("203.0.113.9"), "server row says \(text)")
    }

    // A link the phone cannot run must be refused in words the person can act on, not
    // accepted into a config that can never connect.
    func testAnUnsupportedLinkIsRefusedByName() {
        let app = launch()
        let field = app.descendants(matching: .any)["add.link"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText("brook://server?server=203.0.113.9%3A9999&password=x")
        app.buttons["add.submit"].tap()
        let problem = app.descendants(matching: .any)["add.problem"]
        XCTAssertTrue(problem.waitForExistence(timeout: 5), "no refusal shown")
        XCTAssertTrue(problem.label.contains("not supported on iPhone"), "refusal says \(problem.label)")
        XCTAssertFalse(app.descendants(matching: .any)["home.protection"].exists, "the unsupported link was accepted")
    }
}
