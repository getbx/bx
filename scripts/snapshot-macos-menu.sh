#!/usr/bin/env bash
# 菜单窗口的离屏快照:出 PNG(给人看)+ 视图树 dump(给守卫看)。
#
# **不点菜单栏、不截屏、不弹窗、不抢焦点**(.prohibited 策略下实测
# occlusionState=hidden、app.isActive=false),所以它可以在人正常工作时跑。
#
# 用法: bash scripts/snapshot-macos-menu.sh [输出目录]
#       默认输出到 dist.noindex/menu-snapshots/
#
# 它需要一个可用的 WindowServer 会话 —— 没有就**明说跳过**,绝不安静通过:
# 一条安静地跑了零个窗口的闸门,与没有这条闸门在输出上完全一样。
set -euo pipefail

MENU="$(cd "$(dirname "${BASH_SOURCE[0]}")/../apps/macos/BxMenu" && pwd)"
OUT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/dist.noindex/menu-snapshots}"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "SKIPPED: 非 macOS,菜单快照跑不了"
  exit 0
fi
if ! command -v xcrun >/dev/null 2>&1; then
  echo "SKIPPED: 没有 xcrun,菜单快照跑不了"
  exit 0
fi

mkdir -p "$OUT"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 编译清单与 scripts/test-macos-menu.sh 同理:swiftc 直编所需源文件。
# **窗口那几个文件互相牵连**(FlippedView 曾住在 ServersWindow 里),
# 所以这里带上整组,而不是逐个试到编过为止。
xcrun swiftc -O \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/MaintenancePresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/StatusReport.swift" \
  "$MENU/Sources/BxMenu/MenuRows.swift" \
  "$MENU/Sources/BxMenu/MenuLayout.swift" \
  "$MENU/Sources/BxMenu/RulesModel.swift" \
  "$MENU/Sources/BxMenu/RulesWindow.swift" \
  "$MENU/Sources/BxMenu/ServersModel.swift" \
  "$MENU/Sources/BxMenu/ServersWindow.swift" \
  "$MENU/Sources/BxMenu/DiagnosticsModel.swift" \
  "$MENU/Sources/BxMenu/DiagnosticsWindow.swift" \
  "$MENU/Sources/BxMenu/LogsModel.swift" \
  "$MENU/Sources/BxMenu/AppTrafficModel.swift" \
  "$MENU/Sources/BxMenu/AppTrafficWindow.swift" \
  "$MENU/Sources/BxMenu/DeployModel.swift" \
  "$MENU/Sources/BxMenu/DeployWindow.swift" \
  "$MENU/Snapshots/main.swift" \
  -o "$TMP/snapshot"

if ! "$TMP/snapshot" "$MENU/Snapshots/fixtures" "$OUT" 2>"$TMP/err"; then
  # 没有 WindowServer(纯 headless 会话)时 AppKit 会在连接窗口服务器时失败。
  # 那是「跑不了」,不是「跑了没过」——两者必须分开报。
  if grep -qiE 'window server|WindowServer|not permitted|Connection.*refused' "$TMP/err"; then
    echo "SKIPPED: 没有可用的 WindowServer 会话(headless),菜单快照跑不了"
    cat "$TMP/err" >&2
    exit 0
  fi
  cat "$TMP/err" >&2
  exit 1
fi

echo "输出目录: $OUT"
ls -1 "$OUT"
echo "macOS menu snapshots passed"
