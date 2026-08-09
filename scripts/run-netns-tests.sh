#!/usr/bin/env bash
# 在本机的 Linux 容器里跑 netns 集成测试(macOS 开发机用)。
#
# 为什么不是 `sudo go test -tags integration`:这些测试要 unshare(CLONE_NEWNET)、
# 挂 tmpfs、建真 TUN、装策略路由 —— macOS 上一个都跑不了,而在开发机上 sudo 跑它们
# 也绝不可取(宿主可能正开着 bx)。
#
# 为什么不是 golang 镜像:交叉编译在宿主几秒就好,容器只负责**跑**。busybox 6MB、
# 自带的 ip 输出格式与 iproute2 一致,不用拉任何大镜像(在被代理的网络上这一点很实在)。
#
# 用法:  bash scripts/run-netns-tests.sh [-test.run 正则] [其它 go test 标志]
# 例:    bash scripts/run-netns-tests.sh -test.run TestHarness -test.v
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 必须落在 Colima 共享得到的路径下:macOS 的 /tmp 不在共享范围内,
# 挂进去会变成一个空目录,报 "is a directory: permission denied"。
OUT="$HOME/.cache/bx-harness"
ARCH="$(uname -m)"; [ "$ARCH" = "x86_64" ] && GOARCH=amd64 || GOARCH=arm64

command -v docker >/dev/null || { echo "✗ 需要 docker(本项目开发机用 colima)" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "✗ docker 不可用;先 'colima start'" >&2; exit 1; }

mkdir -p "$OUT"
echo "⏳ 交叉编译 supervisor 集成测试 (linux/$GOARCH) …"
GOOS=linux GOARCH="$GOARCH" go test -c -tags integration -o "$OUT/supervisor.test" "$ROOT/internal/supervisor"

# 源码也要**只读**挂进去并把工作目录设成包目录:本包有若干读源码的守卫测试
# (读 run.go、找 go.mod),只挂二进制的话它们会以 "no such file" 失败 ——
# 那种失败与真实回归长得一模一样,会让这个脚本的红灯失去意义。
echo "⏳ 在特权容器里跑(--rm,一次性;宿主的路由/DNS 碰不到)…"
exec docker run --rm --privileged \
	-v "$OUT":/h:ro -v "$ROOT":/src:ro -w /src/internal/supervisor \
	busybox /h/supervisor.test "$@"
