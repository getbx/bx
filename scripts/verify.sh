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

# **版本必须与 CI 钉死的那个一致,而这条 2026-09-15 才补上。** 在那之前这里读的是
# `$(go env GOPATH)/bin/gofumpt` —— 谁装的哪一版就是哪一版,而 CI 钉的是下面这个。
# 实测两版真的会打架(v0.10.0 要求多行实参表带尾逗号 + 右括号独占一行,v0.11.0
# 放松了这条):本机 v0.11.0 判无漂移、CI v0.10.0 判七个文件漂移,**于是本机这道
# 闸门恒绿而 CI 那道恒红** —— 而这个仓库的全部「已验证」都只由本机这一份背书。
# **一道与它要预演的那道守着不同标准的闸门,比没有这道闸门更糟。**
# 两处 pin 还一不一样由 TestVerifyScriptPinsTheSameGofumptAsCI 钉住。
GOFUMPT_VERSION="v0.10.0"

# gofumpt 输出的是**文件名列表**,退出码恒为 0 —— 这是全脚本唯一一处退出码不够用
# 的地方,故显式把「输出非空」转成失败。
gofumpt_check() {
	local drift
	# **`--others` 不能省。** 只问 `git ls-files` 就只看得见**已跟踪**的文件,
	# 于是一个新文件在它最需要被检查的那一次(提交之前)是隐形的,提交之后才
	# 头一回被看见 —— 实测栽过:本轮两个新测试文件带着格式漂移过了这道闸门,
	# 提交之后的下一次 verify 才转红。
	drift="$(git ls-files --cached --others --exclude-standard '*.go' | grep -v '^internal/embedded/assets/' | grep -v '^internal/winfw/' | xargs go run "mvdan.cc/gofumpt@$GOFUMPT_VERSION" -l)"
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
	# 上面那圈 `go build` **不编 _test.go**,而 Windows 那半的行为断言只在 CI 的
	# windows runner 上才跑 —— 也就是说一个写坏的 `*_windows_test.go` 在本地
	# 一路绿灯,推上去才红。这一步把带 windows-tagged 测试的包单独 vet 一遍
	# (vet 会 typecheck 测试文件)。
	#
	# **刻意不写成 `GOOS=windows go vet ./...`**:那样今天就会撞上
	# internal/tray/win_windows.go 里一条先于此存在的 unsafe.Pointer 告警,
	# 变成一道恒红的闸门 —— 而恒红的闸门会被下一个人删掉。
	windows_test_typecheck() {
		local pkgs
		pkgs="$(git ls-files '*_windows_test.go' | xargs -n1 dirname 2>/dev/null | sort -u | sed 's|^|./|')"
		if [ -z "$pkgs" ]; then
			echo "一个 *_windows_test.go 都没找到 —— 这一步此刻什么都不检查;"
			echo "要么是那些测试没了(那就把这一步一起删掉),要么是判据认不出它们了。"
			return 1
		fi
		# shellcheck disable=SC2086
		env GOOS=windows GOARCH=amd64 go vet $pkgs
	}
	step "windows test files typecheck" windows_test_typecheck
	# **手机那半的地基:纯判据包必须保持可移植。**
	#
	# bx 要不要有手机端还没定(见 docs/superpowers/specs/2026-09-17-mobile-client-design.md),
	# 但有一件事现在就该钉住:**桌面的改动不许悄悄把那条路堵死**。手机上传输与路由
	# 都会是上游的 libbox,bx 的数据面一行都用不上;真正能原样搬过去的是那几个
	# **纯判据**包(泄漏检测、路径解释、规则体检、doctor)—— 它们按构造不做 I/O,
	# 编得过就等于能用(对照:supervisor/tun 也"编得过",但那只是落到了 _other.go
	# 的桩上,是个假绿)。
	#
	# 清单从 git ls-files 现取(与 windows 腿同一条纪律),一个都找不到时响亮失败。
	# 两个目标都免 CGO,所以 Linux 上也跑得了。
	#
	# **这道闸门的力量有明确上限,别把它读成「手机上能用」**(2026-09-17 实测):
	# `GOOS=ios` **满足 `darwin` 构建标签**(go list 实测:ios 构建包含 a_darwin.go),
	# 于是 platform_darwin.go 那种 `exec.Command("route", …)` / networksetup / launchd
	# 的代码会被**原样编进 iOS 构建** —— 编得过,运行时全挂(iOS 上没有 /sbin/route,
	# 更不许起进程)。同理 android 满足 linux。
	# 所以它拦得住的是「引入了一个在 ios/android 上**编不过**的依赖」(cgo、
	# 显式 !ios 约束之类),拦不住「编得过但跑不了」。
	# 真正防住后者的是那几个包自己的 purity_test.go(按 AST 禁 net/os/exec),
	# 以及 leakcheck 那条 go list -deps 的传递依赖守卫。三者各守一段,缺一不可。
	portable_judgment_build() {
		local pkgs target
		pkgs="$(git ls-files '*purity_test.go' | xargs -n1 dirname 2>/dev/null | sort -u | sed 's|^|./|')"
		if [ -z "$pkgs" ]; then
			echo "一个 purity_test.go 都没找到 —— 要么纯判据包没了(那就把这一步删掉),"
			echo "要么判据认不出它们了。"
			return 1
		fi
		for target in ios/arm64 android/arm64; do
			# shellcheck disable=SC2086
			env CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" go build $pkgs || return 1
		done
	}
	step "portable judgment (ios+android)" portable_judgment_build
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
		# 菜单窗口的**离屏快照**。这半边(AppKit)此前在 CI 里一行测试都盖不到,
		# 于是「按钮跑到窗口外面去了」只能靠人盯着屏幕发现 —— 而那正是本仓库
		# 「真机未验」清单里最长的一段。判据钉视图树(PNG 只给人看,像素比对
		# 换个系统版本就全红),脚本没有 WindowServer 时明说 SKIPPED。
		# **两个条件都要满足**,理由与 menu_tests 那段一字不差。
		menu_snapshots() {
			bash scripts/snapshot-macos-menu.sh >/dev/null || return 1
			bash scripts/snapshot-macos-menu.sh 2>/dev/null \
				| grep -qE '^(macOS menu snapshots passed|SKIPPED:)' \
				|| { echo "快照脚本未跑到收尾横幅 —— 可能中途 exit 0 而一个窗口都没渲染"; return 1; }
		}
		step "macos menu snapshots" menu_snapshots
	else
		skip "swift build + menu suites" "swift 未安装"
	fi
else
	skip "swift build + menu suites" "非 macOS"
fi

# leakcheck 页面里那段**纯解析** JS。Go 测试进不去那半边,与 Swift 同一个形状:
# 单独一个运行器,这里挂闸门。跨平台跑(node 在三个 CI runner 上都预装)。
#
# **两个条件都要满足,理由与上面 menu_tests 那段一字不差**:退出码证明「没失败」,
# 收尾横幅证明「真跑过」—— 脚本被清空或提前 exit 0 时只有横幅抓得住。
if command -v node >/dev/null 2>&1; then
	page_js_tests() {
		bash scripts/test-page-js.sh || return 1
		bash scripts/test-page-js.sh 2>/dev/null | grep -q '^page js tests passed$' \
			|| { echo "页面 JS 测试脚本未跑到收尾横幅 —— 可能中途 exit 或断言块被清空"; return 1; }
	}
	step "leakcheck page js" page_js_tests
else
	skip "leakcheck page js" "node 未安装"
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
