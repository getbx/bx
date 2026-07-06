#!/bin/sh
# bx 一键安装(仅装 CLI 到 /usr/local/bin/bx;不 setup、不 up、不碰网络路由)。
# 用法:  curl -fsSL https://raw.githubusercontent.com/getbx/bx/master/scripts/install.sh | sh
# 装完:  sudo bx setup '<你的客户端链接>'  &&  sudo bx up
set -eu

REPO="getbx/bx"
BIN="/usr/local/bin/bx"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	linux) os=linux ;;
	darwin) os=darwin ;;
	*) echo "✗ 不支持的系统:$os(bx 目前支持 linux / macOS)"; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "✗ 不支持的架构:$arch"; exit 1 ;;
esac

asset="bx_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/latest/download"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "⏳ 下载 $asset …"
curl -fSL "$base/$asset" -o "$tmp/$asset"
curl -fSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"

echo "⏳ 校验 SHA256 …"
want=$(awk -v a="$asset" '$NF==a {print $1}' "$tmp/SHA256SUMS")
if [ -z "$want" ]; then
	echo "✗ SHA256SUMS 里找不到 $asset,中止"; exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
else
	echo "✗ 没有 sha256sum / shasum,无法校验,中止"; exit 1
fi
if [ "$want" != "$got" ]; then
	echo "✗ 校验和不符(期望 $want,实得 $got),中止"; exit 1
fi
echo "✅ 校验通过"

tar -C "$tmp" -xzf "$tmp/$asset"

SUDO=""
if [ "$(id -u)" -ne 0 ]; then
	SUDO="sudo"
	echo "→ 需要 sudo 写入 $BIN"
fi
$SUDO install -m 0755 "$tmp/bx" "$BIN"

echo "✅ bx 已安装:$BIN"
"$BIN" --version 2>/dev/null || true
echo ""
echo "下一步:"
echo "  sudo bx setup '<你的客户端链接>'   # 写配置 + 装服务(不启动)"
echo "  sudo bx up                          # 启动并接管流量"
echo "以后更新:sudo bx update"
