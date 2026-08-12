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
# 只认 --yes/-y:既给非交互调用方一个表态的途径,又不把任意参数拼进一条 sudo
# 命令行(也顺手让 ./install.sh --help 不再走到下面那句「完成」)。
ASSUME_YES=""
for arg in "$@"; do
  case "$arg" in
    --yes|-y) ASSUME_YES="--yes" ;;
    *) echo "用法:./install.sh [--yes]" >&2; exit 2 ;;
  esac
done
echo "即将安装 Bx.app 到 /Applications 并配置 bx(需要一次管理员授权)。"
echo "安装不修改你的连接配置。全新安装不会启动保护;若这台机器已经装过 bx(Guardian 服务已加载,"
echo "无论保护是否开启),安装会先征得你同意,"
echo "再停止保护、换好文件、重启保护服务并把保护恢复到原状态(保护开着时期间断网几秒)。"
# 不加 --yes:命令行安装时用户就在终端前,该问就问(会断网的操作必须当面确认)。
# 非交互场景(无终端的 SSH/CI)由用户显式 ./install.sh --yes 表态,经 "$@" 透传;
# 不表态时 app-install 会以非零退出报错,set -e 就会在打印「完成」之前中止。
# ASSUME_YES 不加引号:它要么是空(不传参),要么是单个 --yes,不会被词分割。
rc=0
sudo "$DIR/Bx.app/Contents/Resources/bx-cli" app-install --app-source "$DIR/Bx.app" $ASSUME_YES || rc=$?
if [ "$rc" -eq 2 ]; then
  echo "已取消:未做任何改动。"
  exit 2
elif [ "$rc" -ne 0 ]; then
  exit "$rc"
fi
# **菜单栏的 LaunchAgent 由这里 bootstrap,不由上面那条 sudo。**
#
# bootstrap 是 launchctl 里少数**必须身处目标 GUI session** 的操作(bootout 与
# kickstart 不是)。root 进程没有目标用户 GUI 域的 audit session token,直接
# bootstrap 必然 EIO(5);`launchctl asuser <uid>` 是个 workaround,而 2026-08-11
# 真机升级证明它**不可靠** —— 那次 app-install 成功 bootout 了正在跑的菜单栏,
# 随后 asuser+bootstrap 报 `Bootstrap failed: 5: Input/output error`,于是**升级把
# 一个本来好好的菜单栏弄没了**:launchd 里没有 job、进程也没了,而 plist 与程序
# 都完好。
#
# 而这个脚本本来就以普通用户身份跑(第 5 行明确拒绝 root),它**就在**那个 GUI
# session 里 —— 这一步天生属于它,不属于那条 sudo。
#
# 失败只警告不中止:无 GUI session 的场景(SSH、CI)bootstrap 一定失败,而那时
# 「文件都装好了」仍然是真的,不该把整个安装判成失败。
if [ -f "$HOME/Library/LaunchAgents/com.getbx.bx.menu.plist" ]; then
  if ! launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.getbx.bx.menu.plist" 2>/dev/null; then
    # 已经加载时 bootstrap 会失败,那是正常的 —— 用 kickstart 兜一下(不带 -k:
    # 跑着就是 no-op,不闪烁)。两条都失败才提示用户。
    launchctl kickstart "gui/$(id -u)/com.getbx.bx.menu" 2>/dev/null || {
      echo "! 菜单栏没能自动启动。手动执行(不要加 sudo):"
      echo "    launchctl bootstrap gui/$(id -u) $HOME/Library/LaunchAgents/com.getbx.bx.menu.plist"
    }
  fi
fi
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
  配置 Guardian 保护服务与登录项。安装不修改你的连接配置。
  全新安装不会启动保护。若这台机器已经装过 bx(Guardian 服务已加载,无论保护是否
  开启,即覆盖安装/升级),安装会先问你一次,再停止保护、换好文件、重启保护服务
  并把保护恢复到原状态——升级前保护开着的话,期间断网几秒。

安装之后:
  打开菜单栏的 bx 图标,选择 Set Up bx... 继续配置。

卸载:
  sudo bx uninstall
  (详见 ./uninstall.sh)

Notes:
  install.sh 需要以你的普通 macOS 用户身份运行(会在需要时通过 sudo 请求一次管理员授权)。
  覆盖安装到一台**已经装过 bx**(Guardian 服务已加载,无论保护是否开启)的机器上时
  会先问你一次;无终端的场景(非交互 SSH、CI)问不出来,install.sh 会报错中止而不是
  假装装好——确认要升级就跑 ./install.sh --yes。
  install.sh 不会执行 bx setup(你的连接配置一个字都不改)。
  全新安装不启动保护、不修改 DNS/路由;覆盖安装到一台已经装过 bx 的机器上时(无论保护是否开启),
  安装会在你确认后重启保护——DNS 与路由随之被重新接管,这是恢复保护的必然结果。
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
