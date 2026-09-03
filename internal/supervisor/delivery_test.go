package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/stats"
)

// **用窗口内的增量,不是累计值。**
// 累计值会让一台昨天出过问题的机器永远显示不健康 —— 那正好是这个功能要消灭的
// 东西的镜像:另一种不该有的颜色。
func TestDeliveryMonitorJudgesTheWindowNotAllOfHistory(t *testing.T) {
	m := &deliveryMonitor{}
	m.observe(1000, 1000, false) // 基线:历史上全是失败
	m.observe(1050, 1000, true)  // 这一窗 50 次全成功
	if w := m.warning(); w != nil {
		t.Errorf("拿历史累计下了判断:%+v", w)
	}
}

// 窗口未满时不改判断 —— 半个窗口的数字与一个完整窗口的含义不同,
// 拿它下判断就是把门槛悄悄降低。
func TestDeliveryMonitorWaitsForAFullWindow(t *testing.T) {
	m := &deliveryMonitor{}
	m.observe(0, 0, false)
	m.observe(50, 50, false) // 窗口没到
	if w := m.warning(); w != nil {
		t.Errorf("窗口没满就报了:%+v", w)
	}
	m.observe(50, 50, true)
	if m.warning() == nil {
		t.Error("一个完整窗口里 50 次全失败,却一个字没说")
	}
}

// **计数器回退(Core 重启)之后必须能自己恢复。**
//
// 判「这一窗作废」的活儿由 stats.JudgeDelivery 干(它把不可能的计数判成
// Unknown);这里要钉的是**重新取基线**这半件事 —— 少了它,重启之后每一窗
// 都拿一个陈旧的基线去算,监视器会长时间失明而没有任何一处报错。
//
// (第一版在 observe 里另写了一个「回退就作废」的分支,变异实测证明它是死代码:
// 两条路都会走到同一次重新取基线,判断本来就是 Unknown。已删。)
func TestDeliveryMonitorRebaselinesAfterACounterReset(t *testing.T) {
	m := &deliveryMonitor{}
	m.observe(900, 900, false)
	m.observe(10, 10, true) // 计数器归零了
	if w := m.warning(); w != nil {
		t.Errorf("跨重启的差值被当成了判据:%+v", w)
	}
	// 重新取过基线之后照常工作。
	m.observe(40, 40, true)
	if m.warning() == nil {
		t.Error("重新取基线之后没能恢复判断")
	}
}

// **接线守卫**:算出来的告警真的被拼进了 Report。
// 一个算出来了却没被拼进去的告警,与没有这个功能在输出上完全一样 ——
// 而这个仓库全部的事故都在组装根上。
func TestDeliveryWarningReachesTheReport(t *testing.T) {
	m := &deliveryMonitor{}
	m.observe(0, 0, false)
	m.observe(40, 40, true)

	got := appendDeliveryWarning([]stats.Warning{{Name: "existing"}}, m)
	if len(got) != 2 {
		t.Fatalf("告警没被拼进去:%+v", got)
	}
	if got[0].Name != "existing" {
		t.Error("把既有告警挤掉了")
	}
	if got[1].Name != "tunnel_not_delivering" {
		t.Errorf("拼进去的不是那条:%+v", got[1])
	}
	if !strings.Contains(got[1].Detail, "40") {
		t.Errorf("观测到的数字没带上:%s", got[1].Detail)
	}
}

// **没接线时一个字都不说,而不是「一切正常」。**
// nil monitor 是「这个部署没有这份观测」,与「观测过、没问题」是两件事。
func TestDeliveryWarningIsSilentWithoutAMonitor(t *testing.T) {
	base := []stats.Warning{{Name: "existing"}}
	if got := appendDeliveryWarning(base, nil); len(got) != 1 {
		t.Errorf("没接线却动了告警列表:%+v", got)
	}
}

// 一切正常时不占地方 —— 这是它不被训练成噪声的前提。
func TestDeliveryWarningStaysQuietWhenTrafficFlows(t *testing.T) {
	m := &deliveryMonitor{}
	m.observe(0, 0, false)
	m.observe(100, 2, true)
	if got := appendDeliveryWarning(nil, m); len(got) != 0 {
		t.Errorf("流量正常却报了:%+v", got)
	}
}
