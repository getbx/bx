package leakcheck

import "testing"

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
