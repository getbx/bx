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
}
