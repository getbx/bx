package leakcheck

import (
	"encoding/json"
	"testing"
	"time"
)

// 零值必须是 NotChecked。**这是本包的地基**:一个零值读作 OK 的三态,会让
// 「页面没跑成」在下游渲染成「一切正常」——设计风险四点名的那种最坏失败。
func TestZeroVerdictIsNotChecked(t *testing.T) {
	var v Verdict
	if v != NotChecked {
		t.Fatalf("零值必须是 NotChecked,得到 %v", v)
	}
	if got := v.String(); got != "not checked" {
		t.Fatalf("零值必须渲染成 %q,得到 %q", "not checked", got)
	}
}

// 词汇与 MenuRows.swift 的 MenuRowMark 一一对应(ok/bad/unknown 的 unknown 在
// 这里叫 not checked,是同一格)。**串是用户可见契约**,改它等于改菜单与页面上
// 的字,必须先改这条测试。
func TestVerdictVocabulary(t *testing.T) {
	for _, tc := range []struct {
		verdict Verdict
		want    string
	}{
		{NotChecked, "not checked"},
		{OK, "ok"},
		{Bad, "bad"},
	} {
		if got := tc.verdict.String(); got != tc.want {
			t.Errorf("%d 应渲染成 %q,得到 %q", tc.verdict, tc.want, got)
		}
	}
}

// anomalyCount 只数 bad。**not checked 永远不计入异常** —— MenuRows.swift 的
// anomalyCount 已经这么写了,这里是接上它,不是新发明。把 NotChecked 计进来,
// 一台没联网的机器就会显示一堆异常,而它什么问题都没有。
func TestAnomalyCountCountsOnlyBad(t *testing.T) {
	rep := NewReport(fixedTime(), EndpointDisclosure{}, []Finding{
		{ID: "a", Verdict: Bad},
		{ID: "b", Verdict: NotChecked},
		{ID: "c", Verdict: OK},
		{ID: "d", Verdict: NotChecked},
		{ID: "e", Verdict: Bad},
	}, nil)
	if rep.AnomalyCount != 2 {
		t.Fatalf("只有 2 条 bad,AnomalyCount 应为 2,得到 %d", rep.AnomalyCount)
	}
}

// 一份完全没有 finding 的报告,异常数是 0 而不是负数/panic,且不谎称健康:
// 判据由调用方看 Findings 是否为空。这里只钉住计数不炸。
func TestAnomalyCountOfEmptyReport(t *testing.T) {
	rep := NewReport(fixedTime(), EndpointDisclosure{}, nil, nil)
	if rep.AnomalyCount != 0 {
		t.Fatalf("空报告的 AnomalyCount 应为 0,得到 %d", rep.AnomalyCount)
	}
	if !rep.GeneratedAt.Equal(fixedTime()) {
		t.Fatalf("GeneratedAt 必须原样带出注入的时间,得到 %v", rep.GeneratedAt)
	}
}

// **报告是只写的,这是一个刻意的决定,不是漏掉了 UnmarshalJSON。**
//
// `bx leakcheck --json` 的消费方是 agent / jq,不是 Go;仓库里没有一处把 Report
// 读回来。而 Verdict 只有 MarshalJSON 这件事在这里必须被钉住,因为它有一个安静的
// 失败形状:谁将来顺手写一句 json.Unmarshal(data, &rep),得到的会是**一份所有
// verdict 都读作零值(not checked)的报告**,而 not checked 恰好又是这个功能里
// 「没问出来」的那一格 —— 一次解码事故会伪装成一次诚实的观测。
//
// 现在的行为是**响亮失败**(字符串塞不进 uint8),这条测试就锁住这一点。
// 将来真要在 Go 里读回它:补 UnmarshalJSON、让它认那三个词、把这条测试换成
// 一条真正的往返测试(含「未知词必须报错而不是落进 NotChecked」)。
func TestReportsAreWriteOnlyAndNeverDecodeSilently(t *testing.T) {
	data, err := json.Marshal(NewReport(fixedTime(), Endpoints(), []Finding{
		{ID: "a", Verdict: Bad, Summary: "leaking"},
		{ID: "b", Verdict: OK, Summary: "fine"},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(data, &back); err == nil {
		t.Fatalf("Report 现在能被解码了(%+v)—— 如果这是有意的,请连同 Verdict 的"+
			"UnmarshalJSON 一起补上并把这条测试换成真正的往返测试;"+
			"当前的危险在于一份解码失败的报告会读作「全部 not checked」,"+
			"与一次诚实的「没问出来」逐字节相同", back.Findings)
	}
}

// 可达性的坏消息是「你用不了」,而 path/identity 的坏消息是「你泄漏了」——
// 后者是安全问题,前者不是。把它算进 AnomalyCount 就是让一次连不上
// 稀释掉真正的泄漏告警(spec §6.1)。
//
// **这条守卫钉的是 NewReport 里那个 else 分支**:它今天把所有非 identity 的
// Bad 都算进 anomalies,加新 Section 而不改它,编译和现有测试都不会红。
func TestReachSectionNeverCountsAsALeak(t *testing.T) {
	findings := []Finding{
		{ID: "x", Section: SectionPath, Verdict: Bad},
		{ID: "y", Section: SectionReach, Verdict: Bad, Reach: ReachUnreachable},
		{ID: "z", Section: SectionReach, Verdict: Bad, Reach: ReachRefused},
	}
	got := NewReport(time.Now(), EndpointDisclosure{}, findings, nil)
	if got.AnomalyCount != 1 {
		t.Fatalf("AnomalyCount = %d, want 1 —— 可达性的 Bad 不许并进流量泄漏那个数", got.AnomalyCount)
	}
	if got.IdentityCount != 0 {
		t.Fatalf("IdentityCount = %d, want 0", got.IdentityCount)
	}
	if got.Reach.Unreachable != 1 || got.Reach.Refused != 1 {
		t.Fatalf("Reach = %+v, want Unreachable=1 Refused=1", got.Reach)
	}
}

// 四态各自计数,绝不合成 —— 「一条都没查出来」与「查了、全可达」在屏幕上
// 必须长得不一样(spec §6.2)。
func TestReachSummaryCountsAllFourStatesSeparately(t *testing.T) {
	findings := []Finding{
		{ID: "a", Section: SectionReach, Verdict: OK, Reach: ReachReachable},
		{ID: "b", Section: SectionReach, Verdict: OK, Reach: ReachReachable},
		{ID: "c", Section: SectionReach, Verdict: NotChecked, Reach: ReachUndetermined},
		{ID: "d", Section: SectionReach, Verdict: Bad, Reach: ReachUnreachable},
	}
	got := NewReport(time.Now(), EndpointDisclosure{}, findings, nil).Reach
	want := ReachSummary{Reachable: 2, Undetermined: 1, Unreachable: 1}
	if got != want {
		t.Fatalf("Reach = %+v, want %+v", got, want)
	}
}

// 非 reach 段的 Finding 带着 Reach 零值,不许被算进任何一格 ——
// ReachUndetermined 恰好是零值,所以「只对 SectionReach 读这个字段」是承重的。
func TestNonReachFindingsNeverTouchTheReachSummary(t *testing.T) {
	findings := []Finding{
		{ID: "p", Section: SectionPath, Verdict: OK},
		{ID: "i", Section: SectionIdentity, Verdict: Bad},
		{ID: "s", Section: SectionSurface, Verdict: Info},
	}
	if got := NewReport(time.Now(), EndpointDisclosure{}, findings, nil).Reach; got != (ReachSummary{}) {
		t.Fatalf("Reach = %+v, want 全零 —— 非 reach 段的零值 Reach 字段不许被计数", got)
	}
}
