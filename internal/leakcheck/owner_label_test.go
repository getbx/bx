package leakcheck

import (
	"strings"
	"testing"
)

// **接口的人话名字里已经含了归属,外面别再包一层。**
//
// 真机(2026-08-11)打出来的是 `the tunnel bx is managing (bx (utun0))` —— bx 出现
// 两次。原因是 describeRef 走 DescribeInterface,而后者本来就把 utun0 翻成
// 「bx (utun0)」;describeOwner 又在外面套了「the tunnel bx is managing (…)」。
// 别人的隧道同样重复:「the VPN on Work VPN (utun4)」。
func TestOwnerLabelDoesNotRepeatTheOwner(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local LocalFacts
	}{
		{"bx 自己", LocalFacts{
			DefaultRouteV4: InterfaceRef{Name: "utun0", Display: "bx (utun0)"},
			BXTunInterface: "utun0",
		}},
		{"别人的 VPN", LocalFacts{
			DefaultRouteV4: InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := describeOwner(WhoOwnsTheRoute(tc.local), tc.local)
			if strings.Count(label, "(") > 1 {
				t.Errorf("归属标签套了两层括号:%q", label)
			}
			for _, word := range []string{"bx", "VPN"} {
				if strings.Count(label, word) > 1 {
					t.Errorf("归属标签里 %q 出现了 %d 次:%q",
						word, strings.Count(label, word), label)
				}
			}
		})
	}
}
