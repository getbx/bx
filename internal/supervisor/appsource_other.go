//go:build !darwin

package supervisor

import "github.com/getbx/bx/internal/appattr"

type unsupportedAppSource struct{}

func newAppSource() appSource { return unsupportedAppSource{} }

// **返回 nil map + 错误,不是空 map。** 空 map 会被上层读成「查过了,一个应用
// 都没有」,而那是句自洽的假话 —— 与 internal/observe 拒绝把「没问过」报成
// 「不归 bx」同源。
func (unsupportedAppSource) OwnersByPort() (map[appattr.PortKey]string, error) {
	return nil, errAppSourceUnsupported
}
