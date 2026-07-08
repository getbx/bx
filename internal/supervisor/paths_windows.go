//go:build windows

package supervisor

// bx 运行期文件:Windows 用 ProgramData 下固定路径。
// AF_UNIX socket 在 Windows 10 1803+ 支持(Go 亦支持),故仍用 unix socket 做状态面。
const (
	SockPath = `C:\ProgramData\bx\bx.sock`
	PidPath  = `C:\ProgramData\bx\bx.pid`
)
