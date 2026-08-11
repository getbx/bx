package leakcheck

import "testing"

// 计划外补的两条守卫。**理由是实测**:把 `Empty()` 改成恒 `false`、把 `Known()`
// 改成恒 `false`,全量 `go test ./...` 一条都不转红 —— 两个方法在本任务落地时
// 是**零覆盖**的生产代码,而它们正是「没观测到」与「观测到了」的分界线。
// 本仓库反复栽在「绿得没道理」上,所以在它们还只有几行的时候就把语义钉住。

// Empty 回答的是「这份上报里有没有任何**观测结果**」,不是「有没有发生过任何事」。
// 三个 Err 字段刻意不参与判断:一份三项全失败的上报里,观测结果依然是零 ——
// 而下游要的正是这个区别,失败的原因由各条规则自己去读 Err。
func TestBrowserReportEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report BrowserReport
		want   bool
	}{
		{"零值", BrowserReport{}, true},
		{
			// 页面跑起来了、三项全失败:仍然是「什么都没观测到」。
			"只有错误也算空",
			BrowserReport{ExitV4Err: "load failed", ExitV6Err: "load failed", STUNErr: "ICE failed"},
			true,
		},
		{
			// UserAgent 是页面自报的,不是观测到的网络事实,不能让上报显得非空。
			"只有 UserAgent 也算空",
			BrowserReport{UserAgent: "Mozilla/5.0"},
			true,
		},
		{"有 v4 出口", BrowserReport{ExitV4: "5.6.7.8"}, false},
		{"有 v6 出口", BrowserReport{ExitV6: "2001:db8::1"}, false},
		{"有 srflx", BrowserReport{SRFLX: []string{"1.2.3.4"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.report.Empty(); got != tc.want {
				t.Fatalf("Empty() 应为 %v,得到 %v(report=%+v)", tc.want, got, tc.report)
			}
		})
	}
}

// Known 表示「这一项确实观测到了一个接口」。**带 Err 的接口名不算观测到** ——
// 采集半途失败时留下的名字可能是残缺的,把它当成事实正是本功能要消灭的那种
// 「看起来像答案的非答案」。
func TestInterfaceRefKnown(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  InterfaceRef
		want bool
	}{
		{"零值不算观测到", InterfaceRef{}, false},
		{"有名字无错误", InterfaceRef{Name: "utun4"}, true},
		{"有名字也有错误:不算", InterfaceRef{Name: "utun4", Err: "route lookup failed"}, false},
		{"只有错误", InterfaceRef{Err: "route lookup failed"}, false},
		{"只有 Display 翻译名、没有接口名:不算", InterfaceRef{Display: "Unidentified VPN"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.Known(); got != tc.want {
				t.Fatalf("Known() 应为 %v,得到 %v(ref=%+v)", tc.want, got, tc.ref)
			}
		})
	}
}
