package leakcheck

import (
	"errors"
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
		{"claude.ai 首页是CF挑战", 403, bodyCloudflareChallenge, nil, ReachUndetermined},
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
	if api != ReachReachable || challenge != ReachUndetermined {
		t.Fatalf("api=%v challenge=%v, want reachable/undetermined", api, challenge)
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
