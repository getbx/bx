package cli

import "testing"

// `bx update --json` 必须真的安装,不能只打一份检查报告。
//
// 真机 2026-08-06:菜单栏点 "Update bx" 恒显示 "Update Failed"。菜单栏跑的是
// `bx update --json`(main.swift:750),而 updateAction 里 `if c.Bool("json")`
// 在安装分支之前就 return 了一份 updateCheckReport —— 于是安装从未发生,
// 菜单栏拿这份 {current,latest,available,verified} 去解 UpdateResultJSON,
// 既没有 rolled_back 也没有 phase=="committed",判定失败。
//
// 根因是 --json 被当成模式开关而不是输出格式。本函数刻意**不接收 json 参数**:
// 输出格式在类型层面就无法影响"装不装"这个决定。
func TestUpdateDispositionInstallsWhenNotChecking(t *testing.T) {
	for _, tt := range []struct {
		name      string
		check     bool
		force     bool
		available bool
		want      updateDisposition
	}{
		{"有新版且未指定 --check:必须安装", false, false, true, updateDispositionInstall},
		{"--check:只报告,绝不安装", true, false, true, updateDispositionReport},
		{"--check 且已是最新:仍只报告", true, false, false, updateDispositionReport},
		{"已是最新:无需安装", false, false, false, updateDispositionUpToDate},
		{"--force 即便已是最新也安装", false, true, false, updateDispositionInstall},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideUpdateDisposition(tt.check, tt.force, tt.available); got != tt.want {
				t.Errorf("decideUpdateDisposition(check=%v, force=%v, available=%v) = %v, want %v",
					tt.check, tt.force, tt.available, got, tt.want)
			}
		})
	}
}
