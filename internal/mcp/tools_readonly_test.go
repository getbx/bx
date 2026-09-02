package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callToolOn(t *testing.T, srv *mcpsdk.Server, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx := context.Background()
	st, ct := mcpsdk.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "t", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func callTool(t *testing.T, ops Ops, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	return callToolOn(t, newServer(ops), name, args)
}

func TestCapabilitiesTool(t *testing.T) {
	ops := &fakeOps{caps: CapabilitiesOut{Platform: "linux", Transports: []string{"brook", "reality"}, Installed: true}}
	res := callTool(t, ops, "bx_capabilities", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out CapabilitiesOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Platform != "linux" || !out.Installed {
		t.Fatalf("got %+v", out)
	}
}

func TestDiagnoseTool(t *testing.T) {
	ops := &fakeOps{diagnose: DiagnoseOut{Findings: []Finding{{Severity: "warn", Title: "v6 enabled"}}}}
	res := callTool(t, ops, "bx_diagnose", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
}

func TestStatusToolIncludesMutationState(t *testing.T) {
	ops := &fakeOps{status: StatusOut{TunnelHealthy: true, MutationState: "armed"}}
	res := callTool(t, ops, "bx_status", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out StatusOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.MutationState != "armed" {
		t.Fatalf("mutation_state=%q want armed", out.MutationState)
	}
}

func TestLogsToolReturnsStructuredReport(t *testing.T) {
	ops := &fakeOps{logs: LogsOut{OK: false, Text: "partial\n", Error: "denied", Hint: "sudo bx logs"}}
	res := callTool(t, ops, "bx_logs", map[string]any{"lines": 5})
	if res.IsError {
		t.Fatal("logs tool should return structured log report, not tool error")
	}
	var out LogsOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK || out.Text != "partial\n" || out.Error != "denied" || out.Hint == "" {
		t.Fatalf("logs out = %+v, want structured error report", out)
	}
}

func TestInspectToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{inspect: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "inspect", "--json", "--skip-probe"},
		JSON:    map[string]any{"ok": true, "kind": "inspect"},
	}}
	res := callTool(t, ops, "bx_inspect", map[string]any{"skip_probe": true})
	if res.IsError {
		t.Fatal("inspect tool should be read-only and successful")
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.JSON["kind"] != "inspect" || out.Command[1] != "inspect" {
		t.Fatalf("inspect out = %+v, want CLI JSON envelope", out)
	}
}

func TestLeakCheckToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{leakCheck: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "leak-check", "--json", "--network"},
		JSON:    map[string]any{"ok": true, "kind": "leak"},
	}}
	res := callTool(t, ops, "bx_leak_check", map[string]any{"network": true, "expected_ips": []string{"203.0.113.10"}})
	if res.IsError {
		t.Fatal("leak_check tool should be read-only and successful")
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.JSON["kind"] != "leak" || out.Command[1] != "leak-check" {
		t.Fatalf("leak_check out = %+v, want CLI JSON envelope", out)
	}
}

func TestObserveToolReturnsCLIJSONEnvelope(t *testing.T) {
	ops := &fakeOps{observe: JSONCommandOut{
		OK:      true,
		Command: []string{"bx", "observe", "--json", "--duration", "30s"},
		JSON:    map[string]any{"ok": true, "kind": "observe"},
	}}
	res := callTool(t, ops, "bx_observe", map[string]any{"duration": "30s"})
	if res.IsError {
		t.Fatal("observe tool should be read-only and successful")
	}
	var out JSONCommandOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.JSON["kind"] != "observe" || out.Command[1] != "observe" {
		t.Fatalf("observe out = %+v, want CLI JSON envelope", out)
	}
}

func TestCheckToolRunsTheSafeDefaultBundle(t *testing.T) {
	ops := &fakeOps{check: CheckOut{OK: true, Risk: "low"}}
	res := callTool(t, ops, "bx_check", map[string]any{})
	if res.IsError {
		t.Fatal("safe check should be available without a mutation approval")
	}
	if len(ops.calls) != 1 || ops.calls[0] != "check" {
		t.Fatalf("calls=%v want [check]", ops.calls)
	}
	if ops.checkIn.Network || ops.checkIn.Duration != "" {
		t.Fatalf("zero-value check must not opt in to external probes: %+v", ops.checkIn)
	}
}

func TestObserveArgs(t *testing.T) {
	got := observeArgs(ObserveIn{Duration: "30s", Interval: "1s", Scenario: "video"})
	for _, want := range []string{"observe", "--json", "--duration", "30s", "--interval", "1s", "--scenario", "video"} {
		if !stringSliceContains(got, want) {
			t.Fatalf("observe args = %v, missing %s", got, want)
		}
	}
}

func TestInspectArgsDefaultToNoOutboundProbe(t *testing.T) {
	got := inspectArgs("/etc/bx/config.yaml", InspectIn{})
	if !stringSliceContains(got, "--skip-probe") {
		t.Fatalf("inspect args = %v, want --skip-probe by default", got)
	}
	got = inspectArgs("/etc/bx/config.yaml", InspectIn{Probe: true})
	if stringSliceContains(got, "--skip-probe") {
		t.Fatalf("inspect args = %v, did not expect --skip-probe when probe=true", got)
	}
}

// **MCP 这一侧不许有「打开浏览器」这个选项。**
//
// 它此前有 browser / browser_confirmed 两个字段,外加一道运行期的确认门。那道门
// 现在不需要了 —— 浏览器那半整个搬去了 `bx leakcheck`,而它要人在屏幕前点一下才
// 产生数据,那从来就不适合由 agent 代劳。**按构造做不到,比运行期拦一下更强。**
//
// 这条守卫留下来的理由是:上一版那道门是有人专门写的,而删掉它之后,谁把字段加
// 回来就不会再有任何东西拦着。它钉的是**能力的缺席**,不是某段代码的存在。
// 上一条守卫盯的是**字段**,而 agent 唯一读得到的是**工具描述那句话** ——
// 两者是两回事,2026-08-31 实测:字段早就删干净了,描述里却还写着
// 「browser=true requires browser_confirmed=true after user confirmation」,
// 教 agent 去传两个根本不存在的参数。
//
// **对 agent 撒谎比对人撒谎更糟**:人读到不对会去看代码,agent 会照着描述
// 构造调用,然后拿到一个它无从理解的 schema 错误。这与本仓库反复记的
// 「关于代码的陈述」同类,只是读者换成了机器。
//
// 判据是**描述与 schema 必须一致**:凡描述里提到的参数名,结构体里必须真有
// 那个字段 —— 反过来不要求(不是每个字段都值得在一句话里点名)。
func TestMCPToolDescriptionsDoNotNameParametersThatDoNotExist(t *testing.T) {
	fields := map[string]bool{}
	for _, typ := range []any{LeakCheckIn{}, CheckIn{}, InspectIn{}, ObserveIn{}, LogsIn{}} {
		v := reflect.TypeOf(typ)
		for i := 0; i < v.NumField(); i++ {
			tag := v.Field(i).Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name != "" && name != "-" {
				fields[name] = true
			}
		}
	}
	source, err := os.ReadFile("tools_readonly.go")
	if err != nil {
		// 读不懂现在的代码时响亮失败 —— 一条认不出目标的守卫与没有守卫一样,
		// 但它看起来更让人放心。
		t.Fatalf("读 tools_readonly.go: %v", err)
	}
	// 只查那几个**曾经存在过、现已删除**的参数名:它们是最容易在描述里留下
	// 残影的一类,而通用的「任何 snake_case 词都必须是字段」会把 outbound、
	// network-path 这类普通英文词也当成参数名,制造假红。
	for _, retired := range []string{"browser_confirmed", "browser_timeout"} {
		if bytes.Contains(source, []byte(retired)) && !fields[retired] {
			t.Errorf("工具描述里还写着已删除的参数 %q —— agent 会照着它构造调用", retired)
		}
	}
	if bytes.Contains(source, []byte("browser=true")) && !fields["browser"] {
		t.Error("工具描述里还写着 browser=true,而 schema 里没有 browser 字段")
	}
}

func TestMCPCannotOpenABrowser(t *testing.T) {
	for _, typ := range []any{LeakCheckIn{}, CheckIn{}} {
		v := reflect.TypeOf(typ)
		for i := 0; i < v.NumField(); i++ {
			name := strings.ToLower(v.Field(i).Name)
			if strings.Contains(name, "browser") {
				t.Errorf("%s 又有了 %s 字段 —— agent 不该能让 bx 打开界面;"+
					"浏览器那半住在 `bx leakcheck`,它需要人点一下",
					v.Name(), v.Field(i).Name)
			}
		}
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// bx_explain 是 agent 那一侧「请求级的为什么」唯一的入口。三条性质各自承重。

// ① 它必须真的把 target 传下去。
// 一个丢掉 target 的工具会对每个问题返回同一个答案,而 agent 无从察觉。
func TestExplainToolForwardsTheTarget(t *testing.T) {
	ops := &fakeOps{explain: JSONCommandOut{OK: true, JSON: map[string]any{"target": "x"}}}
	_ = callTool(t, ops, "bx_explain", map[string]any{"target": "steamstatic.com"})
	if ops.explainIn.Target != "steamstatic.com" {
		t.Errorf("target 没传下去:%q", ops.explainIn.Target)
	}
}

// ② 空 target 必须**在拨号之前**被挡下,而且要给可行动的指引。
// 让它一路走到 Core 再报「target is required」,agent 拿到的是一个通用失败。
func TestExplainToolRejectsAnEmptyTargetWithGuidance(t *testing.T) {
	live := &liveOps{}
	_, err := live.Explain(ExplainIn{Target: "   "})
	var te ToolError
	if !errors.As(err, &te) {
		t.Fatalf("空 target 没有产出结构化错误:%v", err)
	}
	if te.Remediation == "" {
		t.Error("挡下了却没说该怎么办")
	}
}

// ③ **绝不带可执行路径。** bx_apps 那次已经为此立过规矩:agent 要回答的是
// 「哪条路、为什么」,不是「那个程序装在哪儿」。explain 连应用维度都没有,
// 这条守卫钉的是「将来别顺手加进来」。
func TestExplainToolCarriesNoExecutablePaths(t *testing.T) {
	res := callTool(t, &fakeOps{explain: JSONCommandOut{OK: true, JSON: map[string]any{"target": "x", "tcp": map[string]any{"effective": "direct"}}}},
		"bx_explain", map[string]any{"target": "x"})
	body := res.Content[0].(*mcpsdk.TextContent).Text
	for _, forbidden := range []string{"exec_path", "execPath", "ExecPath"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("输出里出现了 %s:%s", forbidden, body)
		}
	}
}

// ①b **参数构造那一跳单独守。** 上面那条断言的是工具层传给 Ops 的结构体,
// 而 fakeOps 不经过 liveOps —— 变异实测(在 liveOps.Explain 里丢掉 target)
// 它照样全绿。这是本仓库反复出现的「守卫钉住缺陷旁边的东西」。
func TestExplainArgsCarryTheTarget(t *testing.T) {
	args := explainArgs("steamstatic.com")
	if len(args) == 0 || args[0] != "explain" {
		t.Fatalf("不是 explain 命令:%v", args)
	}
	found := false
	for _, a := range args {
		if a == "steamstatic.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("target 没进命令行:%v —— 每个问题会得到同一个答案", args)
	}
	if !strings.Contains(strings.Join(args, " "), "--json") {
		t.Errorf("没要 JSON 输出:%v", args)
	}
}
