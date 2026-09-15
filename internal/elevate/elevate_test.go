package elevate

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// —— Windows 上没有 sudo,而 bx 一直在让人敲它(2026-09-15 真机实测)——
//
// `030-SJWJ-GSR-B` 上逐条抓到:`bx explain` 说「要看 bx 的判定先 sudo bx up」、
// `bx status` 说「启动:sudo bx up」、`bx doctor` 三处 HINT、`bx leak-check`
// 的「下一步」。**每一处的唯一目的都是让人照着敲**,而照着敲的结果是
// `'sudo' 不是内部或外部命令`。本仓库为 `bx force-teardown` 写过同一句话:
// 读到它的人正处在最需要它管用的时刻,一句命令不存在是最坏的伤害。
//
// 判定收进这个叶子包(不引本仓库任何东西),37 个文件共用一份 —— 与
// internal/udpsource、internal/barriercidr、internal/dirsync 同一条:
// **唯一需要逐字对齐的东西就该只有一份。**

// **提权前缀是平台事实,而判据要在任何平台上都测得了。**
// 两个分支都由纯函数覆盖,不靠「恰好在这台机器上跑」。
func TestCmdPrefixesOnlyWhereThePlatformHasSudo(t *testing.T) {
	if got := cmdWith("sudo ", "bx up"); got != "sudo bx up" {
		t.Errorf("有 sudo 的平台上 = %q,want %q", got, "sudo bx up")
	}
	if got := cmdWith("", "bx up"); got != "bx up" {
		t.Errorf("没有 sudo 的平台上 = %q —— 印一个不存在的命令是这个包要消灭的东西", got)
	}
}

// **返回的必须是一条能粘贴的命令,一个字都不许多。**
// 把「需要管理员」拼进命令串里,粘贴过去就报错 —— 那句话归 Note()。
func TestCmdNeverCarriesProse(t *testing.T) {
	for _, prefix := range []string{"sudo ", ""} {
		got := cmdWith(prefix, "bx up")
		for _, forbidden := range []string{"(", "(", "管理员", "PowerShell", "以"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("命令串里混进了散文 %q:%q —— 它是用来粘贴的", forbidden, got)
			}
		}
	}
}

// 本平台那份常量必须与本平台的事实一致 —— 两个分支都测过了,这条钉的是「接对了哪一个」。
func TestThisPlatformGetsTheRightPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		if Prefix != "" {
			t.Errorf("windows 上前缀是 %q —— 那个命令不存在", Prefix)
		}
		if Note() == "" {
			t.Error("windows 上没有那句「要在管理员 PowerShell 里跑」—— 裸命令自己说不出它需要提权")
		}
		return
	}
	if Prefix != "sudo " {
		t.Errorf("类 Unix 平台上前缀是 %q,want %q", Prefix, "sudo ")
	}
	// **类 Unix 平台上那句话必须是空的**:`sudo` 三个字母自己就说清了要提权,
	// 再加一句就是每条提示都多一行废话。
	if Note() != "" {
		t.Errorf("类 Unix 平台上多出了一句 %q", Note())
	}
}

// 空命令不许返回一个光秃秃的 "sudo " —— 那是一条粘贴过去只会进交互模式的东西。
func TestCmdSaysNothingForAnEmptyCommand(t *testing.T) {
	for _, prefix := range []string{"sudo ", ""} {
		if got := cmdWith(prefix, ""); got != "" {
			t.Errorf("空命令返回了 %q", got)
		}
	}
}

// **Note() 必须有生产调用方。**
//
// 一个没人调用而测试盖着的函数,与没有这个功能在输出上完全一样 —— 本仓库
// 编号的第三种守卫失效,而这一支的起点(`LooksLikeOurFault` 零调用方)正是它。
// Note() 在有 sudo 的平台上返回空串,于是「没接上」在本机测试里**完全看不见**;
// 真机上的后果是 Windows 用户读到一条裸命令、而没有任何东西告诉他要提权。
func TestNoteIsActuallyRendered(t *testing.T) {
	out, err := exec.Command("git", "-C", filepath.Join("..", ".."), "grep", "-l", "elevate.Note()", "--", "*.go").Output()
	if err != nil {
		t.Fatalf("查不到调用方:%v —— 这条守卫读不懂现在的仓库了,先修它", err)
	}
	var production []string
	for _, f := range strings.Fields(string(out)) {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "internal/elevate/") {
			continue
		}
		production = append(production, f)
	}
	if len(production) == 0 {
		t.Error("elevate.Note() 没有任何生产调用方 —— 它在有 sudo 的平台上返回空串," +
			"于是「没接上」在本机测试里完全看不见,而 Windows 用户会读到一条裸命令")
	}
}
