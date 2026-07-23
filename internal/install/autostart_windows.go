//go:build windows

// autostart_windows.go 治理"开机自启"两处:服务 StartType(SCM)+ 托盘登录自启(HKCU Run)。
// 一个 SetAutostart 原子改两处,避免"服务自启开、图标自启关"错位。
package install

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// hkcuRunKey 是当前用户登录自启注册表路径;runValueName 是 bx 的值名。
const (
	hkcuRunKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValueName = "bx"
)

// SetAutostart 原子设置开机自启:enabled → 服务 StartAutomatic + 写 HKCU Run;
// !enabled → 服务 StartManual(非 Disabled,仍可手动/up 起)+ 删 HKCU Run。需管理员(改 SCM 配置)。
func SetAutostart(enabled bool) error {
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
	if enabled {
		cfg.StartType = mgr.StartAutomatic
	} else {
		cfg.StartType = mgr.StartManual
	}
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("设服务 StartType: %w", err)
	}
	return setTrayLoginAutostart(enabled)
}

// setTrayLoginAutostart 写/删当前用户的托盘登录自启 HKCU Run 项(值 = `"<BinPath>" tray`)。
func setTrayLoginAutostart(enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, hkcuRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 HKCU Run: %w", err)
	}
	defer k.Close()
	if enabled {
		if err := k.SetStringValue(runValueName, `"`+BinPath+`" tray`); err != nil {
			return fmt.Errorf("写 HKCU Run: %w", err)
		}
		return nil
	}
	if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("删 HKCU Run: %w", err)
	}
	return nil
}

// AutostartEnabled 报告服务是否开机自启(StartType==StartAutomatic)。非提权只读,给托盘勾选框用。
func AutostartEnabled() bool {
	return ServiceState("is-enabled", windowsServiceName) == "enabled"
}
