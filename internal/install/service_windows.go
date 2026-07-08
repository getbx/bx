//go:build windows

// service_windows.go 用 golang.org/x/sys/windows/svc/mgr 把 bx 装成 Windows Service
// (systemd/launchd 的对应物)。安装/管理需管理员;服务以 LocalSystem 跑(TUN/路由/WFP 权限)。
// 服务进程侧的 SCM 握手(svc.Run handler)在 internal/cli/service_windows.go。
package install

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// windowsServiceName 是 SCM 里的服务名(区别于 systemd 的 "bx.service")。
const windowsServiceName = "bx"

// windowsInstallService 建服务(手动启动、不启动;up 再设自启+启动,对齐 setup「只装不启」)。
// execStart 是带引号的完整命令行,拆成 exepath+args 喂 CreateService(它内部再正确加引号)。
// 已存在则先停删重建(幂等重装,对齐 setup 可重跑)。
func windowsInstallService(execStart string) error {
	fields := commandLineFields(execStart)
	if len(fields) == 0 {
		return fmt.Errorf("空的服务启动命令: %q", execStart)
	}
	exepath, args := fields[0], fields[1:]
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需管理员): %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(windowsServiceName); err == nil {
		_ = stopAndWait(s)
		_ = s.Delete()
		s.Close()
	}
	s, err := m.CreateService(windowsServiceName, exepath, mgr.Config{
		DisplayName:      "bx 透明全局代理",
		Description:      "bx transparent global proxy (TUN + split routing)",
		StartType:        mgr.StartManual,
		ServiceStartName: "", // LocalSystem
	}, args...)
	if err != nil {
		return fmt.Errorf("创建服务(需管理员): %w", err)
	}
	s.Close()
	return nil
}

// windowsEnableService 设开机自启并启动(bx up)。
func windowsEnableService() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("读服务配置: %w", err)
	}
	cfg.StartType = mgr.StartAutomatic
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("设开机自启: %w", err)
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动服务: %w", err)
	}
	return nil
}

// windowsDisableService 停止并取消开机自启(bx down)。
func windowsDisableService() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	_ = stopAndWait(s)
	if cfg, err := s.Config(); err == nil {
		cfg.StartType = mgr.StartDisabled
		_ = s.UpdateConfig(cfg)
	}
	return nil
}

// windowsRestartService 重启(不改自启状态)。
func windowsRestartService() error {
	m, s, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	_ = stopAndWait(s)
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动服务: %w", err)
	}
	return nil
}

// windowsUninstallService 停止并删除服务(不存在即视为已卸)。
func windowsUninstallService() error {
	m, s, err := openService()
	if err != nil {
		return nil
	}
	defer m.Disconnect()
	defer s.Close()
	_ = stopAndWait(s)
	if err := s.Delete(); err != nil {
		return fmt.Errorf("删除服务: %w", err)
	}
	return nil
}

// windowsServiceInstalled 报告服务是否已注册。
func windowsServiceInstalled() bool {
	m, s, err := openService()
	if err != nil {
		return false
	}
	m.Disconnect()
	s.Close()
	return true
}

// windowsServiceExecCmd 读服务 BinaryPathName 取子命令(up 防呆:须为 "run")。
func windowsServiceExecCmd() (string, error) {
	m, s, err := openService()
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return "", fmt.Errorf("读服务配置: %w", err)
	}
	return serviceSubcommand(cfg.BinaryPathName), nil
}

// windowsServiceState 返回 is-active/is-enabled 风格状态(供 status 呈现)。
func windowsServiceState(action string) string {
	m, s, err := openService()
	if err != nil {
		if action == "is-enabled" {
			return "disabled"
		}
		return "inactive"
	}
	defer m.Disconnect()
	defer s.Close()
	switch action {
	case "is-active":
		if st, err := s.Query(); err == nil && st.State == svc.Running {
			return "active"
		}
		return "inactive"
	case "is-enabled":
		if cfg, err := s.Config(); err == nil && cfg.StartType == mgr.StartAutomatic {
			return "enabled"
		}
		return "disabled"
	}
	return "unknown"
}

func openService() (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("连接服务管理器(需管理员): %w", err)
	}
	s, err := m.OpenService(windowsServiceName)
	if err != nil {
		m.Disconnect()
		return nil, nil, fmt.Errorf("打开服务 %s(未安装?): %w", windowsServiceName, err)
	}
	return m, s, nil
}

// stopAndWait 发 Stop 并轮询到 Stopped(最多 ~10s)。未在运行时 Control 返回错误,视为已停。
func stopAndWait(s *mgr.Service) error {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return nil // 多半本就没跑
	}
	deadline := time.Now().Add(10 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务停止超时")
		}
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}
