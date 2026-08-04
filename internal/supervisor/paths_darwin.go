//go:build darwin

package supervisor

// bx 运行期文件:macOS 用 /var/run(/run 在 macOS 不存在)。Core 的控制 socket/pid
// 与 Guardian(internal/guardian.RuntimeDir)同迁进 bx 自有子目录,而非散落在
// /var/run 根下——目录本身经 secdir.Ensure 校验属主与权限,不受同目录下其它
// 进程占位干扰。
const (
	RuntimeDir = "/var/run/bx"
	SockPath   = RuntimeDir + "/core.sock"
	PidPath    = RuntimeDir + "/core.pid"
)
