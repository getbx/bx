package leakserve

import (
	_ "embed"
	"html/template"

	"github.com/getbx/bx/internal/leakcheck"
)

//go:embed page.html
var pageHTML string

var pageTemplate = template.Must(template.New("leakcheck").Parse(pageHTML))

// pageData 是页面拿到的**全部**东西:一个 token 和一份第三方披露。
//
// **本机事实那一半从不出现在这里,这是「JS 保持愚蠢」的结构性保证。**
// 没有 LocalFacts,「srflx ≠ 出口」这类结论在页面里不可能算得出来——缺的不是
// 代码,是数据。判断只可能发生在 Go 里,而那是这个仓库测得到的地方。
//
// 字段集合由 page_test.go 的 TestPageDataCarriesOnlyTokenAndDisclosure 穷举钉住。
type pageData struct {
	Token     string
	Endpoints leakcheck.EndpointDisclosure
	// ChecksJSON 是三条结论的**骨架**(id / 标题 / 它等哪几个浏览器探测),
	// 以 JSON 字面量嵌进页面。
	//
	// **它是结构,不是可判断的原料。** 里面没有任何一条观测:没有地址、没有接口名、
	// 没有本机事实 —— 页面拿它只能把行摆出来,推不出任何结论。判断仍然全部在 Go。
	//
	// 为什么结构也从 Go 来:「哪一项吃哪几个探测」是判据的知识。让页面自己抄一份,
	// 加第四条规则时要改两个地方,而其中一个(JS)这个仓库测不到 —— 两边悄悄不
	// 一致时没有任何东西会红。骨架与真实产出由
	// leakcheck.TestOutlineMatchesWhatJudgeActuallyEmits 钉住。
	ChecksJSON template.JS
}
