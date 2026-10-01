#!/usr/bin/env bash
# 把 mobile/bxkit 绑成 iOS 用的 Bxkit.xcframework,放进 apps/ios/Frameworks/(不进仓库)。
# gomobile 要求绑定时能解析到它自己的 bind 包;为此不往 bx 的 go.mod 里加一条只有构建才用的
# 依赖,而是在一个临时包装模块里绑(replace 回本仓库)。
set -euo pipefail
cd "$(dirname "$0")/.."
root="$(pwd)"
dest="apps/ios/Frameworks/Bxkit.xcframework"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
gomod_go="$(sed -n 's/^go \(.*\)$/\1/p' go.mod)"
cat > "$work/go.mod" <<MOD
module bxkitbuild

go ${gomod_go}

require github.com/getbx/bx v0.0.0

replace github.com/getbx/bx => ${root}
MOD
cat > "$work/tools.go" <<'GO'
//go:build tools

package bxkitbuild

import (
	_ "github.com/getbx/bx/mobile/bxkit"
	_ "github.com/getbx/bx/mobile/bxdeploy"
	_ "github.com/sagernet/gomobile/bind"
)
GO
gobin="$(go env GOPATH)/bin"
(cd "$work" && GOFLAGS=-mod=mod go get github.com/sagernet/gomobile@v0.1.13 >/dev/null && GOFLAGS=-mod=mod go mod tidy >/dev/null)
rm -rf "$dest"
mkdir -p apps/ios/Frameworks
(cd "$work" && GOFLAGS=-mod=mod PATH="$gobin:$PATH" gomobile bind -target ios,iossimulator -iosversion 17.0 \
	-o "$root/$dest" github.com/getbx/bx/mobile/bxkit github.com/getbx/bx/mobile/bxdeploy)
echo "built $dest"
