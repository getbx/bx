package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bx_protection:把 Guardian 那一侧的**意图 / 事实 / 差异**发给 agent。
//
// **它为什么必须存在**(2026-08-31 在项目所有者机器上实测出来的缺口):agent 经
// bx_inspect 拿到的 status 是 Core 的 stats.Report,而 `bx status --json` 比它多
// 16 个键 —— desired(用户意图)、observed(观测到的事实)、reconcile(调谐环
// 报告)、divergence、protection_state、recovery、dns_state…… **全部是 Guardian
// 那半**。也就是说 bx 整个控制面架构最核心的那个洞见「意图 / 事实 / 差异」,
// agent 一个字都看不到:它答得出「隧道健康、延迟 293ms」,答不出「bx 以为自己
// 开着,而系统说劫持没生效」——而后者恰恰是这套架构存在的理由。
//
// **根因是 bx_status 手挑了 6 个字段**(TunnelHealthy/LatencyMS/Restarts/Mode/
// UDPMode/MutationState),世界往前走了、投影没跟上。所以这个工具刻意**不再手挑**:
// 与 bx_inspect 同一个模式,原样转发 CLI 的 JSON 信封。

// 判据是**返回类型**:JSONCommandOut 原样带走 CLI 的 JSON(map[string]any),
// 而一个手写的结构体意味着有人又挑了一遍字段 —— 那正是这次要修的病,
// 而且它是**安静**的:漏掉的字段不会有任何东西报错。
func TestProtectionToolForwardsWithoutANarrowingProjection(t *testing.T) {
	method, ok := reflect.TypeOf((*Ops)(nil)).Elem().MethodByName("Protection")
	if !ok {
		t.Fatal("Ops 接口里没有 Protection")
	}
	if got := method.Type.Out(0).Name(); got != "JSONCommandOut" {
		t.Fatalf("Protection 的返回类型是 %s —— 手挑字段的投影会随 CLI 演进悄悄漏掉新字段,"+
			"而那正是 bx_status 今天缺 16 个键的原因", got)
	}
}

// 转发的必须是 `bx status --json`:Guardian 那半只有它带,inspect 里的
// status 是 Core 的 stats.Report,没有 desired/observed/reconcile。
func TestProtectionArgsAskForTheJSONStatus(t *testing.T) {
	args := protectionArgs()
	if len(args) == 0 || args[0] != "status" {
		t.Fatalf("protectionArgs = %v, want 以 status 开头", args)
	}
	if !strings.Contains(strings.Join(args, " "), "--json") {
		t.Fatalf("protectionArgs = %v, 必须带 --json", args)
	}
}

func TestProtectionToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{protection: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "status", "--json"},
		JSON: map[string]any{
			"desired":          "on",
			"protection_state": "protected",
			"reconcile":        map[string]any{"unchanged_rounds": float64(3)},
		},
	}}
	res := callTool(t, ops, "bx_protection", map[string]any{})
	if res.IsError {
		t.Fatalf("bx_protection 该是只读且成功的: %+v", res)
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	// 三样都要原样到达 —— 少任何一样,agent 就又答不出「意图 vs 事实」。
	for _, key := range []string{"desired", "protection_state", "reconcile"} {
		if _, ok := out.JSON[key]; !ok {
			t.Fatalf("Guardian 那半的 %q 没到达 agent: %+v", key, out.JSON)
		}
	}
}

// 这个工具**不该需要 root**:guardian.sock 的读取面对业主开放,而 MCP 的整个
// 前提是 agent 以业主身份免 sudo 操作 bx(记忆:AI-native = agent 可安全操作的
// 底座)。一个要 root 的诊断工具在这条设计下等于不存在。
//
// 判据只能是「它不在需要 root 的那张清单里」—— 真去跑一次 bx status 是集成
// 测试的事,这里钉的是分类。
func TestProtectionToolIsNotMarkedAsRequiringRoot(t *testing.T) {
	ops := &fakeOps{}
	res := callTool(t, ops, "bx_protection", map[string]any{})
	if res.IsError {
		t.Fatalf("零值替身下也该成功(读不到就由 CLI 那层如实报): %+v", res)
	}
}
