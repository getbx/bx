package cli

import (
	"context"

	"github.com/getbx/bx/internal/platformcheck"
)

// 平台检查下沉到 internal/platformcheck(2026-09-09):它要被 Guardian 的
// /v1/doctor 采集与这里的 bx doctor 两边引。这个壳只为既有调用点保名字。
//
// 曾经并排还有一个 collectTerminalProxyChecks —— **没有任何调用方**,而
// `platformcheck.Collect` 自己第一句就在调 `TerminalProxyChecks`。一个只被
// 注释提到的转发壳,与不存在它在行为上完全一样,却会让下一个人以为 cli 这边
// 还留着一条单独取终端代理的路。
func collectPlatformChecks(ctx context.Context) []checkReport { return platformcheck.Collect(ctx) }
