//go:build darwin

package install

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// **真机事故(2026-08-13):升级停在「重启保护服务」,机器裸奔,只能手动 `sudo bx up`。**
//
//	升级未完成:launch Guardian with launchctl kickstart -k system/com.getbx.bx.guard:
//	exit status 113: Could not find service "com.getbx.bx.guard" in domain for system
//
// 根因是竞态,不是 launchctl 用错了:`bootout` **返回时服务还没真的从域里消失**。
// 紧接着 EnableGuardian 问 `Loaded()`,看到一个正在拆除中的服务 → 判 active=true
// → 于是计划里**只有 kickstart、跳过了 bootstrap** → 等 kickstart 真跑时服务已经
// 没了 → 113。
//
// 修在源头:bootout 要等到标签真的消失为止。
func TestBootoutWaitsUntilTheLabelIsActuallyGone(t *testing.T) {
	// 第 1、2 次问还在(launchd 正在拆),第 3 次才消失 —— 真机上的形状。
	control := &racyLaunchdControl{loadedAnswers: []bool{true, true, true, false}}
	var slept int
	if err := bootoutGuardianWithControlAndWait(context.Background(), control, func() { slept++ }); err != nil {
		t.Fatalf("bootout 报错:%v", err)
	}
	if control.loadedCalls < 3 {
		t.Fatalf("只问了 %d 次 Loaded —— 没有等服务真的消失,竞态原样还在", control.loadedCalls)
	}
	if slept == 0 {
		t.Error("一次都没等就重复轮询 —— 那是忙等")
	}
}

// **等不到也不许报错。** 「停止」这条路上任何一步都不得因为别的事没做成而失败
// (2026-08-04 那次 71 分钟事故换来的规矩)。等超时就往下走,让 enable 那边的
// 兜底去处理 —— 而不是把机器留在「已停止、没重启」的状态。
func TestBootoutDoesNotFailWhenTheLabelNeverDisappears(t *testing.T) {
	control := &racyLaunchdControl{alwaysLoaded: true}
	if err := bootoutGuardianWithControlAndWait(context.Background(), control, func() {}); err != nil {
		t.Fatalf("等不到就报错了:%v —— 停止路径不许依赖别的先成功", err)
	}
}

// **纵深防御:即便竞态仍然发生,也必须自己走出来。**
//
// kickstart 报「找不到服务」时,正确的处置是补一次 bootstrap 再试,而不是中止
// 整个升级 —— 中止的代价是机器停在「保护已关、文件已换、服务没起」这个状态。
// launchdLabelAbsent 这个判据仓库里早就有,只是这条路上没用上。
func TestEnableRecoversWhenKickstartFindsNoService(t *testing.T) {
	control := &fakeGuardianLaunchdControl{
		loaded: map[string]bool{guardianLaunchdLabel: true}, // 拆除中,看起来还在
		runErr: map[string]error{
			"kickstart -k system/" + guardianLaunchdLabel: errors.New(
				`exit status 113: Could not find service "com.getbx.bx.guard" in domain for system`),
		},
	}
	// 第一次 kickstart 失败之后,服务确实不在了 —— 兜底必须重新 bootstrap。
	control.loadedAfterFailure = map[string]bool{guardianLaunchdLabel: false}

	if err := enableGuardianWithControl(context.Background(), control, func() bool { return false }); err != nil {
		t.Fatalf("没能自己走出竞态:%v", err)
	}
	joined := strings.Join(control.calls, " | ")
	if !strings.Contains(joined, "bootstrap system") {
		t.Fatalf("kickstart 说找不到服务,却没有补一次 bootstrap:%s", joined)
	}
}

// 反面:**不是**「找不到服务」的失败仍要如实上报,别把真故障吞成静默重试。
func TestEnableStillReportsUnrelatedFailures(t *testing.T) {
	control := &fakeGuardianLaunchdControl{
		runErr: map[string]error{
			"bootstrap system " + guardianLaunchdPlistPath: errors.New("exit status 5: Input/output error"),
		},
	}
	err := enableGuardianWithControl(context.Background(), control, func() bool { return false })
	if err == nil {
		t.Fatal("无关的 bootstrap 失败被吞掉了")
	}
	if !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("错误里没有原始原因:%v", err)
	}
}

type racyLaunchdControl struct {
	loadedAnswers []bool
	alwaysLoaded  bool
	loadedCalls   int
	calls         []string
}

func (r *racyLaunchdControl) Loaded(context.Context, string) (bool, error) {
	r.loadedCalls++
	if r.alwaysLoaded {
		return true, nil
	}
	if r.loadedCalls <= len(r.loadedAnswers) {
		return r.loadedAnswers[r.loadedCalls-1], nil
	}
	return false, nil
}

func (r *racyLaunchdControl) Run(_ context.Context, args ...string) error {
	r.calls = append(r.calls, strings.Join(args, " "))
	return nil
}
