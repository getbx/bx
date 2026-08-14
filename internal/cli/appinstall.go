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
//
// 它是个**不带任何断言的哨兵**:给用户看的那句话由 upgradeCannotAskMessage
// 按 desiredOn 生成。上一版把「会断网」写死在这里,而 stopsProtection 只要
// Guardian 处于 loaded 就为真(保护未开启时同样如此,那正是任何一次 bx down
// 之后的常态)—— 于是一次运行里会先打印「当前保护未开启,不会影响网络」,
// 紧接着报「会断网的升级」,自相矛盾。
var errCannotAsk = errors.New("非交互环境,无法确认升级")

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

// stdinIsTerminal 判断 stdin 是不是**真的终端**。
//
// 曾经判的是「字符设备」,而 `/dev/null` 与 `/dev/zero` 都是字符设备 ——
// 于是在 CI、cron、以及任何把 stdin 接到 /dev/null 的地方,这个函数返回 true:
// 一个非交互环境被当成可以向用户提问的终端。见 fdIsTerminal。
func stdinIsTerminal() bool {
	return fdIsTerminal(os.Stdin)
}
