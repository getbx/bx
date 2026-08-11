package leakserve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/leakcheck"
)

// 页面必须在**联网之前**原样显示要联系谁(设计风险三:第三方暴露是用户明确
// 接受的,但必须可见)。断言打在真实响应体上,不是打在模板文件上。
func TestPageDisclosesEndpointsVerbatim(t *testing.T) {
	srv := newTestServer(t)
	resp := get(t, srv, "/?t="+srv.Token())
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{leakcheck.EchoV4URL, leakcheck.EchoV6URL, leakcheck.STUNURL} {
		if !strings.Contains(html, want) {
			t.Errorf("页面必须原样显示 %q,它是用户可见契约", want)
		}
	}
	// 页面还必须带上 token,否则它自己发不出 POST。
	if !strings.Contains(html, srv.Token()) {
		t.Error("页面里必须带 token,否则它的上报请求会被自己的闸门拒掉")
	}
	// 这一页带着本次运行专属的 token,不该进磁盘缓存。
	//
	// 原表把这条记为「不转红,靠 review」——它其实是可测的,而这个仓库的教训正是
	// 「记在案上靠 review」的东西没人再看第二眼。
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("带 token 的页面必须 no-store,得到 Cache-Control: %q", got)
	}
}

// **JS 愚蠢的结构性保证之一**:页面拿到的响应里,只有已经判完的结论,
// 没有任何可供它自己判断的原料。顶层键集合逐字锁定。
func TestReportResponseCarriesFinishedConclusions(t *testing.T) {
	srv := newTestServer(t)
	req, err := http.NewRequest(http.MethodPost,
		"http://"+srv.Addr().String()+"/report?t="+srv.Token(),
		strings.NewReader(`{"exit_v4":"5.6.7.8","srflx":["1.2.3.4"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.Origin())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var top map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&top); err != nil {
		t.Fatalf("上报响应必须是 JSON: %v", err)
	}
	want := map[string]bool{
		"generated_at": true, "endpoints": true,
		"findings": true, "evidence": true, "anomaly_count": true,
	}
	for key := range top {
		if !want[key] {
			t.Errorf("上报响应多了顶层键 %q:页面只能拿到成品结论,多一个原料键就是"+
				"给 JS 递上了可判断的东西,而这个仓库的测试覆盖不到 JS", key)
		}
	}
	for key := range want {
		if _, ok := top[key]; !ok {
			t.Errorf("上报响应缺了 %q", key)
		}
	}

	// findings 里的 verdict 必须已经是三个词之一,不是数字/布尔。
	var report struct {
		Findings []struct {
			Verdict string `json:"verdict"`
			Summary string `json:"summary"`
		} `json:"findings"`
	}
	raw, _ := json.Marshal(top)
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) == 0 {
		t.Fatal("响应里没有 findings")
	}
	for _, f := range report.Findings {
		switch f.Verdict {
		case "ok", "bad", "not checked":
		default:
			t.Errorf("verdict 必须是三个词之一,得到 %q —— 数字/布尔会逼 JS 自己映射", f.Verdict)
		}
		if f.Summary == "" {
			t.Error("summary 不能为空:页面除了显示它没有别的事可做")
		}
	}
}

// **JS 愚蠢的结构性保证之二**:Report 的字段类型里不许出现 BrowserReport 或
// LocalFacts。本机事实那一半从不下发 —— 没有它,「srflx ≠ 出口」这类结论在 JS
// 里不可能算得出来,缺的不是代码,是数据。
func TestReportTypeCarriesNoRawMaterial(t *testing.T) {
	banned := map[reflect.Type]string{
		reflect.TypeOf(leakcheck.BrowserReport{}): "浏览器原始上报",
		reflect.TypeOf(leakcheck.LocalFacts{}):    "本机事实",
	}
	typ := reflect.TypeOf(leakcheck.Report{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		ft := field.Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if why, bad := banned[ft]; bad {
			t.Errorf("Report.%s 的类型是 %s(%s):页面拿到 Report,把原料塞进去就等于"+
				"把判断权交给了测不到的 JS", field.Name, ft, why)
		}
	}
}

// **本机事实那一半从不下发**这条论证的支点,是页面能拿到的东西只有 token 与
// 第三方披露 —— 所以这里穷举 pageData 的字段名,照 TestOptionsHasNoListenAddressField
// 的形状写。
//
// 这条是变异验证逼出来的:给 pageData 加一个 `Local leakcheck.LocalFacts` 字段
// 并在模板里注入,上面三条守卫**一条都不会红**(它们守的是 Report 的类型与
// /report 的响应,不是页面模板拿到的数据)。而那一步正好把判断权交给了这个仓库
// 测不到的 JS —— Task 12 的全部论证都建立在「页面拿不到原料」上。
func TestPageDataCarriesOnlyTokenAndDisclosure(t *testing.T) {
	typ := reflect.TypeOf(pageData{})
	want := map[string]bool{"Token": true, "Endpoints": true}
	if typ.NumField() != len(want) {
		t.Fatalf("pageData 现在有 %d 个字段,守卫只认识 %d 个 —— 加字段请连同这条守卫"+
			"一起论证:页面多拿到一样原料,判断就可能搬进测不到的 JS 里",
			typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !want[field.Name] {
			t.Errorf("pageData 多了字段 %q(类型 %s):页面只该拿到 token 与第三方披露,"+
				"本机事实那一半从不下发", field.Name, field.Type)
		}
	}
	// 字段名对了、类型换成原料也一样致命(把 Endpoints 换成 LocalFacts 不会撞上
	// 上面那条数量断言)。
	banned := map[reflect.Type]string{
		reflect.TypeOf(leakcheck.BrowserReport{}): "浏览器原始上报",
		reflect.TypeOf(leakcheck.LocalFacts{}):    "本机事实",
	}
	for i := 0; i < typ.NumField(); i++ {
		ft := typ.Field(i).Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if why, bad := banned[ft]; bad {
			t.Errorf("pageData.%s 的类型是 %s(%s):这正是「页面拿不到原料」这条论证的支点",
				typ.Field(i).Name, ft, why)
		}
	}
}

// pageScriptFunction 取出页面脚本里一个具名函数的函数体。读不出来必须**响亮
// 失败** —— 一个读不懂现在代码的守卫,静默放行比没有守卫更糟。
func pageScriptFunction(t *testing.T, name string) string {
	t.Helper()
	head := "function " + name + "("
	start := strings.Index(pageHTML, head)
	if start < 0 {
		t.Fatalf("页面里找不到 %s —— 本守卫读不懂现在的代码,请连同它一起重写", name)
	}
	rest := pageHTML[start:]
	// 函数体缩进两格,收尾就是行首两格加右花括号。
	end := strings.Index(rest, "\n  }\n")
	if end < 0 {
		t.Fatalf("找不到 %s 的收尾 —— 本守卫读不懂现在的代码,请连同它一起重写", name)
	}
	return rest[:end]
}

// **每一个出网探测都必须有自己的上限,而且共用同一个预算。**
//
// 这条守卫是 JS 唯一能被 CI 看到的地方(本仓库的测试与变异验证覆盖不到页面)。
// 没有超时的 fetch 在一台没有 IPv6 的机器上会卡在 OS 的 TCP 超时(macOS 约 75 秒),
// 而 `Promise.all` 等最慢的那个 —— 整次检测于是被推向 bx 的 2 分钟硬超时,
// 也就是「浏览器那一半从来没到过」。**一个放弃的探测会说明原因;一个挂住的探测
// 让整次检测什么都说不出来。**
func TestPageProbesShareOneBudgetAndCannotHang(t *testing.T) {
	const decl = "var PROBE_TIMEOUT_MS = "
	if strings.Count(pageHTML, decl) != 1 {
		t.Fatalf("页面必须**恰好**声明一次探测预算 %q —— 两份预算会各自漂移", decl)
	}
	var budget int
	if _, err := fmt.Sscanf(pageHTML[strings.Index(pageHTML, decl)+len(decl):], "%d", &budget); err != nil {
		t.Fatalf("读不出探测预算:%v", err)
	}
	if budget <= 0 || budget > 15000 {
		t.Fatalf("探测预算 %dms 不合理:必须为正,而且要远小于 2 分钟的硬超时", budget)
	}

	echo := pageScriptFunction(t, "fetchEcho")
	if !strings.Contains(echo, "AbortController") || !strings.Contains(echo, "signal:") {
		t.Errorf("fetchEcho 必须给 fetch 带上一个会被 abort 的 signal,否则它没有上限:\n%s", echo)
	}
	if !strings.Contains(echo, "PROBE_TIMEOUT_MS") {
		t.Errorf("fetchEcho 的上限必须来自那个共用预算,不许自带一个数字:\n%s", echo)
	}

	ice := pageScriptFunction(t, "gatherSrflx")
	if !strings.Contains(ice, "PROBE_TIMEOUT_MS") {
		t.Errorf("ICE 收集的上限也必须来自那个共用预算 —— 两个探测各写一个数字,"+
			"改一个忘改另一个不会有任何报错:\n%s", ice)
	}
}
