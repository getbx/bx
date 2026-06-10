//go:build darwin

package supervisor

// bx 运行期文件:macOS 用 /var/run(/run 在 macOS 不存在)。
const (
	SockPath = "/var/run/bx.sock"
	PidPath  = "/var/run/bx.pid"
)
