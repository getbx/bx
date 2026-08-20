package supervisor

import (
	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/tun"
)

// wireAppAttribution 把**同一个** *AppTraffic 同时接到 dialer(记判定)与
// engine(记字节)上,并返回 tun.New 要的那个 Option。
//
// **一次调用产出两处接线,是这个函数存在的全部理由。** 两边各拿一个实例时,
// 连接记录落在 A、字节账落在 B,而 A 与 B 各自看起来都完全正常:界面上每个
// 应用都有连接数、字节数恒为 0 —— 没有任何一处会报错。这个仓库全部的事故都
// 在组装根上,而组装根恰恰是单测进不去的地方;把它收成一个函数,是为了让
// 「同一个实例」这条不变量有一个测得着的落点
// (TestWireAppAttributionGivesDialerAndEngineTheSameInstance)。
func wireAppAttribution(d *dialer.Dialer, at *AppTraffic) tun.Option {
	d.AppRecorder = at
	return tun.WithByteAttribution(at)
}
