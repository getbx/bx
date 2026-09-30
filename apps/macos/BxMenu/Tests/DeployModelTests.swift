import Foundation

@main
struct DeployModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    // The password is never an argument (argv is visible to every process on this Mac): the flag
    // only says "read it from stdin".
    static func testArgumentsCarryTheFlagNeverThePassword() {
        let t = DeployTarget(host: "203.0.113.9", sshPort: "2222", user: "root", name: "tokyo")
        let args = deployArguments(t, DeployOptions(hasPassword: true))
        expect(args == ["server", "deploy", "--json", "--ssh-port", "2222", "--password-stdin", "--name", "tokyo", "root@203.0.113.9"],
               "arguments: \(args)")
        let keyOnly = deployArguments(DeployTarget(host: "h", sshPort: "22", user: "root"), DeployOptions())
        expect(!keyOnly.contains("--password-stdin"), "no password, no stdin flag: \(keyOnly)")
        expect(!keyOnly.contains("--ssh-port"), "port 22 is the default, leave ssh_config alone: \(keyOnly)")
        let retry = deployArguments(DeployTarget(host: "h"), DeployOptions(reinstall: true, forgetHostKey: true))
        expect(retry.contains("--force") && retry.contains("--forget-host-key"), "retry flags: \(retry)")
    }

    static func testValidationCatchesTypos() {
        expect(deployValidationError(DeployTarget(host: "")) != nil, "empty address accepted")
        expect(deployValidationError(DeployTarget(host: "root@1.2.3.4")) != nil, "user@ in the address field accepted")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", sshPort: "0")) != nil, "port 0 accepted")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", sshPort: "70000")) != nil, "port 70000 accepted")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", sshPort: "ssh")) != nil, "non-numeric port accepted")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", sshPort: "")) == nil, "empty port should mean 22")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", user: "")) != nil, "empty login accepted")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4")) == nil, "a plain address was refused")
    }

    // Same rule as Go's config.ValidateServerName: accepted here but refused there would end a
    // successful install with a configuration error.
    static func testNameRulesMatchTheConfig() {
        expect(deployValidationError(DeployTarget(host: "h", name: "东京")) != nil, "non-ASCII name accepted")
        expect(deployValidationError(DeployTarget(host: "h", name: "tokyo-2.a_b")) == nil, "valid name refused")
        expect(deployValidationError(DeployTarget(host: "h", name: String(repeating: "a", count: 65))) != nil, "65-char name accepted")
    }

    static func testProgressAdvancesStepByStep() {
        var p = DeployProgress()
        for id in ["connect", "download"] {
            p.apply(DeployEvent(event: "step", step: id))
        }
        expect(p.state("connect") == .done && p.state("download") == .running && p.state("install") == .pending,
               "after two steps: \(p)")
        p.apply(DeployEvent(event: "error", code: "install_failed"))
        expect(p.state("download") == .failed, "the running step should turn failed")
        var ok = DeployProgress()
        for id in deploySteps { ok.apply(DeployEvent(event: "step", step: id)) }
        ok.apply(DeployEvent(event: "done", added: true))
        expect(deploySteps.allSatisfy { ok.state($0) == .done }, "done should close every step: \(ok)")
    }

    static func testEventsParseFromTheCLIOutput() {
        let line = #"{"event":"done","name":"tokyo","host":"203.0.113.9","added":true,"probe":{"measured":true,"reachable":true,"rtt_ms":180}}"#
        let e = parseDeployEvent(line)
        expect(e?.name == "tokyo" && e?.probe?.rttMS == 180 && e?.added == true, "parsed: \(String(describing: e))")
        expect(parseDeployEvent("not json") == nil, "garbage parsed")
    }

    // Two failures have a one-click way forward; the rest explain what to change.
    static func testFailuresOfferTheRightNextStep() {
        expect(deployFailure("already_installed").action == .reinstall, "already installed should offer Reinstall")
        expect(deployFailure("host_key_changed").action == .forgetHostKey, "changed fingerprint should offer I Reinstalled It")
        expect(deployFailure("auth_failed").action == nil, "a wrong password has no retry button")
        for code in ["unreachable", "auth_failed", "sudo_password", "password_change_required", "unsupported_system", "checksum", "install_failed", "something_new"] {
            let f = deployFailure(code)
            expect(!f.headline.isEmpty && !f.advice.isEmpty, "\(code) has no words")
        }
    }

    // The most common beginner failure after a clean install: the provider's firewall.
    static func testUnreachableAfterInstallPointsAtTheProviderFirewall() {
        let e = DeployEvent(event: "done", name: "tokyo", added: true,
                            probe: DeployProbe(measured: true, reachable: false))
        let r = deployResult(e)
        expect(r.detail.contains("security group"), "unreachable result: \(r.detail)")
        expect(!r.headline.contains("ready"), "a server this Mac cannot reach is not \"ready\": \(r.headline)")
        let fine = deployResult(DeployEvent(event: "done", name: "tokyo", added: true,
                                            probe: DeployProbe(measured: true, reachable: true, rttMS: 180)))
        expect(fine.detail.contains("180"), "reachable result: \(fine.detail)")
        expect(fine.detail.contains("did not change"), "must say the current exit did not change: \(fine.detail)")
    }

    static func testNotSetUpHandsTheLinksToSetup() {
        let r = deployResult(DeployEvent(event: "done", notSetUp: true, link: "bx://A", udp: "bx://B"))
        expect(r.setUpLink == "bx://A" && r.setUpUDP == "bx://B", "links: \(String(describing: r.setUpLink)) \(String(describing: r.setUpUDP))")
        expect(!r.canOpenServers, "nothing was added, nothing to open")
    }

    // After a deploy the phone can join by scanning a QR of the link — the link rides along.
    static func testTheResultCarriesTheLinkForThePhone() {
        let r = deployResult(DeployEvent(event: "done", name: "tokyo", added: true, link: "bx://A"))
        expect(r.phoneLink == "bx://A", "phone link: \(String(describing: r.phoneLink))")
        let none = deployResult(DeployEvent(event: "done", name: "tokyo", added: true))
        expect(none.phoneLink == nil, "no link, no phone button")
    }

    static func main() {
        testTheResultCarriesTheLinkForThePhone()
        testArgumentsCarryTheFlagNeverThePassword()
        testValidationCatchesTypos()
        testNameRulesMatchTheConfig()
        testProgressAdvancesStepByStep()
        testEventsParseFromTheCLIOutput()
        testFailuresOfferTheRightNextStep()
        testUnreachableAfterInstallPointsAtTheProviderFirewall()
        testNotSetUpHandsTheLinksToSetup()
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        } else {
            print("DeployModelTests passed")
        }
    }
}
