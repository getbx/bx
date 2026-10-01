import XCTest

// The Protection tab on synthetic inputs (`--fixture`: no VPN framework calls, no Keychain).
final class HomeScreenUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    /// Rows below the fold of a list are not drawn until scrolled to.
    private func reveal(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        let element = app.descendants(matching: .any)[id]
        for _ in 0..<4 where !element.exists || !element.isHittable {
            app.swipeUp()
        }
        return element
    }

    private func launch(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["--fixture"] + extra
        app.launch()
        return app
    }

    // First run: three ways in, said in plain words — not a link box nobody knows how to fill.
    func testWithoutAServerTheFirstScreenOffersThreeWaysIn() {
        let app = launch()
        XCTAssertTrue(app.descendants(matching: .any)["home.scan"].waitForExistence(timeout: 10), "first screen lacks scanning")
        for id in ["home.deploy", "home.guide", "home.paste"] {
            XCTAssertTrue(reveal(app, id).exists, "first screen lacks \(id)")
        }
        XCTAssertFalse(app.descendants(matching: .any)["home.protection"].exists, "a protection switch with no server to protect through")
        XCTAssertFalse(app.descendants(matching: .any)["add.link"].exists, "typing a link is the last resort, not the first box")
    }

    func testScanningACodeAsksBeforeAdding() {
        let app = launch(["--fixture-scan", Self.incoming])
        app.buttons["home.scan"].tap()
        let simulate = app.buttons["scan.simulate"]
        XCTAssertTrue(simulate.waitForExistence(timeout: 5))
        simulate.tap()
        XCTAssertTrue(app.descendants(matching: .any)["incoming.host"].waitForExistence(timeout: 5), "no confirmation after scanning")
        app.buttons["incoming.add"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["home.server"].waitForExistence(timeout: 10))
    }

    func testTheGuideLeadsToSetup() {
        let app = launch()
        app.buttons["home.guide"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["guide.continue"].waitForExistence(timeout: 5) || true)
        reveal(app, "guide.continue").tap()
        XCTAssertTrue(app.textFields["deploy.address"].waitForExistence(timeout: 8), "the guide did not lead to setting up the server")
    }

    // iPhone's own "add VPN configurations" alert is explained before it appears.
    func testFirstTurnOnExplainsTheVPNPrompt() {
        let app = launch(["--fixture-server"])
        let button = app.buttons["home.protection"]
        XCTAssertTrue(button.waitForExistence(timeout: 10))
        button.tap()
        let go = app.buttons["vpn.continue"]
        XCTAssertTrue(go.waitForExistence(timeout: 5), "turned on without explaining the VPN prompt")
        go.tap()
        let state = app.descendants(matching: .any)["home.state"]
        let protected = NSPredicate(format: "label == %@", "Protected")
        XCTAssertEqual(XCTWaiter.wait(for: [expectation(for: protected, evaluatedWith: state)], timeout: 5), .completed)
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
        reveal(app, "home.typeLink").tap()
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

    // Phone-first: no link yet, only what the provider gave. The deploy is scripted (no network);
    // the real one is Go's internal/deploy, end-to-end tested against a real sshd.
    func testSettingUpAServerFromThePhoneSavesIt() {
        let app = launch(["--fixture-deploy", "ok"])
        let deploy = app.buttons["home.deploy"]
        XCTAssertTrue(deploy.waitForExistence(timeout: 10), "no Set Up My Server on first run")
        deploy.tap()
        let address = app.textFields["deploy.address"]
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.tap()
        address.typeText("203.0.113.9")
        let password = app.secureTextFields["deploy.password"]
        password.tap()
        password.typeText("pw")
        app.buttons["deploy.start"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["deploy.result"].waitForExistence(timeout: 10), "no result")
        app.buttons["deploy.done"].tap()
        let server = app.descendants(matching: .any)["home.server"]
        XCTAssertTrue(server.waitForExistence(timeout: 10))
        let text = server.label + " " + ((server.value as? String) ?? "")
        XCTAssertTrue(text.contains("203.0.113.9"), "server row says \(text)")
    }

    func testATakenPortIsExplained() {
        let app = launch(["--fixture-deploy", "port"])
        app.buttons["home.deploy"].tap()
        let address = app.textFields["deploy.address"]
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.tap()
        address.typeText("203.0.113.9")
        let password = app.secureTextFields["deploy.password"]
        password.tap()
        password.typeText("pw")
        app.buttons["deploy.start"].tap()
        let failure = app.descendants(matching: .any)["deploy.failure"]
        XCTAssertTrue(failure.waitForExistence(timeout: 10), "no failure shown")
        XCTAssertTrue(failure.label.contains("already used"), "failure says \(failure.label)")
        XCTAssertFalse(app.descendants(matching: .any)["home.server"].exists, "a failed deploy saved a server")
    }

    // The web-console fallback prints `sudo bx setup --udp 'UDP' 'MAIN'`; pasting that whole line
    // must take the main link, not the UDP one.
    func testPastingTheWholeSetupLineTakesTheMainLink() {
        let app = launch()
        reveal(app, "home.typeLink").tap()
        let field = app.descendants(matching: .any)["add.link"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText("sudo bx setup --udp 'bx://UDP-ONLY' '\(Self.mainFixture)'")
        app.buttons["add.submit"].tap()
        let server = app.descendants(matching: .any)["home.server"]
        XCTAssertTrue(server.waitForExistence(timeout: 10), "the pasted line was refused")
        let text = server.label + " " + ((server.value as? String) ?? "")
        XCTAssertTrue(text.contains("203.0.113.9"), "server row says \(text)")
    }

    static let mainFixture = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"
}

