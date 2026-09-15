// Package elevate 回答一个问题:**在这个平台上,「以管理员身份跑这条 bx 命令」
// 写出来是什么样。**
//
// **它是叶子包,不引本仓库任何东西**(理由同 internal/udpsource、
// internal/barriercidr、internal/dirsync):同一句 `sudo bx up` 此前散在 37 个
// 文件里,而 Windows 上**根本没有 sudo** —— 2026-09-15 在 `030-SJWJ-GSR-B`
// 上逐条抓到:`bx explain`、`bx status`、`bx doctor`(三处)、`bx leak-check`
// 全都在让人敲一条 `'sudo' 不是内部或外部命令` 的东西。
//
// **每一处的唯一目的都是让人照着敲**,所以这不是措辞讲究:本仓库为
// `bx force-teardown` 写过同一句话 —— 读到它的人正处在最需要它管用的时刻。
//
// 两个出口刻意分开:
//
//   - Cmd 返回**一条能粘贴的命令**,一个字都不许多;
//   - Note 返回那句「它需要管理员权限」,**只在有散文的地方**用 —— 拼进命令串里
//     就等于让每一条 hint 都粘贴不了(而 hint 常常是 JSON 里的一个字段)。
package elevate

// Cmd 把一条 bx 命令写成「在本平台上以管理员身份跑它」的样子。
func Cmd(command string) string { return cmdWith(Prefix, command) }

// Note 是那句「这条命令需要管理员权限」;有 sudo 的平台上是空串 ——
// `sudo` 三个字母自己就说清了,再加一句就是每条提示都多一行废话。
func Note() string { return note }

func cmdWith(pfx, command string) string {
	if command == "" {
		// 空命令加前缀会变成一条光秃秃的 `sudo`,粘贴过去只会进交互模式。
		return ""
	}
	return pfx + command
}
