#!/usr/bin/env bash
# bx iOS 开发构建的驱动(第二期:无界面)。计划见
# docs/superpowers/plans/2026-09-29-mobile-phase2-ios-tunnel.md
#
#   scripts/ios-dev.sh config            以 root 读 /etc/bx/config.yaml,生成 apps/ios/Dev/(要 sudo 密码)
#   scripts/ios-dev.sh build             构建 libbox(若缺)、生成工程、签名构建、装到手机
#   scripts/ios-dev.sh run <scenario>    connect | deadserver | armed | armedbroken | stop | remove,打出 BX-RESULT 那一行
#
# 设备:BX_IOS_DEVICE(默认第一台已连接的真机)。
set -euo pipefail
cd "$(dirname "$0")/.."
root="$(pwd)"

device() {
	if [ -n "${BX_IOS_DEVICE:-}" ]; then echo "$BX_IOS_DEVICE"; return; fi
	xcrun devicectl list devices 2>/dev/null | awk '/connected/ && /physical/ {for (i=1;i<=NF;i++) if ($i ~ /^[0-9A-F]{8}-[0-9A-F]{16}$/) {print $i; exit}}'
}

case "${1:-}" in
config)
	tool="$(mktemp -d)/bx-ios-devconfig"
	go build -o "$tool" ./cmd/bx-ios-devconfig
	sudo "$tool" --out "$root/apps/ios/Dev"
	;;
build)
	[ -f apps/ios/Dev/libbox-config.json ] || { echo "apps/ios/Dev is empty: run scripts/ios-dev.sh config first" >&2; exit 1; }
	bash scripts/build-libbox-ios.sh
	(cd apps/ios && xcodegen generate --quiet)
	dev="$(device)"
	[ -n "$dev" ] || { echo "no connected iPhone" >&2; exit 1; }
	# 签名走 Xcode 里已登录的团队账号(Team XXXXXXXXXX)。App Store Connect API key 在
	# 这一步被 provisioning 服务拒过(2026-09-29,「Authentication failed: bearer token」),
	# 而同一把 key 的公证在 CI 上一直正常 —— 两个服务认证不同,别把它加回来当默认路径。
	xcodebuild -project apps/ios/BxiOS.xcodeproj -scheme BxApp -configuration Debug \
		-destination "id=$dev" -derivedDataPath apps/ios/build \
		-allowProvisioningUpdates build -quiet
	xcrun devicectl device install app --device "$dev" apps/ios/build/Build/Products/Debug-iphoneos/bx.app
	;;
run)
	scenario="${2:?scenario: connect | deadserver | armed | armedbroken | stop | remove}"
	dev="$(device)"
	xcrun devicectl device process launch --device "$dev" --terminate-existing --console \
		com.getbx.bx.ios --scenario "$scenario" 2>&1 | tee /dev/stderr | grep '^BX-RESULT ' | sed 's/^BX-RESULT //'
	;;
*)
	sed -n '2,11p' "$0"
	exit 2
	;;
esac
