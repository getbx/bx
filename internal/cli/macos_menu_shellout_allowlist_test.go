package cli

import (
	"sort"
	"strings"
	"testing"
)

// **这是 spec §1 那张表的不变量。** 菜单上只有两类动作允许提权 / 开终端:
// Guardian 不存在时也必须能跑的(install / repair / uninstall / update / 逃生口 /
// 旧 Guardian 上的降级路),与必须走终端归档的(Export Diagnostics)。加进来可以,
// 悄悄加不行 —— 与 destPublicationAllowlist(§ appattr 发布面)同款:每一条写理由。
var menuShellOutAllowlist = map[string]string{
	"beginSetup":           "首次 setup 要写 /etc/bx 与装 unit,Guardian 还不存在",
	"runEmbeddedInstaller": "install / repair 要换掉 Guardian 自己",
	"uninstallBx":          "卸载要停掉并删掉 Guardian",
	"updateBx":             "更新要停掉 Guardian 换二进制",
	"exportDiagnostics":    "诊断包归档写用户目录并 chown,一年一次,走终端(spec §1)",
	"performToggle":        "Turn Off 的逃生口:Guardian 死了也要能关掉保护(2026-08-04 那 71 分钟)",
	"replaceConfiguration": "旧 Guardian(没有 servers 能力)的降级路;新 Guardian 上菜单不画它,改用 Add Server",
}

// TestMacMenuShellOutsStayOnTheAllowlist 钉住:main.swift 里 `runPrivileged(`、
// `openTerminal(`、`runPrivilegedScriptOffMainThread(` 的调用点只能落在
// menuShellOutAllowlist 那七个函数里。菜单的绝大多数动作(Turn On、加规则、切服务器、
// 跑体检……)从 Task 1-7 起都经 Guardian 的 owner 门,不再 shell out;这条守卫防的是
// 悄悄给某个新动作补一条 `runPrivileged(...)` 而不经过那道门。
//
// 判据复用 swiftFunctionDefs/enclosingSwiftFunc(与 TestMacMenuSpawnsOnlyFromTheActionPath
// 同一对辅助函数),不另写一个包围函数扫描器——两条守卫读的是同一份「main.swift 里
// 函数体的字节区间」,写第二份就是给漂移开了一道口子。
func TestMacMenuShellOutsStayOnTheAllowlist(t *testing.T) {
	code := menuMainSwiftCode(t)
	defs := swiftFunctionDefs(code)
	if len(defs) == 0 {
		t.Fatal("一个函数定义都没枚举到 —— 守卫读不懂现在的 main.swift")
	}

	var offenders []string
	seen := map[string]bool{}
	for _, needle := range []string{"runPrivileged(", "openTerminal(", "runPrivilegedScriptOffMainThread("} {
		from := 0
		for {
			i := strings.Index(code[from:], needle)
			if i < 0 {
				break
			}
			at := from + i
			from = at + len(needle)
			// `private func runPrivileged(` 是定义不是调用点:enclosingSwiftFunc 会把
			// 它算到外层函数(或顶层)。按前面紧挨的 `func` 跳过,与
			// TestMacMenuSpawnsOnlyFromTheActionPath 同一个判据。
			if strings.HasSuffix(strings.TrimRight(code[:at], " \t"), "func") {
				continue
			}
			fn := enclosingSwiftFunc(defs, at)
			if fn == "" {
				fn = "<top-level>"
			}
			seen[fn] = true
			if _, ok := menuShellOutAllowlist[fn]; !ok {
				offenders = append(offenders, fn+" → "+needle)
			}
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("main.swift 里有白名单之外的提权/开终端调用:%v —— 菜单的动作应经 Guardian 的 "+
			"owner 门;确实必须留在 CLI 的(Guardian 不存在时也要能跑 / 必须走终端归档)"+
			"把函数名连同理由加进 menuShellOutAllowlist", offenders)
	}

	// 反向:白名单里不许有陈旧条目(函数已不存在,或已经不再 shell-out)。
	for fn := range menuShellOutAllowlist {
		if !seen[fn] {
			t.Errorf("白名单条目 %q 已经没有任何 shell-out 落在它里面 —— 删掉它,别让清单说假话", fn)
		}
	}
}
