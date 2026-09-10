package doctor

// 服务三行的判据(darwin 上 Guardian 是 launchd 服务)。**从 internal/cli 原样搬来**:
// Guardian 的 /v1/doctor 采集要用同一份,而 cli 不能被 guardian import。

// DarwinGuardianServiceName 是 Guardian 的 launchd plist 标签。
//
// 统一布局下 Core 不是 launchd 服务(由 Guardian 起停),所以 doctor 绝不能去查
// install.UnitInstalled() 那两个 Core plist——那必然三条 FAIL,而保护好得很
// (真机 2026-08-06)。install.ServiceName 是 systemd 的 "bx.service",同样不该
// 印在 macOS 上。
const DarwinGuardianServiceName = "com.getbx.bx.guard"

// DarwinServiceChecks 由 Guardian 的安装/活跃状态产出服务三条。**只有这一份** ——
// 文本路径不再自己算一遍,它渲染的是 Judge 折出来的同一份 Report(见
// renderDoctorReport)。此前那个只服务文本路径的孪生函数(darwinServiceDoctorLines)
// 已经没有调用方,连同只为驱动它而存在的两条测试一起删掉了:一份没人调用而测试
// 盖着的判据,与没有判据在输出上完全一样,却会让下一个人以为文本路径还有第二份判定。
//
// launchd 没有 systemd 那种 enabled 与 active 的分离:Guardian 的 plist 带
// RunAtLoad+KeepAlive,装上即开机自启,故 enabled 直接由 installed 决定。
// 检查**名字**与 linux 那三条保持一致(service_installed/active/enabled),
// 消费方按名字取值,不该因为平台不同而找不到。
func DarwinServiceChecks(installed, active bool) []Check {
	installHint := ""
	if !installed {
		installHint = "sudo bx setup <client-link>"
	}
	activeState := "inactive"
	if active {
		activeState = "active"
	}
	enabledState := "disabled"
	if installed {
		enabledState = "enabled"
	}
	return []Check{
		{Name: "service_installed", Status: BoolStatus(installed), Detail: DarwinGuardianServiceName, Hint: installHint},
		{
			Name:   "service_active",
			Status: ServiceStatusFromState("is-active", activeState),
			Detail: activeState,
			Hint:   HintForState(activeState, "sudo bx up", "bx logs"),
		},
		{Name: "service_enabled", Status: ServiceStatusFromState("is-enabled", enabledState), Detail: enabledState, Hint: "sudo bx up"},
	}
}

func BoolStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

func ServiceStatusFromState(action, state string) string {
	switch action {
	case "is-active":
		if state == "active" {
			return "ok"
		}
	case "is-enabled":
		if state == "enabled" {
			return "ok"
		}
	}
	if state == "unknown" {
		return "warn"
	}
	return "fail"
}

func HintForState(state, primary, logs string) string {
	if state == "active" {
		return primary
	}
	if primary == "" {
		return logs
	}
	return primary + "; " + logs
}
