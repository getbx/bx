package leakcheck

import (
	"errors"
	"strings"
	"testing"
)

// fixture 全部来自 2026-09-13 那轮真机实测(spec §2)。
// **合成数据造不出这里最要紧的那个形状**:同一个 403 之下,
// 「服务 JSON」与「CF 挑战页」是相反的两件事。
const (
	bodyAnthropic405 = `{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "Method Not Allowed"
  }
}`
	bodyOpenAI401 = `{
  "error": {
    "message": "Missing bearer authentication in header",
    "type": "invalid_request_error"
  }
}`
	bodyGoogle403 = `{
  "error": {
    "code": 403,
    "message": "Method doesn't allow unregistered callers"
  }
}`
	bodyCloudflareChallenge = `<!DOCTYPE html><html><head><title>Just a moment...</title>` +
		`<meta http-equiv="content-security-policy" content="default-src 'none'; ` +
		`script-src 'nonce-x' 'unsafe-eval' https://challenges.cloudflare.com">`
	// 构造的,不是实测 —— 这台机器的出口在支持区,没见过真的地区拒绝(spec §3.3)。
	// **真机见到之后回来把真实 body 换进这里。**
	bodyRegionRefused = `{"error":{"type":"permission_error","message":"Service not available in your region"}}`
)

func TestJudgeReachOnRealMachineFixtures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
		want   ReachState
	}{
		{"anthropic 405 服务JSON", 405, bodyAnthropic405, nil, ReachReachable},
		{"openai 401 服务JSON", 401, bodyOpenAI401, nil, ReachReachable},
		{"google 403 也是服务JSON", 403, bodyGoogle403, nil, ReachReachable},
		{"favicon 404 空body", 404, "", nil, ReachReachable},
		{"favicon 200 二进制", 200, "\x00\x00\x01\x00", nil, ReachReachable},
		{"claude.ai 首页是CF挑战", 403, bodyCloudflareChallenge, nil, ReachChallenged},
		{"地区拒绝", 403, bodyRegionRefused, nil, ReachRefused},
		{"拨不通", 0, "", errors.New("dial tcp: i/o timeout"), ReachUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := JudgeReach(tc.status, []byte(tc.body), tc.err); got != tc.want {
				t.Fatalf("JudgeReach = %v, want %v", got, tc.want)
			}
		})
	}
}

// **同一个 403,相反的两件事** —— 这是整个判据的形状,单独钉一条。
// 只看状态码的实现会让这两个必然相等。
func TestSame403MeansOppositeThings(t *testing.T) {
	api := JudgeReach(403, []byte(bodyGoogle403), nil)
	challenge := JudgeReach(403, []byte(bodyCloudflareChallenge), nil)
	if api == challenge {
		t.Fatalf("两个 403 判成了同一个 %v —— 判据一定只看了状态码", api)
	}
	if api != ReachReachable || challenge != ReachChallenged {
		t.Fatalf("api=%v challenge=%v, want reachable/challenged", api, challenge)
	}
}

// 认不出的东西一律「没问出来」,绝不升格成可达(spec §3.1 零值纪律)。
func TestUnrecognisedResponsesAreUndeterminedNotReachable(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{418, "teapot"},
		{502, "<html>bad gateway</html>"},
		{503, ""},
	} {
		if got := JudgeReach(tc.status, []byte(tc.body), nil); got == ReachReachable {
			t.Fatalf("status=%d body=%q 判成了 reachable —— 认不出就该是 undetermined", tc.status, tc.body)
		}
	}
}

// 拨号错误压过一切:拿到 dialErr 就不许再去看 status/body。
func TestDialErrorWinsOverEverything(t *testing.T) {
	if got := JudgeReach(200, []byte(bodyAnthropic405), errors.New("no route to host")); got != ReachUnreachable {
		t.Fatalf("JudgeReach = %v, want unreachable —— 有拨号错误时不该再看响应", got)
	}
}

// **C1(2026-09-14 review):judgeReachTarget 此前一条测试都没有覆盖到。**
// 所有喂 Judge 的既有测试用的都是空 ReachProbes,只走得到「没有探测记录」那条
// 早退分支 —— 删掉 `f.Reach = current.State`、或把整个 switch 删光,既有的八条
// 守卫全部照样绿。这正是本文件头上「测试输入让待守属性不可见」那个坑,Task 5
// 把它原样复刻了一遍。
//
// 这条钉的是判定本身:给定每一态的 ReachProbe,断言 (Verdict, Reach, Summary
// 关键词),覆盖 judgeReachTarget 的全部分支,包括新加的 ReachChallenged 与
// 「探过了但认不出」的 ReachUndetermined 两种不同措辞。
func TestJudgeReachTargetCoversEveryState(t *testing.T) {
	tgt := ReachTargets()[0] // anthropic_api

	for _, tc := range []struct {
		name          string
		state         ReachState
		wantVerdict   Verdict
		wantSummary   string // Summary 必须包含的关键词
		forbidSummary string // Summary 绝不许出现的词(空串表示不检查)
	}{
		{
			name: "可达", state: ReachReachable,
			wantVerdict: OK, wantSummary: "bx can reach",
			forbidSummary: "you can use", // spec §3.4:绝不说「你可以用 X」
		},
		{
			name: "被拒", state: ReachRefused,
			wantVerdict: Bad, wantSummary: "refused",
		},
		{
			name: "不可达", state: ReachUnreachable,
			wantVerdict: Bad, wantSummary: "could not reach",
		},
		{
			// 有 CF 特征串这份真凭据,才敢替用户否掉「不是你的出口有问题」。
			name: "人机挑战", state: ReachChallenged,
			wantVerdict: NotChecked, wantSummary: "Cloudflare",
		},
		{
			// 探过了、但认不出是哪一种(未知状态码/重定向)——**不许借用
			// Challenged 那句 CF 措辞**,判据没有这份凭据(I1)。
			name: "认不出", state: ReachUndetermined,
			wantVerdict: NotChecked, wantSummary: "could not determine",
			forbidSummary: "Cloudflare",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := ReachProbe{TargetID: tgt.ID, Path: ReachPathCurrent, State: tc.state}
			f := judgeReachTarget(tgt, []ReachProbe{probe})

			if f.Verdict != tc.wantVerdict {
				t.Errorf("Verdict = %v, want %v", f.Verdict, tc.wantVerdict)
			}
			// **C1 的核心断言**:Reach 字段必须原样带出探测到的状态。
			// 删掉 `f.Reach = current.State` 就是这一行转红。
			if f.Reach != tc.state {
				t.Errorf("Reach = %v, want %v —— Judge 的 Reach 字段没有原样带出探测状态",
					f.Reach, tc.state)
			}
			if !strings.Contains(f.Summary, tc.wantSummary) {
				t.Errorf("Summary = %q,不含 %q", f.Summary, tc.wantSummary)
			}
			if tc.forbidSummary != "" && strings.Contains(f.Summary, tc.forbidSummary) {
				t.Errorf("Summary = %q,不该出现 %q", f.Summary, tc.forbidSummary)
			}
		})
	}
}

// 没有探测记录时(今天的常态,接线归后续任务)必须诚实地说「没检查」,
// **不许借用人机挑战那句话** —— 这与「探过了、认不出」是两件不同的事,零值
// ReachUndetermined 之下两条路径的 Summary 必须不同(早退分支 vs switch 的
// default 分支)。
func TestJudgeReachTargetWithNoProbeSaysNotCheckedNotChallenged(t *testing.T) {
	tgt := ReachTargets()[0]
	f := judgeReachTarget(tgt, nil)

	if f.Verdict != NotChecked {
		t.Fatalf("Verdict = %v, want NotChecked", f.Verdict)
	}
	if f.Reach != ReachUndetermined {
		t.Fatalf("Reach = %v, want ReachUndetermined(零值)", f.Reach)
	}
	if strings.Contains(f.Summary, "Cloudflare") {
		t.Fatalf("没有探测记录时不许提 Cloudflare —— 那是在断言一次从没发生过的观测:%q",
			f.Summary)
	}
	if !strings.Contains(f.Summary, "was not checked") {
		t.Fatalf("Summary = %q,应如实说明这一轮没有检查", f.Summary)
	}
}
