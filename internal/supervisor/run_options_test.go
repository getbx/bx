package supervisor

import (
	"reflect"
	"testing"
)

// 注入缝的存在理由是集成台要在 netns 里跑完整 Run() 而不发任何真实外网流量。
// 它是本代码库唯一一处「组装根可以被外部指定」的缝 —— 本轮两个 Critical 都住在
// Run() 的接线里,而接线不可测正是它们能活下来的原因。
func TestOptionsExposeTunnelBuilderSeam(t *testing.T) {
	f, ok := reflect.TypeOf(Options{}).FieldByName("BuildTunnel")
	if !ok {
		t.Fatal("Options 需要 BuildTunnel 注入缝")
	}
	want := "func(string, string, bool) (*tunnel.Tunnel, error)"
	if got := f.Type.String(); got != want {
		t.Fatalf("BuildTunnel 签名必须与内部 buildTunnel 闭包一致\ngot  %s\nwant %s", got, want)
	}
}
