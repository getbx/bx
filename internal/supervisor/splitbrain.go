package supervisor

import (
	"fmt"
	"log"

	"github.com/getbx/bx/internal/config"
	bxdns "github.com/getbx/bx/internal/dns"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/overlay"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
)

// splitBrain 是分流脑那一相位的产物。
//
// **只带出去三样**:router(下游拿它建 dialer)、ListsOverridden(自动刷新的
// 门)、以及两个计数(只进日志)。china 列表本身与它的路径**留在相位内** ——
// 它们此前是 Run() 里四个只在二十行内被用到的局部变量,而组装根的每一个局部
// 变量都是一次「它后面还会被谁改」的阅读负担。
type splitBrain struct {
	Router *route.Router
	// ListsOverridden 为真 = 用户用自己的表替掉了内建/刷新那张。
	// **它会关掉自动刷新** —— 不能拿上游的表盖掉用户明确指定的那份;
	// 误报的代价是一台机器永远停在首装那份快照上。
	ListsOverridden bool
	DomainCount     int
	CIDRCount       int
}

// buildSplitBrain 准备 china 列表并建好路由判定。
//
// 从 Run() 抽出来是为了让它的输入输出关系**可以被断言**:「global 模式一个
// 字节的列表都不读」「CLI flag 压过 config.lists」这两条各自都对应过真实事故,
// 而它们长在 764 行的组装根里时只能靠读代码确认。
//
// **准备列表失败不是致命的**:降级成空列表继续,等刷新补 —— 一台拿不到列表的
// 机器仍然该能起来(那时 split 退化成「全走隧道」,是安全方向)。
func buildSplitBrain(cfg *config.Config, opts Options) (splitBrain, error) {
	global := cfg.Global || opts.Global

	var chinaDomain, chinaCIDR []string
	var overridden bool
	if !global {
		domainPath, cidrPath, err := provision.EnsureLists(cfg.DataDir, embedded.ChinaDomain(), embedded.ChinaCIDR())
		if err != nil {
			log.Printf("could not prepare the china list (falling back to an empty one until a refresh fills it in): %v", err)
		}
		// 列表路径覆盖优先级:CLI flag > config lists.* > 内嵌/刷新快照。
		// 搞反是安静的失败:用户以为自己换了参照表,而 bx 用的还是默认那份。
		domainOverride := firstNonEmpty(opts.ChinaDomainPath, cfg.Lists.ChinaDomain)
		cidrOverride := firstNonEmpty(opts.ChinaCIDRPath, cfg.Lists.ChinaCIDR)
		if domainOverride != "" {
			domainPath = domainOverride
		}
		if cidrOverride != "" {
			cidrPath = cidrOverride
		}
		overridden = domainOverride != "" || cidrOverride != ""
		chinaDomain = readLines(domainPath)
		chinaCIDR = readLines(cidrPath)
	}

	router, err := BuildRouter(cfg, chinaDomain, chinaCIDR)
	if err != nil {
		// 走到这里的只有「配置里那些规则/CIDR 本身是坏的」——
		// 准备列表失败上面已经降级过了,不会到这一步。
		return splitBrain{}, tagStartFailure(ErrConfig, fmt.Errorf("building the routing brain: %w", err))
	}
	router.GlobalProxy = global

	return splitBrain{
		Router:          router,
		ListsOverridden: overridden,
		DomainCount:     len(chinaDomain),
		CIDRCount:       len(chinaCIDR),
	}, nil
}

// buildSplitRoutes 把用户配置的 split 规则与在跑的 overlay 租户那组合成一张
// **有序**的路由表。
//
// **顺序即优先级**:bxdns 的 matchSplit 取**第一个**命中的路由,所以用户在
// config 里写的必须排在 overlay 那组前面 —— 否则用户为 `ts.net` 配的解析器
// 会被硬编码的那个静默遮蔽。早先 run.go 里那两个循环的顺序正好相反,而注释
// 却写着「用户可以覆盖」。
//
// 抽成纯函数是为了让这条顺序**成为行为断言**:它此前只由一条读 run.go 源码、
// 比较两个 for 循环出现位置的守卫钉着,而那条守卫自己写着「读源码是这里唯一
// 够得着的办法」—— 那句话是真的,同时也是一份待办。
//
// overlay 的解析器补端口而用户写的原样用:overlay 给的是裸 IP,而用户可能
// 故意指了非 53 端口。
func buildSplitRoutes(userRules []config.SplitRule, overlayRoutes []overlay.SplitRoute) []bxdns.SplitRoute {
	var routes []bxdns.SplitRoute
	for _, r := range userRules {
		routes = append(routes, bxdns.SplitRoute{
			Match: route.NewDomainSet(r.Domains),
			// **读 Servers,不读 Server。** 后者只是单台写法的输入形式,
			// config 在加载期把它归一化进 Servers;下游读哪一个必须只有一种答案。
			Servers: r.Servers,
		})
	}
	for _, r := range overlayRoutes {
		routes = append(routes, bxdns.SplitRoute{
			Match:   route.NewDomainSet(overlaySplitPatterns(r.Suffix)),
			Servers: []string{normalizeDNSServerAddr(r.Resolver)},
		})
	}
	return routes
}
