package supervisor

import (
	"context"
	"runtime"
	"testing"
)

// **成功的答案必须真的是个答案。**
//
// 这条守卫此前写作「非 darwin 必须返回错误」,守的其实是它的下半句:
// 「不得静默返回零值冒充查到了」。Linux 有了实现之后,那个写法就只剩下平台清单的
// 意思,而承重的是这一条 —— 它在**每个**平台上都成立:
//
//	err == nil  ⟹  拿到了接口,**或者**内核明说这条路被 reject
//
// Reject 那一支的 Interface 本来就是空的(没有出口,因为包会被扔掉),
// 所以不能简单地要求「成功就得有接口」—— 那会把一个正确的答案判成违规。
func TestLookupRouteNeverReturnsAnEmptyAnswerAsSuccess(t *testing.T) {
	selection, err := LookupRoute(context.Background(), "1.1.1.1", false)
	if err != nil {
		// 查不到要**说出来**,这正是本条守卫的另一半。
		return
	}
	if selection.Interface == "" && !selection.Reject {
		t.Fatalf("err 为 nil 却什么都没答上:%+v —— 零值冒充「查到了」正是这条守卫要挡的", selection)
	}
}

// 没有实现的平台必须明确报错,而不是零值冒充。
func TestLookupRouteIsExplicitlyUnsupportedWhereItIsNotImplemented(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux":
		t.Skip("这两个平台有实现,行为由上面那条守卫覆盖")
	}
	if _, err := LookupRoute(context.Background(), "1.1.1.1", false); err == nil {
		t.Error("没有实现的平台必须返回错误,不得静默返回零值")
	}
}
