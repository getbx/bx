//go:build windows

// service_windows.go 是 bx 进程侧的 Windows Service SCM 握手:当 bx.exe 由服务控制管理器(SCM)
// 作为服务拉起时,进程必须实现 svc.Handler、及时上报 Running/Stopped,否则 SCM 判定启动超时而杀。
// 服务的安装/管理(create/start/stop/delete)在 internal/install/service_windows.go。
package cli

import (
	"context"

	"golang.org/x/sys/windows/svc"
)

// bxServiceName 须与 install.windowsServiceName 一致(SCM 里的服务名)。
const bxServiceName = "bx"

// isWindowsService 报告当前进程是否由 SCM 作为服务拉起(而非用户在控制台跑 bx run 调试)。
func isWindowsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// runAsWindowsService 在 SCM 会话下运行 run:Start 时后台跑 run 并上报 Running;收到 Stop/Shutdown
// 时 cancel ctx,让 supervisor.Run 经 ctx.Done 走 defer **全量还原**(拆路由劫持、关 TUN、撤 WFP),
// 还原跑完再上报 Stopped——与 SIGINT/SIGTERM 的关机路径同源,不漏还原。
func runAsWindowsService(run func(context.Context) error) error {
	return svc.Run(bxServiceName, &bxService{run: run})
}

type bxService struct {
	run func(context.Context) error
}

func (h *bxService) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.run(ctx) }()

	s <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel() // 触发 supervisor.Run 的 defer 全量还原
				<-errCh  // 等还原跑完(拆路由/TUN/WFP)再上报 Stopped
				s <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-errCh:
			// run 自行退出(隧道致命错误 / 死手到点):停服务并透出退出码。
			s <- svc.Status{State: svc.Stopped}
			if err != nil {
				return false, 1
			}
			return false, 0
		}
	}
}
