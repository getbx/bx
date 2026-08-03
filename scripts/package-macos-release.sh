#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
VERSION="${BX_VERSION:-dev}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist/release}"
RELEASE_DIR="$DIST_ROOT/$RELEASE_NAME"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS release packaging requires macOS." >&2
  exit 1
fi

case "$ARCH" in
  arm64|amd64) ;;
  *)
    echo "Unsupported BX_ARCH=$ARCH; use arm64 or amd64." >&2
    exit 2
    ;;
esac

rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"
touch "$RELEASE_DIR/.metadata_never_index"

echo "Building bx for darwin/$ARCH..."
GOOS=darwin GOARCH="$ARCH" go build -trimpath -ldflags "-X github.com/getbx/bx/internal/version.Version=$VERSION" -o "$RELEASE_DIR/bx" "$ROOT"

echo "Packaging menu bar app..."
BX_ARCH="$ARCH" BX_VERSION="$VERSION" BX_DIST_DIR="$ROOT/dist/macos-$ARCH" "$ROOT/scripts/package-macos-menu.sh" >/dev/null
ditto "$ROOT/dist/macos-$ARCH/Bx.app" "$RELEASE_DIR/Bx.app"

echo "Embedding release assets into Bx.app..."
RESOURCES="$RELEASE_DIR/Bx.app/Contents/Resources"
install -m 0755 "$RELEASE_DIR/bx" "$RESOURCES/bx-cli"
GOOS=darwin GOARCH="$ARCH" go build -trimpath -ldflags "-X github.com/getbx/bx/internal/version.Version=$VERSION" \
  -o "$RESOURCES/bx-bridge" "$ROOT/cmd/bx-bridge"
CLI_SHA=$(shasum -a 256 "$RESOURCES/bx-cli" | awk '{print $1}')
BRIDGE_SHA=$(shasum -a 256 "$RESOURCES/bx-bridge" | awk '{print $1}')
cat > "$RESOURCES/release.json" <<EOF
{
  "schema_version": 1,
  "version": "$VERSION",
  "platform": "darwin/$ARCH",
  "assets": {
    "bx-cli": "$CLI_SHA",
    "bx-bridge": "$BRIDGE_SHA"
  }
}
EOF
rm "$RELEASE_DIR/bx"   # 顶层裸 bx 不再进包(经 App 内 bx-cli 由 app-install 安装)

cat > "$RELEASE_DIR/install.sh" <<'SCRIPT'
#!/bin/bash
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
[ "$(uname -s)" = "Darwin" ] || { echo "仅支持 macOS" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || { echo "请勿用 root 运行本脚本(安装时会请求一次 sudo)" >&2; exit 1; }
MACHINE="$(uname -m)"
case "__BX_RELEASE_ARCH__:$MACHINE" in
  arm64:arm64|amd64:x86_64) ;;
  *) echo "架构不匹配:包为 __BX_RELEASE_ARCH__,机器为 $MACHINE" >&2; exit 1 ;;
esac
[ -x "$DIR/Bx.app/Contents/Resources/bx-cli" ] || { echo "包不完整:缺少 bx-cli" >&2; exit 1; }
echo "即将安装 Bx.app 到 /Applications 并配置 bx(需要一次管理员授权)。"
echo "安装不会启动保护、不修改你的连接配置。"
sudo "$DIR/Bx.app/Contents/Resources/bx-cli" app-install --app-source "$DIR/Bx.app"
echo "完成。打开菜单栏的 bx 图标继续 Set Up。"
SCRIPT

perl -0pi -e "s/__BX_RELEASE_ARCH__/$ARCH/g" "$RELEASE_DIR/install.sh"

cat > "$RELEASE_DIR/uninstall.sh" <<'SCRIPT'
#!/bin/bash
set -euo pipefail
echo "本包不再包含独立的卸载脚本。"
echo "请运行:"
echo "  sudo bx uninstall"
echo
echo "该命令会:停用并卸载 Guardian 保护服务、移除 Bx.app 与 bx CLI、"
echo "移除 launchd 登录项;但会保留 /etc/bx(你的连接配置)与 /var/lib/bx(运行时数据)。"
SCRIPT

cat > "$RELEASE_DIR/README.txt" <<TXT
bx macOS $ARCH release ($VERSION)

安装(两种方式任选其一):
  1. 将 Bx.app 拖到 /Applications,双击打开后点 "Install bx..."。
  2. 运行 ./install.sh(等价于上一步,命令行方式)。

安装做了什么:
  将 Bx.app 装到 /Applications,并把 App 内嵌的 bx-cli 安装为系统 bx 命令、
  配置 Guardian 保护服务与登录项。安装不会启动保护、不修改你的连接配置。

安装之后:
  打开菜单栏的 bx 图标,选择 Set Up bx... 继续配置。

卸载:
  sudo bx uninstall
  (详见 ./uninstall.sh)

Notes:
  install.sh 需要以你的普通 macOS 用户身份运行(会在需要时通过 sudo 请求一次管理员授权)。
  install.sh 不会执行 bx setup、不会执行 bx up、不会修改 DNS/路由。
  旧版客户端(升级前安装的 bx)对本包运行 bx update --package 会解包失败并干净报错,
  属预期行为(pre-1.0);请改用本 README 的安装方式重新安装。
TXT

chmod +x "$RELEASE_DIR/install.sh" "$RELEASE_DIR/uninstall.sh"

(
  cd "$DIST_ROOT"
  rm -f "$RELEASE_NAME.tar.gz"
  tar -czf "$RELEASE_NAME.tar.gz" "$RELEASE_NAME"
  shasum -a 256 "$RELEASE_NAME.tar.gz" > SHA256SUMS
)

echo "Built: $RELEASE_DIR"
echo "Archive: $DIST_ROOT/$RELEASE_NAME.tar.gz"
echo "Checksums: $DIST_ROOT/SHA256SUMS"
