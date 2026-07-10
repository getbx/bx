//go:build windows

// service_windows.go 是 bx 进程侧的 Windows Service SCM 握手:当 bx.exe 由服务控制管理器(SCM)
// 作为服务拉起时,进程必须实现 svc.Handler、及时上报 Running/Stopped,否则 SCM 判定启动超时而杀。
// 服务的安装/管理(create/start/stop/delete)在 internal/install/service_windows.go。
package cli

import (
	"context"
	"log"
	"os"

	"golang.org/x/sys/windows/svc"
)

// bxServiceName 须与 install.windowsServiceName 一致(SCM 里的服务名)。
const bxServiceName = "bx"

// serviceLogPath:服务无控制台,stderr 丢弃 → 日志重定向到此文件,便于诊断/审计。
const serviceLogPath = `C:\ProgramData\bx\service.log`

// isWindowsService 报告当前进程是否由 SCM 作为服务拉起(而非用户在控制台跑 bx run 调试)。
func isWindowsService() bool {
	is, err := svc.IsWindowsService()
	return err == nil && is
}

// runAsWindowsService 在 SCM 会话下运行 run:Start 时后台跑 run 并上报 Running;收到 Stop/Shutdown
// 时 cancel ctx,让 supervisor.Run 经 ctx.Done 走 defer **全量还原**(拆路由劫持、关 TUN、撤 WFP),
// 还原跑完再上报 Stopped——与 SIGINT/SIGTERM 的关机路径同源,不漏还原。
func runAsWindowsService(run func(context.Context) error) error {
	// 服务无控制台、stderr 无处可去 → 把日志重定向到文件(否则服务失败时无从诊断)。
	if f, err := os.OpenFile(serviceLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	log.Printf("bx service: 由 SCM 拉起,开始运行")
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
			// 服务无控制台,CLI 的 stderr 报错会丢失 → 在此显式记进 service.log,否则失败无从查。
			if err != nil {
				log.Printf("bx service: run 退出错误: %v", err)
				s <- svc.Status{State: svc.Stopped}
				return false, 1
			}
			log.Printf("bx service: run 正常退出")
			s <- svc.Status{State: svc.Stopped}
			return false, 0
		}
	}
}
