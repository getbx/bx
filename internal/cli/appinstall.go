package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// bundleRootFromExecutable 从「Bx.app 内可执行文件路径」反推出 Bx.app 包根路径。
// 纯函数,darwin/other 共用(app-install 的 --app-source 缺省从自身可执行路径推导)。
func bundleRootFromExecutable(executable string) (string, error) {
	const marker = "/Bx.app/Contents/Resources/"
	idx := strings.Index(executable, marker)
	if idx < 0 {
		return "", fmt.Errorf("executable %q is not inside a Bx.app bundle; pass --app-source", executable)
	}
	return executable[:idx+len("/Bx.app")], nil
}

// errCannotAsk 表示**没能问出来**,区别于用户明确回答了「不」。
//
// 两者绝不可混为一谈:用户说不,是一次正常结束(退出码 0);问不出来却当成
// 「用户说不」而静静退出 0,会让上层脚本(生成的 install.sh 在 set -e 下紧接着
// 打印「完成」)把「什么都没做」报成成功 —— 那正是本期要消灭的形状:旧 daemon
// 继续跑着旧代码,而用户以为升级落地了。
var errCannotAsk = errors.New(
	"当前不是交互式终端,无法确认这次会断网的升级;" +
		"确认要升级请重跑并加 --yes(例如 ./install.sh --yes 或 bx app-install --yes)",
)

// confirmOnTTY 就地问用户一句「继续吗」。
// 返回 (是否同意, 是否问不出来)。
func confirmOnTTY(prompt string) (bool, error) {
	return confirmPrompt(os.Stdin, stdinIsTerminal(), os.Stdout, prompt)
}

// confirmPrompt 是 confirmOnTTY 的可注入内核(interactive 与 in/out 由调用方给)。
//
// **没有终端 = 问不出来 = 报错**,不是「继续」也不是「用户说不」。默认继续等于把
// 一次会断网的操作强加给每一个非终端调用方(脚本、CI、打包步骤、将来某个 shell
// 出去的 bx update),而且无从退出 —— 一个取消不掉的 --yes 不叫确认;默认「说不」
// 则让调用方分不清「用户拒绝了」和「根本没问成」,于是把没做的事报成做完了。
// 要在非交互环境里升级,调用方显式加 --yes 表态;菜单正是这么做的(它自己先弹了
// 一个 NSAlert)。
func confirmPrompt(in io.Reader, interactive bool, out io.Writer, prompt string) (bool, error) {
	if !interactive {
		fmt.Fprintf(out, "%s\n", prompt)
		return false, errCannotAsk
	}
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		// 读不到回答(EOF/中断)按取消处理:人在终端前却没答话,
		// 不能替他决定一次会断网的操作。
		fmt.Fprintln(out)
		return false, nil
	}
	return confirmationAccepted(answer), nil
}

// confirmationAccepted 只认明确的 y/yes;直接回车(默认)是取消。
func confirmationAccepted(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
