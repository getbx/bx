//go:build darwin

package supervisor

import (
	"errors"
	"testing"
)

// **「scoped 表里没有这条」是一个答案,别的错误不是。**
//
// `route -n get -ifscope <dev> <ip>` 查不到时以非零退出并打 `not in table` ——
// 那正是我们要观测的「出不去」。而超时、权限不足、命令不存在都是**观测失败**,
// 把它们一律当成「出不去」会让每一台机器在 route 抽风时恒红,
// 与本仓库对 Tristate 的纪律相反(问不出来 ≠ 问出了「没有」)。
func TestIsScopedRouteMissingOnlyMatchesTheRealAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"内核说不在表里", errors.New("exit status 1: route: writing to routing socket: not in table"), true},
		{"大小写不同", errors.New("Not In Table"), true},
		{"超时", errors.New("signal: killed"), false},
		{"权限不足", errors.New("route: must be root to alter routing table"), false},
		{"命令不存在", errors.New(`exec: "route": executable file not found in $PATH`), false},
		{"空错误", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isScopedRouteMissing(tc.err); got != tc.want {
				t.Fatalf("isScopedRouteMissing(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// 本机真实一跑:**只断言它不 panic、且三态自洽**。
//
// 不断言具体结果 —— 这台机器此刻直连通不通取决于有没有装 scoped 路由,
// 而那正是被观测的对象。一个把当下状态写死的测试,会在修好之后转红。
func TestDirectEgressReachableIsSelfConsistentOnThisMachine(t *testing.T) {
	reachable, known, err := DirectEgressReachable(t.Context())
	if err != nil {
		t.Skipf("这台机器上探测失败(可接受,观测层会记 Unknown):%v", err)
	}
	if !known && reachable {
		t.Fatal("报了「不知道」却同时说「出得去」—— 三态自相矛盾")
	}
}

// **判定本身的表驱动覆盖。** 上一版这段逻辑住在一个直接 exec 的函数里,
// 测试进不去 —— 变异验证当场发现「把 not-in-table 判据换成恒真」无人察觉。
func TestDecideDirectEgress(t *testing.T) {
	notInTable := errors.New("route: writing to routing socket: not in table")
	boom := errors.New("signal: killed")
	for _, tc := range []struct {
		name                string
		dev                 string
		devErr, lookupErr   error
		iface               string
		wantReach, wantKnow bool
		wantErr             bool
	}{
		{name: "出得去", dev: "en0", iface: "en0", wantReach: true, wantKnow: true},
		{name: "scoped 表里没有 → 出不去(这是答案)", dev: "en0", lookupErr: notInTable, wantKnow: true},
		{name: "查询失败 → 问不出来", dev: "en0", lookupErr: boom, wantErr: true},
		{name: "探不到物理网卡 → 问不出来", dev: "  "},
		{name: "默认路由查不了 → 问不出来", devErr: boom, wantErr: true},
		{name: "路由落在别的网卡上 → 出不去", dev: "en0", iface: "utun0", wantKnow: true},
		{name: "大小写与空白不影响比对", dev: "en0", iface: " EN0 ", wantReach: true, wantKnow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reach, known, err := decideDirectEgress(tc.dev, tc.devErr, tc.iface, tc.lookupErr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if reach != tc.wantReach || known != tc.wantKnow {
				t.Fatalf("= (reachable=%v, known=%v), want (%v, %v)", reach, known, tc.wantReach, tc.wantKnow)
			}
		})
	}
}
