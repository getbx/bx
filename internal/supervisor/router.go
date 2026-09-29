// Package supervisor 顶层编排:把配置、隧道、TUN 引擎、分流脑接成可运行的 bx。
package supervisor

import (
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/routerbuild"
)

// BuildRouter 从配置规则 + china 列表构建分流脑。**判据在 internal/routerbuild,这里是薄壳**
// (手机端要按同一份判据回答「走哪」,而它不能依赖 supervisor)。
func BuildRouter(cfg *config.Config, chinaDomain, chinaCIDR []string) (*route.Router, error) {
	return routerbuild.Build(cfg, chinaDomain, chinaCIDR)
}
