package cli

import (
	"context"

	"github.com/getbx/bx/internal/platformcheck"
)

// 平台检查下沉到 internal/platformcheck(2026-09-09):它要被 Guardian 的
// /v1/doctor 采集与这里的 bx doctor 两边引。这两个壳只为既有调用点与测试保名字。
func collectPlatformChecks(ctx context.Context) []checkReport { return platformcheck.Collect(ctx) }

func collectTerminalProxyChecks() []checkReport { return platformcheck.TerminalProxyChecks() }
