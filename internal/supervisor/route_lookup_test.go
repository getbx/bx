package supervisor

import (
	"context"
	"runtime"
	"testing"
)

// 非 darwin 平台必须返回明确的不支持错误,而不是零值冒充"查到了"。
func TestLookupRouteUnsupportedOutsideDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("本用例断言非 darwin 行为")
	}
	if _, err := LookupRoute(context.Background(), "1.1.1.1", false); err == nil {
		t.Error("非 darwin 平台必须返回错误,不得静默返回零值")
	}
}
