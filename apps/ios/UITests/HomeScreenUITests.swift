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

    // Rule sync: the Rules row says where the phone's rules come from, and never implies the
    // Mac's rules are here before they arrived.
    func testRulesRowSaysDefaultsUntilTheMacsRulesArrive() {
        let app = launch(["--fixture-server"])
        let row = app.descendants(matching: .any)["home.rules"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        let text = row.label + " " + ((row.value as? String) ?? "")
        XCTAssertTrue(text.contains("bx defaults"), "rules row says \(text)")
    }

    func testRulesRowSaysFromYourMacOnceSynced() {
        let app = launch(["--fixture-server", "--fixture-synced"])
        let row = app.descendants(matching: .any)["home.rules"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        let text = row.label + " " + ((row.value as? String) ?? "")
        XCTAssertTrue(text.contains("From your Mac"), "rules row says \(text)")
    }

    // The status word follows the real state, and the one button offers the opposite action.
    func testTheStatusSaysWhetherThisPhoneIsProtected() {
        let off = launch(["--fixture-server"])
        let state = off.descendants(matching: .any)["home.state"]
        XCTAssertTrue(state.waitForExistence(timeout: 10), "no status word")
        XCTAssertEqual(state.label, "Not protected")
        XCTAssertEqual(off.buttons["home.protection"].label, "Turn On Protection")

        let on = launch(["--fixture-server", "--fixture-on"])
        let onState = on.descendants(matching: .any)["home.state"]
        XCTAssertTrue(onState.waitForExistence(timeout: 10))
        let protected = NSPredicate(format: "label == %@", "Protected")
        XCTAssertEqual(XCTWaiter.wait(for: [expectation(for: protected, evaluatedWith: onState)], timeout: 5), .completed, "status says \(onState.label)")
        XCTAssertEqual(on.buttons["home.protection"].label, "Turn Off")
    }

    // A bx:// link opened from the Camera's QR scan (or anywhere else) must be confirmed on screen,
    // with the address, before anything changes — any web page can open a bx:// URL.
    static let incoming = "bx://eyJ2IjoxLCJ0cmFuc3BvcnQiOiJyZWFsaXR5IiwibGluayI6InZsZXNzOi8vMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1QDIwMy4wLjExMy40NDo0NDM_c2VjdXJpdHk9cmVhbGl0eVx1MDAyNnNuaT13d3cuY2xvdWRmbGFyZS5jb21cdTAwMjZwYms9QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQVx1MDAyNnNpZD1hYmNkXHUwMDI2ZnA9Y2hyb21lXHUwMDI2Zmxvdz14dGxzLXJwcngtdmlzaW9uIn0"

    func testAnOpenedLinkIsAddedOnlyAfterConfirmingTheAddress() {
        let app = launch(["--fixture-open", Self.incoming])
        let prompt = app.descendants(matching: .any)["incoming.host"]
        XCTAssertTrue(prompt.waitForExistence(timeout: 10), "no confirmation for an opened link")
        let shown = prompt.label + " " + ((prompt.value as? String) ?? "")
        XCTAssertTrue(shown.contains("203.0.113.44"), "the confirmation does not name the server: \(shown)")
        XCTAssertFalse(app.descendants(matching: .any)["home.server"].exists, "the server was added before confirming")
        app.buttons["incoming.add"].tap()
        let server = app.descendants(matching: .any)["home.server"]
        XCTAssertTrue(server.waitForExistence(timeout: 10))
        let text = server.label + " " + ((server.value as? String) ?? "")
        XCTAssertTrue(text.contains("203.0.113.44"), "server row says \(text)")
    }

    func testAnOpenedLinkSaysItReplacesTheCurrentServer() {
        let app = launch(["--fixture-server", "--fixture-open", Self.incoming])
        let note = app.descendants(matching: .any)["incoming.replaces"]
        XCTAssertTrue(note.waitForExistence(timeout: 10), "no replace notice")
        XCTAssertTrue(note.label.contains("203.0.113.9"), "replace notice says \(note.label)")
        app.buttons["incoming.cancel"].tap()
        let server = app.descendants(matching: .any)["home.server"]
        XCTAssertTrue(server.waitForExistence(timeout: 10))
        let text = server.label + " " + ((server.value as? String) ?? "")
        XCTAssertTrue(text.contains("203.0.113.9"), "cancel changed the server: \(text)")
    }
}

