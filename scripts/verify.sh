#!/usr/bin/env bash
# verify.sh — 提交前的全量验证。**判据一律是退出码,不是字符串匹配。**
#
# 为什么要有这个脚本:2026-08-11 那一轮开发里,同一个根因栽了三次 ——
#   ① `go test … | grep …; git commit` 用 `;` 串联,测试是红的而 commit 照样跑了;
#   ② 变异验证 grep `^failed`,而那个套件打印的是 `FAIL:`,于是「没转红」被误判成
#      守卫失效(实际上一直是红的);
#   ③ 审计脚本用 `head -5` 查 `set -e`,而有的脚本注释头有十几行 —— 代理指标不是事实。
# 三次都是**拿字符串匹配当判据**。把「验过了」变成一个能失败的命令,而不是一句话。
#
# 用法:
#   bash scripts/verify.sh            全量
#   bash scripts/verify.sh --quick    跳过 -race 与交叉编译(改一行时用)
#
# **刻意不用 `set -e`**:那样第一步失败就退出,而一次跑完知道全部坏了什么更有用
# (与 scripts/leak-test.sh 同一取舍)。代价是每一步都要自己收退出码,
# 最后一行 `exit $failed` 才是判据。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

QUICK=0
for arg in "$@"; do
	case "$arg" in
	--quick) QUICK=1 ;;
	*) echo "unknown flag: $arg" >&2; exit 2 ;;
	esac
done

failed=0
LOG="$(mktemp -t bx-verify)"
trap 'rm -f "$LOG"' EXIT

# step 跑一步,只在失败时吐日志。**退出码是唯一判据** —— 这里不 grep 输出。
step() {
	local name="$1"; shift
	printf '  %-34s' "$name"
	if "$@" >"$LOG" 2>&1; then
		echo "ok"
	else
		echo "FAILED"
		sed 's/^/      | /' "$LOG" | tail -25
		failed=$((failed + 1))
	fi
}

# skip 明确记录「这一步没跑」。**它不算通过,只是不算失败** ——
# 一个安静跳过的步骤与一个通过的步骤在输出里必须长得不一样,
# 否则「在 Linux 上跑了 verify」会被当成「macOS 那半也验过了」。
skipped=0
skip() {
	printf '  %-34s%s\n' "$1" "SKIPPED — $2"
	skipped=$((skipped + 1))
}

echo "bx verify  (root=$ROOT)"
echo

step "build" go build ./...
step "vet" go vet ./...
step "unit tests" go test ./... -count=1

# gofumpt 输出的是**文件名列表**,退出码恒为 0 —— 这是全脚本唯一一处退出码不够用
# 的地方,故显式把「输出非空」转成失败。缺工具必须响亮失败,不许静默跳过:
# 一个因为拿不到工具而自动通过的检查,与没有这个检查是同一回事。
gofumpt_check() {
	local bin="$(go env GOPATH)/bin/gofumpt"
	[ -x "$bin" ] || { echo "gofumpt 未安装:go install mvdan.cc/gofumpt@latest"; return 1; }
	local drift
	# **`--others` 不能省。** 只问 `git ls-files` 就只看得见**已跟踪**的文件,
	# 于是一个新文件在它最需要被检查的那一次(提交之前)是隐形的,提交之后才
	# 头一回被看见 —— 实测栽过:本轮两个新测试文件带着格式漂移过了这道闸门,
	# 提交之后的下一次 verify 才转红。
	drift="$(git ls-files --cached --others --exclude-standard '*.go' | grep -v '^internal/embedded/assets/' | grep -v '^internal/winfw/' | xargs "$bin" -l)"
	[ -z "$drift" ] || { echo "以下文件未格式化,请跑 gofumpt -w:"; echo "$drift"; return 1; }
}
step "gofumpt (no drift)" gofumpt_check

if [ "$QUICK" -eq 0 ]; then
	step "race detector (concurrent pkgs)" go test -race -count=1 \
		./internal/guardian ./internal/supervisor ./internal/leakserve ./internal/leakcheck
	for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64; do
		step "cross build $target" env GOOS="${target%/*}" GOARCH="${target#*/}" \
			go build -o /dev/null ./...
	done
else
	skip "race detector" "--quick"
	skip "cross builds" "--quick"
fi

# macOS 那一半。**CI 里 Swift 侧一度整个不跑而全绿**,所以这里两步都要:
#   swift build 只编 SwiftPM target(Sources/),Tests/ 下的文件不属于任何 target;
#   test-macos-menu.sh 用 swiftc 直编直跑那些测试。两者不可互相替代。
if [ "$(uname -s)" = "Darwin" ]; then
	if command -v swift >/dev/null 2>&1; then
		step "swift build (menu app)" swift build --package-path apps/macos/BxMenu
		# **这一步是全脚本唯一一处 grep 参与判据,而且是必要的**:脚本提前 `exit 0`
		# 或 run_test 块被删光时,一个套件都没跑却照样退 0(实测过)。退出码证明
		# 「没失败」,横幅证明「真跑过」—— 两者缺一不可,故两个条件都要满足。
		menu_tests() {
			bash scripts/test-macos-menu.sh || return 1
			bash scripts/test-macos-menu.sh 2>/dev/null | grep -q '^macOS menu tests passed$' \
				|| { echo "菜单测试脚本未跑到收尾横幅 —— 可能中途 return 或套件被清空"; return 1; }
		}
		step "swift menu test suites" menu_tests
	else
		skip "swift build + menu suites" "swift 未安装"
	fi
else
	skip "swift build + menu suites" "非 macOS"
fi

echo
if [ "$failed" -ne 0 ]; then
	echo "✗ verify FAILED — $failed step(s)"
	exit 1
fi
if [ "$skipped" -ne 0 ]; then
	echo "✓ verify passed, but $skipped step(s) were skipped — 那部分没有被验证"
	exit 0
fi
echo "✓ verify passed (all steps ran)"
