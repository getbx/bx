import XCTest

// The Explain screen, driven the way a person uses it, on the committed synthetic fixture
// (never the owner's rules). What the screenshots cannot promise, these check: the answer is
// on screen, says the same three things the Mac says, and every text stays inside the window.
final class ExplainScreenUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    private func launch(target: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["--fixture", "--target", target]
        app.launch()
        return app
    }

    // A row's text, however SwiftUI exposes it (merged into the label, or split into label and value).
    private func text(of element: XCUIElement) -> String {
        let value = (element.value as? String) ?? ""
        return element.label + " " + value
    }

    private func assertAnswer(_ app: XCUIApplication, goes: String, because: String, rule: String?, file: StaticString = #filePath, line: UInt = #line) {
        let goesRow = app.descendants(matching: .any)["explain.goes"]
        XCTAssertTrue(goesRow.waitForExistence(timeout: 10), "no verdict row", file: file, line: line)
        XCTAssertTrue(text(of: goesRow).contains(goes), "verdict row says \(text(of: goesRow)), want \(goes)", file: file, line: line)
        let becauseRow = app.descendants(matching: .any)["explain.because"]
        XCTAssertTrue(text(of: becauseRow).contains(because), "reason row says \(text(of: becauseRow)), want \(because)", file: file, line: line)
        let ruleRow = app.descendants(matching: .any)["explain.rule"]
        if let rule {
            XCTAssertTrue(text(of: ruleRow).contains(rule), "rule row says \(text(of: ruleRow)), want \(rule)", file: file, line: line)
        } else {
            XCTAssertFalse(ruleRow.exists, "a rule row is shown where no rule matched", file: file, line: line)
        }
        let window = app.windows.firstMatch.frame
        for row in [goesRow, becauseRow] + (rule == nil ? [] : [ruleRow]) {
            XCTAssertGreaterThanOrEqual(row.frame.minX, window.minX, "\(row.identifier) starts left of the window", file: file, line: line)
            XCTAssertLessThanOrEqual(row.frame.maxX, window.maxX, "\(row.identifier) runs past the right edge", file: file, line: line)
        }
    }

    func testADirectRuleIsExplainedWithTheRuleThatMatched() {
        assertAnswer(launch(target: "www.apple.com"), goes: "Direct", because: "your direct rule", rule: "*.apple.com")
    }

    func testAPastedURLIsExplainedByItsHost() {
        let app = launch(target: "https://chat.example.net/c/1")
        assertAnswer(app, goes: "Through the tunnel", because: "your proxy rule", rule: "*.example.net")
        XCTAssertTrue(app.staticTexts["chat.example.net"].exists || app.staticTexts["CHAT.EXAMPLE.NET"].exists, "the section header should name the host, not the URL")
    }

    func testIPv6IsReportedBlockedAndTheLongReasonStaysOnScreen() {
        assertAnswer(launch(target: "2001:db8::1"), goes: "Blocked", because: "IPv6 is blocked on this phone (bx sends apps to IPv4)", rule: nil)
    }
}
