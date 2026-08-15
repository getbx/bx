#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MENU="$ROOT/apps/macos/BxMenu"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/bx-menu-tests.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

run_test() {
  local name="$1"
  shift
  swiftc "$@" -o "$TMP/$name"
  "$TMP/$name"
}

run_test status-indicator \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Tests/StatusIndicatorTests.swift"
run_test status-presentation \
  "$MENU/Sources/BxMenu/StatusPresentation.swift" \
  "$MENU/Tests/StatusPresentationTests.swift"
run_test update-presentation \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Tests/UpdatePresentationTests.swift"
run_test first-run \
  "$MENU/Sources/BxMenu/FirstRun.swift" \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/FirstRunTests.swift"
run_test recovery-presentation \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Tests/RecoveryPresentationTests.swift"
run_test guardian-client \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Tests/GuardianClientTests.swift"
run_test instance-gate \
  "$MENU/Sources/BxMenu/InstanceGate.swift" \
  "$MENU/Tests/InstanceGateTests.swift"
run_test status-report \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/StatusReport.swift" \
  "$MENU/Tests/StatusReportTests.swift"
run_test install-presentation \
  "$MENU/Sources/BxMenu/InstallPresentation.swift" \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Tests/InstallPresentationTests.swift"
run_test guardian-status \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Tests/GuardianStatusTests.swift"
run_test guardian-client-timeout \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Tests/GuardianClientTimeoutTests.swift"
run_test toggle-controller \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/ToggleControllerTests.swift"
run_test quit-disposition \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/QuitDispositionTests.swift"
run_test quit-plan \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/QuitPlanTests.swift"
run_test toggle-escape \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/ToggleEscapeTests.swift"
run_test guardian-failure-code \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/GuardianFailureCodeTests.swift"

run_test menu-icon \
  "$MENU/Sources/BxMenu/MenuIcon.swift" \
  "$MENU/Tests/MenuIconTests.swift"

run_test menu-rows \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/StatusReport.swift" \
  "$MENU/Sources/BxMenu/MenuRows.swift" \
  "$MENU/Sources/BxMenu/MaintenancePresentation.swift" \
  "$MENU/Tests/MenuRowsTests.swift"

run_test maintenance-presentation \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/MenuRows.swift" \
  "$MENU/Sources/BxMenu/MaintenancePresentation.swift" \
  "$MENU/Tests/MaintenancePresentationTests.swift"

run_test stopped-diagnosis \
  "$MENU/Sources/BxMenu/StoppedDiagnosis.swift" \
  "$MENU/Tests/StoppedDiagnosisTests.swift"

run_test rules-model \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Tests/RulesModelTests.swift"

run_test deploy-model \
  "$MENU/Sources/BxMenu/DeployModel.swift" \
  "$MENU/Tests/DeployModelTests.swift"

run_test servers-model \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Tests/ServersModelTests.swift"

run_test menu-cadence \
  "$MENU/Sources/BxMenu/MenuCadence.swift" \
  "$MENU/Tests/MenuCadenceTests.swift"

run_test guardian-status-fields \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Tests/GuardianStatusFieldsTests.swift"

echo "macOS menu tests passed"
