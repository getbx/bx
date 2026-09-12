package cli

import (
	"context"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/supervisor"
)

// 这里只**采集**流量成败的事实,一句判断都没有 —— 判断在 doctor.trafficChecks。
//
// 它此前不是这样:判据(点名成片失败的规则、直连出不去时改口、UDP 那句话)
// 长在本包的 doctorOutcomeChecks 里,而那个函数**只有 `bx doctor` 的文本路径
// 调**。菜单的「Check for Problems」自从 Guardian 声明 `doctor` 能力起走的是
// `/v1/doctor` → `doctor.Judge`,于是升级之后那一整类结论从 Checks 页上消失、
// 还被一句加粗的 `0 failed · 0 warnings` 顶掉。判据搬进 doctor 之后,这一侧
// 只剩「去问一次」。

// doctorTrafficFacts 一次问齐:流量成败,以及直连出不出得去。
//
// **返回指针且永不为 nil**,而 doctor.Facts.Traffic 的 nil 语义是「这条路径
// 根本没问」——于是接线只能是 `f.Traffic = doctorTrafficFacts(ctx)` 这一个
// 表达式:想把「问不出来」压成一个具体答案,得先把它拆开,而那是看得见的。
// (上一版靠 Go 的多返回值直接当实参列表保住同一个性质,见 git 历史。)
func doctorTrafficFacts(ctx context.Context) *doctor.TrafficFact {
	report, err := supervisor.FetchStatusReport(statusSocketPath())
	fact := &doctor.TrafficFact{Report: report, DirectEgress: doctorDirectEgress(ctx)}
	if err != nil {
		fact.Err = err.Error()
	}
	return fact
}

// doctorDirectEgress 问一次「bx 自己的直连出不出得去」。
//
// **观测失败一律 Unknown**,绝不倒向任何一边:判成 False 会让一台正常机器上
// 正确的「改规则」建议被换掉;判成 True 会让这次诊断继续把系统故障说成用户
// 的配置问题。
func doctorDirectEgress(ctx context.Context) observe.Tristate {
	if observerForDoctor == nil {
		return observe.Unknown
	}
	ctx, cancel := context.WithTimeout(ctx, doctorObserveTimeout)
	defer cancel()
	return observerForDoctor(ctx).DirectEgressOK
}

// doctorObserveTimeout 与 bx status 那边同值同理由:宁可少答一项,
// 也不能让一次观测把 doctor 挂住 —— 它是出问题时最先敲的命令之一。
const doctorObserveTimeout = 5 * time.Second

// observerForDoctor 可为 nil = 这个平台没有观测原语(见 observerForPlatform)。
var observerForDoctor = observerForPlatform(runtime.GOOS)
