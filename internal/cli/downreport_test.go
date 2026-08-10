package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/guardian"
)

// 强制路径打印的这几行是用户在最坏时刻(保护关不掉、网络可能不通)看到的唯一指引。
// 它今天零覆盖,而阶段② 要重排这段代码 —— 先钉住,重构才可能是安全的。
func TestDownReportForcedPathTellsTheTruthAndGivesTheNextStep(t *testing.T) {
	out, errLines := downReportLines(macOSDownResult{Forced: true})
	joined := strings.Join(append(append([]string{}, out...), errLines...), "\n")

	// ① 必须说清「走的是强制」,否则用户以为一切正常。
	if !strings.Contains(joined, "强制") {
		t.Errorf("强制路径必须让用户知道走的是强制:\n%s", joined)
	}
	// ② 必须逐条列出做过的动作。用户要凭它判断还差什么。
	for _, action := range []string{"关闭意图", "Core", "Guardian", "屏障", "DNS"} {
		if !strings.Contains(joined, action) {
			t.Errorf("强制路径必须列出做过的动作,缺 %q:\n%s", action, joined)
		}
	}
	// ③ **绝不能断言「网络已还原」。** 强制拆除是尽力而为,六步里任何一步都可能失败
	//    而流程仍然继续 —— 断言已还原就是骗人,而被骗的人此刻可能正断着网。
	if strings.Contains(joined, "网络已恢复") {
		t.Errorf("强制路径不得断言网络已恢复(它做不到这个保证):\n%s", joined)
	}
	// ④ 必须给出下一步。
	if !strings.Contains(joined, "bx uninstall") {
		t.Errorf("强制路径必须给出仍不通时的下一步:\n%s", joined)
	}
}

// 干净路径可以断言已恢复 —— Guardian 的事务成功返回才走到这里。
func TestDownReportCleanPathMayAssertRecovery(t *testing.T) {
	out, _ := downReportLines(macOSDownResult{})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "已停止") {
		t.Errorf("干净路径要告诉用户已停止:\n%s", joined)
	}
	if strings.Contains(joined, "强制") {
		t.Errorf("干净路径不该出现「强制」字样:\n%s", joined)
	}
}

// 回落原因必须原样带给用户:它是唯一能说明「为什么没能干净停下」的东西。
func TestDownReportCarriesTheCleanPathFailureCause(t *testing.T) {
	_, errLines := downReportLines(macOSDownResult{Forced: true, Cause: errSentinelForReport})
	joined := strings.Join(errLines, "\n")
	if !strings.Contains(joined, errSentinelForReport.Error()) {
		t.Errorf("回落原因必须出现在给用户的输出里:\n%s", joined)
	}
}

// Cause 为 nil 表示 Guardian 压根没应答 —— 那也要说清楚,不能只说「失败了」。
func TestDownReportSaysGuardianWasUnreachableWhenThereIsNoCause(t *testing.T) {
	_, errLines := downReportLines(macOSDownResult{Forced: true})
	joined := strings.Join(errLines, "\n")
	if !strings.Contains(joined, "未响应") {
		t.Errorf("没有 Cause 时要说明 Guardian 未响应:\n%s", joined)
	}
}

// legacy Core 那条强制路径**不是**「Guardian 未响应」。
//
// 它是三条强制路径里唯一一条 Guardian 好端端应答、而我们**主动**选了重手术的:
// 干净事务停不下一个 Guardian 不掌管的 Core,只会报成功。沿用 Cause==nil 那句
// 文案会让用户去排查一个根本不存在的「Guardian 没应答」故障 —— 在这一期专门
// 消灭假话的语境里,那本身就是一句新的假话。
func TestDownReportExplainsLegacyCoreForcedPathWithoutBlamingGuardian(t *testing.T) {
	_, errLines := downReportLines(macOSDownResult{Forced: true, LegacyCore: true})
	joined := strings.Join(errLines, "\n")
	if strings.Contains(joined, "未响应") {
		t.Errorf("Guardian 应答了,不得说它未响应:\n%s", joined)
	}
	if !strings.Contains(joined, "旧版 Core") {
		t.Errorf("必须说明真实原因(旧版 Core):\n%s", joined)
	}
	if !strings.Contains(joined, "强制") {
		t.Errorf("仍要说清走的是强制路径:\n%s", joined)
	}
}

// Task 1 钉住的那几条性质,必须对**每一种**强制子情况都成立。
//
// 它们此前只对 {Forced:true} 断言过,而强制路径现在有三种进法(Guardian 未响应 /
// 干净事务失败 / 可能有旧版 Core)。一个新分支只要早返回一步,就能悄悄绕过
// 「列出做过的动作」「不断言网络已恢复」「给出 bx uninstall 这条下一步」——
// 而这三条恰恰是用户在最坏时刻唯一的指引。
func TestDownReportForcedPropertiesHoldForEveryForcedSubCase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result macOSDownResult
	}{
		{"guardian 未响应", macOSDownResult{Forced: true}},
		{"干净事务失败", macOSDownResult{Forced: true, Cause: errSentinelForReport}},
		{"可能有旧版 Core", macOSDownResult{Forced: true, LegacyCore: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errLines := downReportLines(tc.result)
			joined := strings.Join(append(append([]string{}, out...), errLines...), "\n")

			if !strings.Contains(joined, "强制") {
				t.Errorf("必须让用户知道走的是强制:\n%s", joined)
			}
			for _, action := range []string{"关闭意图", "Core", "Guardian", "屏障", "DNS"} {
				if !strings.Contains(joined, action) {
					t.Errorf("必须列出做过的动作,缺 %q:\n%s", action, joined)
				}
			}
			if strings.Contains(joined, "网络已恢复") {
				t.Errorf("不得断言网络已恢复(强制拆除做不到这个保证):\n%s", joined)
			}
			if !strings.Contains(joined, "bx uninstall") {
				t.Errorf("必须给出仍不通时的下一步:\n%s", joined)
			}
		})
	}
}

// 升级路径**不得**自己抄一份措辞。
//
// appinstall_darwin.go 一直内联着自己那份「⚠️ Guardian 未响应,已改走强制停止」,
// 而 macOSDownLifecycleFor 选择 legacy 分支时根本不看 purpose —— 于是升级路径会
// 打印出与 bx down 相同的那句假话(Guardian 明明应答了)。两处必须由同一个纯函数
// 导出原因。
func TestForcedTeardownReasonDistinguishesEveryForcedSubCase(t *testing.T) {
	unreachable := forcedTeardownReason(macOSDownResult{Forced: true})
	failed := forcedTeardownReason(macOSDownResult{Forced: true, Cause: errSentinelForReport})
	legacy := forcedTeardownReason(macOSDownResult{Forced: true, LegacyCore: true})

	if !strings.Contains(unreachable, "未响应") {
		t.Errorf("Guardian 没应答时要说清:%q", unreachable)
	}
	if !strings.Contains(failed, errSentinelForReport.Error()) {
		t.Errorf("干净事务失败必须带上原因:%q", failed)
	}
	if strings.Contains(legacy, "未响应") {
		t.Errorf("Guardian 应答了,不得说它未响应:%q", legacy)
	}
	if !strings.Contains(legacy, "旧版 Core") {
		t.Errorf("必须说明真实原因:%q", legacy)
	}
	// 三种原因必须互不相同,否则区分它们的意义就没了。
	if unreachable == failed || failed == legacy || unreachable == legacy {
		t.Errorf("三种强制原因必须可区分:\n%q\n%q\n%q", unreachable, failed, legacy)
	}
}

var errSentinelForReport = errReportSentinel{}

type errReportSentinel struct{}

func (errReportSentinel) Error() string { return "recovery-incomplete-sentinel" }

// 逃生口**自己也部分失败**时返回的那条错误,是整条链上最要紧的一段文字:
// 用户此刻保护关不掉、网络可能不通,而这段话里带着他们必须手敲的
// `route delete` 命令 —— 那是把网络拿回来的唯一办法。
//
// **更正(复审指出):这条此前不是零覆盖,是部分覆盖。**
// macos_lifecycle_test.go:679 的 TestMacOSDownForcedTeardownClearsBarrierEvenWhenEarlierStepsFail
// 已经硬编码了四条清理命令中的一条("route -n delete -net 0.0.0.0/2")。
// 本条仍然更强,而且强在两个具体的地方:它查**全部**四条(少一条即红,而硬编码
// 一条的写法对「少了另外三条」是瞎的),并且在 hints 为空时 t.Fatal 而不是
// 平凡地通过(「每一条都在里面」对空集合恒真 —— 反极性断言的老陷阱)。
func TestForcedTeardownFailureCarriesTheManualRecoveryCommands(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	boom := errors.New("bootout-refused")
	f.macOSLifecycleDeps.forceTeardown = func(context.Context) error { return boom }

	err := forcedMacOSTeardown(context.Background(), stopIntent{purpose: downPurposeUser}, f.macOSLifecycleDeps, nil)
	if err == nil {
		t.Fatal("有步骤失败时必须报错 —— 静默成功会让用户以为网络已经还原")
	}
	msg := err.Error()

	if !strings.Contains(msg, boom.Error()) {
		t.Errorf("失败原因必须带出来:\n%s", msg)
	}
	// 兜底出路。
	if !strings.Contains(msg, "bx uninstall") {
		t.Errorf("必须给出兜底出路:\n%s", msg)
	}
	// **手动删屏障路由的命令**:屏障是 /2 reject,不删掉整机不通,
	// 而此刻自动删除已经失败了,用户只剩手敲这一条路。
	if !strings.Contains(msg, "launchctl bootout") {
		t.Errorf("必须给出手动停 Guardian 的命令:\n%s", msg)
	}
	hints := blockingRouteCleanupHints()
	if len(hints) == 0 {
		t.Fatal("blockingRouteCleanupHints 为空 —— 指引里那句「逐条删除阻断路由」后面就没有东西了")
	}
	for _, hint := range hints {
		if !strings.Contains(msg, hint) {
			t.Errorf("手动删路由的命令 %q 必须出现在指引里:\n%s", hint, msg)
		}
	}
}

// 干净路径失败导致的回落,原因同样要出现在这条错误里 —— 它解释了「为什么没能干净停下」。
func TestForcedTeardownFailureAlsoCarriesTheCleanPathCause(t *testing.T) {
	f := newFakeMacOSLifecycleDeps()
	f.macOSLifecycleDeps.restoreSystemDNS = func(context.Context) error { return errors.New("dns-stuck") }
	cause := errors.New("recovery-incomplete")

	err := forcedMacOSTeardown(context.Background(), stopIntent{purpose: downPurposeUser}, f.macOSLifecycleDeps, cause)
	if err == nil {
		t.Fatal("有步骤失败时必须报错")
	}
	if !strings.Contains(err.Error(), cause.Error()) {
		t.Errorf("干净路径的失败原因必须一并带出:\n%s", err.Error())
	}
}

// 零值 macOSDownResult 渲染出来的是「✅ bx 已停止并取消开机自启。」——
// 这一期要杀的那句原话。任何在**出错**时返回零值的路径,只要有一个调用方忽略 err
// 去渲染,就会把它印给一个保护还开着的用户。所以出错路径也必须带上 Forced。
//
// 这条不是假想:复审在 legacy 分支上实测到了零值返回,而 upgraderun.go 那条路
// 恰恰会拿结果去渲染。
func TestDownResultOnErrorPathsNeverRendersAsCleanSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result macOSDownResult
	}{
		{"legacy 分支出错", macOSDownResult{Forced: true, LegacyCore: true}},
		{"干净路径失败后强制也失败", macOSDownResult{Forced: true, Cause: errSentinelForReport}},
		{"Guardian 不可达且强制失败", macOSDownResult{Forced: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := downReportLines(tc.result)
			joined := strings.Join(stdout, "\n")
			if strings.Contains(joined, "网络已恢复") || strings.Contains(joined, "✅") {
				t.Fatalf("出错路径的结果绝不能渲染成干净成功:\n%s", joined)
			}
		})
	}
}

// Guardian 现在会在报 off 之前向系统求证;求证不过就发 needs_attention。
// 而 downReportLines 此前**从不读 result.Status**,于是那份判断在 bx down 这条路上
// 完全不可见 —— 菜单栏读 protection_state 所以生效了,CLI 没有。
// 机制建好而最后一寸没接,等于没建。
func TestDownReportDoesNotClaimStoppedWhenGuardianCouldNotConfirm(t *testing.T) {
	for _, protection := range []string{
		guardian.ProtectionNeedsAttention,
		guardian.ProtectionBlocked,
	} {
		t.Run(protection, func(t *testing.T) {
			stdout, stderr := downReportLines(macOSDownResult{
				Status: guardian.Status{Protection: protection},
			})
			joined := strings.Join(append(append([]string{}, stdout...), stderr...), "\n")
			if strings.Contains(joined, "✅") {
				t.Fatalf("Guardian 没能确认关闭时不许打勾:\n%s", joined)
			}
			// 下一步必须指向**真的能看到原因**的地方。这条最初写的是 bx status,
			// 而 bx status 根本不显示 last_error —— 那条断言当时钉住的是一个死胡同。
			if !strings.Contains(joined, "bx-guard.err.log") {
				t.Errorf("要给出下一步(而且是真能看到原因的那个):\n%s", joined)
			}
		})
	}
}

// 反向:Guardian 确认关掉了,照旧打勾 —— 否则这条修复自己变成噪声源。
func TestDownReportStillConfirmsStopWhenGuardianSaysOff(t *testing.T) {
	stdout, _ := downReportLines(macOSDownResult{
		Status: guardian.Status{Protection: guardian.ProtectionOff},
	})
	if !strings.Contains(strings.Join(stdout, "\n"), "✅") {
		t.Fatalf("确认关闭时必须照旧给出肯定答复:\n%s", strings.Join(stdout, "\n"))
	}
}

// 进度行与报告不许互相打脸。曾经 macOSDownAction 无条件打
// 「✓ bx 已停止,网络已恢复」,而紧接着的报告说「没能确认关闭」——
// 用户在相邻两行里同时读到断言和对它的否认。
//
// 这条守的是**它们读同一个判据**,而不是各自判各自的。
func TestProgressLineAgreesWithTheReport(t *testing.T) {
	unconfirmed := macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionNeedsAttention}}
	if downConfirmedStopped(unconfirmed) {
		t.Fatal("needs_attention 不该被判成「已确认停止」——进度行会因此打出肯定的勾")
	}
	confirmed := macOSDownResult{Status: guardian.Status{Protection: guardian.ProtectionOff}}
	if !downConfirmedStopped(confirmed) {
		t.Fatal("off 必须判成已确认,否则每次正常停止都会显示「未能确认」")
	}
}

// 给用户的理由要用 Guardian 报的失败码,它比 protection_state 具体得多:
// core_still_running(确知还在)与 core_scan_failed(问不出来)对用户是两件事。
func TestUnconfirmedReasonPrefersTheGuardianFailureCode(t *testing.T) {
	result := macOSDownResult{Status: guardian.Status{
		Protection: guardian.ProtectionNeedsAttention,
		LastError:  "core_still_running",
	}}
	if got := downUnconfirmedReason(result); got != "core_still_running" {
		t.Fatalf("应优先用失败码, got %q", got)
	}
	_, stderr := downReportLines(result)
	if !strings.Contains(strings.Join(stderr, "\n"), "core_still_running") {
		t.Errorf("失败码必须出现在给用户的那句话里:\n%s", strings.Join(stderr, "\n"))
	}
}

// **不许把用户支进死胡同。** 这里曾写「请执行 bx status 查看原因」,而 bx status
// 根本不显示 last_error —— 用户照做,什么也看不到。
func TestUnconfirmedGuidancePointsSomewhereTheAnswerActuallyIs(t *testing.T) {
	stdout, _ := downReportLines(macOSDownResult{
		Status: guardian.Status{Protection: guardian.ProtectionNeedsAttention},
	})
	joined := strings.Join(stdout, "\n")
	if !strings.Contains(joined, "bx-guard.err.log") {
		t.Errorf("要指向真的能看到原因的地方(Guardian 日志):\n%s", joined)
	}
	if strings.Contains(joined, "bx status 查看原因") {
		t.Errorf("bx status 不显示 last_error,别把用户支过去:\n%s", joined)
	}
}
