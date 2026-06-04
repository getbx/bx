package supervisor

// bx 运行期文件:状态查询用的 unix socket 与进程 pid 文件。
const (
	SockPath = "/run/bx.sock"
	PidPath  = "/run/bx.pid"
)
