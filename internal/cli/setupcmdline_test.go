package cli

import (
	"strings"
	"testing"
)

// **bx 不许教用户敲一条 bx 自己会拒绝的命令。**
//
// 真机(2026-08-14):`bx server deploy` 打出的下一步是
// `sudo bx setup 'bx://MAIN' --udp 'bx://UDP'`,照着敲直接被 checkSetupArgs 拒绝 ——
// 而那个顺序是从 `bx server install` 的输出里抄来的,**两处都在教人敲错的命令**。
//
// 更讽刺的是 `checkSetupArgs` 的注释里就写着这在 2026-08-06/07 的真机上栽过:
// 加守卫的人没去修**生成**那条命令的地方。
//
// 判据不是文本匹配,而是**把渲染出来的参数喂给那道守卫本人** —— 守卫的规则
// 将来怎么改,这条测试跟着走。
func TestGeneratedSetupCommandPassesItsOwnGuard(t *testing.T) {
	for _, tc := range []struct{ name, main, udp string }{
		{"主 + UDP", "bx://MAIN", "bx://UDP"},
		{"只有主链接", "bx://MAIN", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := setupCommandLine(tc.main, tc.udp)
			if !strings.Contains(line, "bx setup") {
				t.Fatalf("渲染出来的不像一条 setup 命令:%s", line)
			}
			if err := checkSetupArgs(positionalArgsOf(line)); err != nil {
				t.Fatalf("bx 生成的命令被 bx 自己拒绝了:\n  命令:%s\n  拒绝理由:%v", line, err)
			}
		})
	}
}

// 反面:旧的那个顺序**确实**会被守卫拒绝 —— 否则上面那条测试可以靠
// 「守卫从不拒绝任何东西」满足。
func TestTheOldArgumentOrderIsStillRejected(t *testing.T) {
	bad := "sudo bx setup 'bx://MAIN' --udp 'bx://UDP'"
	if err := checkSetupArgs(positionalArgsOf(bad)); err == nil {
		t.Fatal("旧顺序没被拒绝 —— 那条守卫已经不起作用了,上面那条测试也就没有意义")
	}
}

// positionalArgsOf 按 Go flag 包的语义算出「urfave 会把哪些当成位置参数」:
// **遇到第一个非 flag 的 token 就停止解析 flag**,此后全是位置参数。
// 这正是 checkSetupArgs 注释里描述的那条语义。
func positionalArgsOf(commandLine string) []string {
	fields := strings.Fields(commandLine)
	// 跳到 `setup` 之后
	for i, f := range fields {
		if f == "setup" {
			fields = fields[i+1:]
			break
		}
	}
	var positional []string
	stopped := false
	for i := 0; i < len(fields); i++ {
		token := strings.Trim(fields[i], "'\"")
		switch {
		case stopped:
			positional = append(positional, token)
		case strings.HasPrefix(fields[i], "-"):
			i++ // flag 的值
		default:
			stopped = true
			positional = append(positional, token)
		}
	}
	return positional
}
