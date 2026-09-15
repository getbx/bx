//go:build !windows

package elevate

const (
	// Prefix 是拼在一条 bx 命令前面的提权前缀。**它是常量**,所以拼进
	// `const` 块与任何常量表达式里都合法 —— 162 处替换因此是机械安全的:
	// 只换 `sudo ` 那四个字符,不用去猜命令在散文里到哪儿结束。
	Prefix = "sudo "
	// note 为空:`sudo` 自己就说清了要提权,再加一句就是每条提示多一行废话。
	note = ""
)
