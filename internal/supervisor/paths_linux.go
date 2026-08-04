//go:build linux

package supervisor

// bx 运行期文件:状态查询用的 unix socket 与进程 pid 文件。socket 路径是信任边界
// (peer-cred 鉴权、0666 权限对外暴露状态面),而 /run 是内核/系统共享的根目录、
// 属主与权限不受 bx 掌控——对它做 chmod/校验既越权又可能因属主不是 euid 而硬失败。
// 故与 darwin(/var/run/bx)同构,bx 落在自有子目录 /run/bx 下,只对这个子目录
// 做 secdir.Ensure 校验,不触碰共享父目录。
const (
	RuntimeDir = "/run/bx"
	SockPath   = RuntimeDir + "/core.sock"
	PidPath    = RuntimeDir + "/core.pid"
)
