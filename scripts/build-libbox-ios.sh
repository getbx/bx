#!/usr/bin/env bash
# 从 sing-box 同一 tag 的源码构建 iOS 用的 Libbox.xcframework,放进 apps/ios/Frameworks/(不进仓库)。
# tag 取自 internal/singboxrules.TargetSingboxVersion —— 与桌面内嵌、与翻译器钉的是同一个版本。
# 用 sing-box 自己的 gomobile 分叉(上游 gomobile 不认 -libname 等参数,实测)。
set -euo pipefail
cd "$(dirname "$0")/.."
ver="$(sed -n 's/^const TargetSingboxVersion = "\(.*\)"$/\1/p' internal/singboxrules/version.go)"
[ -n "$ver" ] || { echo "cannot read TargetSingboxVersion" >&2; exit 1; }
dest="apps/ios/Frameworks/Libbox.xcframework"
if [ -f "$dest/.bx-version" ] && [ "$(cat "$dest/.bx-version")" = "$ver" ]; then
	echo "Libbox.xcframework $ver already built"
	exit 0
fi
cache="${HOME}/Library/Caches/bx-ios/sing-box-${ver}"
if [ ! -d "$cache/.git" ]; then
	rm -rf "$cache"
	git clone -q --depth 1 --branch "v${ver}" https://github.com/SagerNet/sing-box.git "$cache"
fi
gobin="$(go env GOPATH)/bin"
GOFLAGS= go install github.com/sagernet/gomobile/cmd/gomobile@v0.1.13
GOFLAGS= go install github.com/sagernet/gomobile/cmd/gobind@v0.1.13
(cd "$cache" && rm -rf Libbox.xcframework && PATH="$gobin:$PATH" go run ./cmd/internal/build_libbox -target apple -platform ios)
mkdir -p apps/ios/Frameworks
rm -rf "$dest"
mv "$cache/Libbox.xcframework" "$dest"
echo "$ver" > "$dest/.bx-version"
echo "built Libbox.xcframework $ver"
