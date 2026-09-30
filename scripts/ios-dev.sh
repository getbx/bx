#!/usr/bin/env bash
# bx iOS 开发构建的驱动(第二期:无界面)。计划见
# docs/superpowers/plans/2026-09-29-mobile-phase2-ios-tunnel.md
#
#   scripts/ios-dev.sh config            以 root 读 /etc/bx/config.yaml,生成 apps/ios/Dev/(要 sudo 密码)
#   scripts/ios-dev.sh build             构建 libbox(若缺)、生成工程、签名构建、装到手机
#   scripts/ios-dev.sh snapshot          模拟器里用合成夹具(不含你的规则)截 Explain 页,浅色/深色各一张
#   scripts/ios-dev.sh uitest            模拟器里跑 Explain 页的 XCUITest(合成夹具),每项封顶 2 分钟
#   scripts/ios-dev.sh run <scenario>    connect | deadserver | armed | armedbroken | app | explain --target <x> | stop | remove
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
	bash scripts/build-bxkit-ios.sh
	(cd apps/ios && xcodegen generate --quiet)
	dev="$(device)"
	[ -n "$dev" ] || { echo "no connected iPhone" >&2; exit 1; }
	# 签名走 Xcode 里已登录的团队账号。App Store Connect API key 在
	# 这一步被 provisioning 服务拒过(2026-09-29,「Authentication failed: bearer token」),
	# 而同一把 key 的公证在 CI 上一直正常 —— 两个服务认证不同,别把它加回来当默认路径。
	# 团队 ID 不进仓库:取自本机私有的 ~/.private_keys/app-store-connect.env(APPLE_TEAM_ID),
	# 或显式给 BX_IOS_TEAM。
	team="${BX_IOS_TEAM:-}"
	if [ -z "$team" ] && [ -f "$HOME/.private_keys/app-store-connect.env" ]; then
		team="$(. "$HOME/.private_keys/app-store-connect.env" && echo "${APPLE_TEAM_ID:-}")"
	fi
	[ -n "$team" ] || { echo "no Apple team ID: set BX_IOS_TEAM or APPLE_TEAM_ID in ~/.private_keys/app-store-connect.env" >&2; exit 1; }
	xcodebuild -project apps/ios/BxiOS.xcodeproj -scheme BxApp -configuration Debug \
		-destination "id=$dev" -derivedDataPath apps/ios/build \
		DEVELOPMENT_TEAM="$team" -allowProvisioningUpdates build -quiet
	xcrun devicectl device install app --device "$dev" apps/ios/build/Build/Products/Debug-iphoneos/bx.app
	;;
snapshot)
	# spec §8 在第四步之前要回答的问题:界面能不能让 agent 自己看。能 —— 真实渲染路径
	# (模拟器里真跑 App),用 --fixture 的合成规则,截图落 apps/ios/build-sim/snapshots/。
	sim="${BX_IOS_SIM:-$(xcrun simctl list devices available | awk -F'[()]' '/iPhone 1[0-9] Pro \(/ {print $2; exit}')}"
	[ -n "$sim" ] || { echo "no iPhone simulator" >&2; exit 1; }
	bash scripts/build-libbox-ios.sh
	bash scripts/build-bxkit-ios.sh
	mkdir -p apps/ios/Dev
	(cd apps/ios && xcodegen generate --quiet)
	xcodebuild -project apps/ios/BxiOS.xcodeproj -scheme BxApp -configuration Debug \
		-destination "id=$sim" -derivedDataPath apps/ios/build-sim CODE_SIGNING_ALLOWED=NO build -quiet
	xcrun simctl boot "$sim" 2>/dev/null || true
	xcrun simctl bootstatus "$sim" -b >/dev/null
	xcrun simctl install "$sim" apps/ios/build-sim/Build/Products/Debug-iphonesimulator/bx.app
	out=apps/ios/build-sim/snapshots
	mkdir -p "$out"
	for look in light dark; do
		xcrun simctl ui "$sim" appearance "$look"
		for home in "" --fixture-server "--fixture-server --fixture-on"; do
			xcrun simctl launch --terminate-running-process "$sim" com.getbx.bx.ios --fixture $home >/dev/null
			sleep 3
			name="$(echo "$home" | sed 's/--fixture-//g; s/ /-/g')"
			xcrun simctl io "$sim" screenshot "$out/home-${look}${name:+-$name}.png" >/dev/null 2>&1
		done
		for target in www.apple.com https://chat.example.net/c/1 2001:db8::1; do
			xcrun simctl launch --terminate-running-process "$sim" com.getbx.bx.ios --fixture --target "$target" >/dev/null
			sleep 3
			name="$(echo "$target" | tr -c 'A-Za-z0-9' '_')"
			xcrun simctl io "$sim" screenshot "$out/explain-${look}-${name}.png" >/dev/null 2>&1
		done
	done
	xcrun simctl ui "$sim" appearance light
	ls "$out"
	;;
uitest)
	# 每项测试封顶 2 分钟:XCUITest 在断言失败后会去抓整棵无障碍树做排查,这一步在本机实测会
	# 挂住半小时以上(2026-09-29)—— 封顶之后失败照样是失败,只是不再挂住。
	sim="${BX_IOS_SIM:-$(xcrun simctl list devices available | awk -F'[()]' '/iPhone 1[0-9] Pro \(/ {print $2; exit}')}"
	[ -n "$sim" ] || { echo "no iPhone simulator" >&2; exit 1; }
	bash scripts/build-libbox-ios.sh
	bash scripts/build-bxkit-ios.sh
	mkdir -p apps/ios/Dev
	(cd apps/ios && xcodegen generate --quiet)
	xcodebuild test -project apps/ios/BxiOS.xcodeproj -scheme BxApp -destination "id=$sim" \
		-derivedDataPath apps/ios/build-sim -test-timeouts-enabled YES -maximum-test-execution-time-allowance 120 \
		2>&1 | grep -E "Test Case .*(passed|failed)|error:|\*\* TEST"
	exit "${PIPESTATUS[0]}"
	;;
run)
	scenario="${2:?scenario: connect | deadserver | armed | armedbroken | stop | remove}"
	dev="$(device)"
	shift 2
	xcrun devicectl device process launch --device "$dev" --terminate-existing --console \
		com.getbx.bx.ios --scenario "$scenario" "$@" 2>&1 | tee /dev/stderr | grep '^BX-RESULT ' | sed 's/^BX-RESULT //'
	;;
*)
	sed -n '2,11p' "$0"
	exit 2
	;;
esac
