//go:build windows

package supervisor

// bx 运行期文件:Windows 用 ProgramData 下固定路径,本就是 bx 自有目录(非共享
// 系统目录),故与 linux/darwin 的「自有运行时子目录」不变量天然一致。
// AF_UNIX socket 在 Windows 10 1803+ 支持(Go 亦支持),故仍用 unix socket 做状态面。
const (
	RuntimeDir = `C:\ProgramData\bx`
	SockPath   = RuntimeDir + `\bx.sock`
	PidPath    = RuntimeDir + `\bx.pid`
)
