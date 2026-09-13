package setup

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"gopkg.in/yaml.v3"
)

// 本文件是「用户手选的服务器清单」的写入侧。
//
// **它与自动容灾是两件相反的事。** `transports:` 是优先级表(不健康就自动切),
// 而自动切会**悄悄改变你的出口 IP** —— 那正是项目所有者明确要求退场的东西。
// `servers:` + `current:` 是用户自己选,而 config.resolveServers 只把当前那一条
// 放进 Transports,好让 runFailover 根本起不来。
//
// 与 UpdateTransports 同一条纪律:**在 yaml.Node 上做外科手术**,不整份重写 ——
// 后者会把用户手写的注释与分流策略一起冲掉(2026-08-06 真机事故)。

// SameServerName 是「这两个名字指的是不是同一台」的**唯一**一份判据:大小写与
// 首尾空白都不计较。
//
// 它存在的理由是这条判据此前在三处各写了一份同样的 `EqualFold(TrimSpace…)` ——
// guardian 的 serverNamed、guardian 的 replace 里一份内联、以及本文件里的
// RemoveServer。漂移的后果不是崩溃:一处认得出而另一处认不出时,菜单会说
// 「已经删掉了」而配置里那一行还在(或者反过来,查得到却写不进去)。
// 一处收紧成大小写敏感,`bx server rm Osaka` 就会静默什么都不做。
func SameServerName(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ListServers 读出清单与当前选中的那台。
func ListServers(path string) ([]config.Server, string, error) {
	root, _, err := loadConfigRoot(path)
	if err != nil {
		return nil, "", err
	}
	return readServers(root), scalarValue(mappingValue(root, "current")), nil
}

// SetCurrentServer 把 current 换成清单里的另一台。
//
// **名字不在清单里必须报错,并列出可选项。** 静默接受会让用户以为切过去了,
// 重启之后才发现还在原来那台 —— 那正是 2026-08-06「以为换了服务器其实没换」的形状。
func SetCurrentServer(path, name string) error {
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	servers := readServers(root)
	if len(servers) == 0 {
		return fmt.Errorf("配置里没有 servers 清单;先用 `bx setup` 或 `bx server add` 加一台")
	}
	for _, s := range servers {
		if SameServerName(s.Name, name) {
			// 存回**清单里的原样拼写**,而不是用户敲的大小写。
			setScalar(root, "current", s.Name)
			return writeConfigRoot(path, doc)
		}
	}
	return fmt.Errorf("没有名为 %q 的服务器;清单里有:%s", name, strings.Join(serverNames(servers), "、"))
}

// UpsertServer 加一台或就地更新同名的那一台,并把 current 设成它。
//
// 首次调用时若配置还是旧式的 `server:` + `udp.transport:`,先把旧的迁成 servers[0] ——
// 用户手里绝大多数是那种,加第二台时不该要求他先手工改格式。
func UpsertServer(path, name, link, udp string) (added bool, err error) {
	return addServer(path, name, link, udp, true)
}

// AddServer 把一台加进清单,**但绝不改变现在在用的是哪一台** —— 除非清单本来
// 就没有 current,那时它把**此刻实际在用的那一台**写上去(见 settleCurrent)。
//
// 它与 UpsertServer 的区别只有这一条,而这一条是要害:刚部署好一台新 VPS
// **不构成**「把我的出口换过去」的请求。换出口是有后果的事(登录态、风控、
// 正在下载的东西),项目所有者明确要求它必须是人显式的一下 —— 这也正是自动
// 容灾被否掉的理由。顺手把 current 改掉,就是用另一个入口把它偷偷做了。
func AddServer(path, name, link, udp string) (added bool, err error) {
	return addServer(path, name, link, udp, false)
}

func addServer(path, name, link, udp string, makeCurrent bool) (added bool, err error) {
	if err := config.ValidateServerName(name); err != nil {
		return false, err
	}
	if strings.TrimSpace(link) == "" {
		return false, fmt.Errorf("服务器 %q 的链接不能为空", name)
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return false, err
	}
	list := mappingValue(root, "servers")
	if list == nil || list.Kind != yaml.SequenceNode {
		list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		removeKey(root, "servers")
		appendKey(root, "servers", list)
		// **迁移旧式单服务器配置。** 丢掉它的 UDP 会让切回去时 UDP 静默走主传输。
		if legacy := scalarValue(mappingValue(root, "server")); legacy != "" {
			legacyName, nerr := config.DeriveServerName(legacy)
			if nerr != nil {
				legacyName = "legacy"
			}
			var legacyUDP string
			if u := mappingValue(root, "udp"); u != nil {
				legacyUDP = scalarValue(mappingValue(u, "transport"))
			}
			list.Content = append(list.Content, serverNode(legacyName, legacy, legacyUDP))
			removeKey(root, "server")
			if u := mappingValue(root, "udp"); u != nil {
				removeKey(u, "transport")
			}
		}
	}
	for _, entry := range list.Content {
		if !SameServerName(scalarValue(mappingValue(entry, "name")), name) {
			continue
		}
		setScalar(entry, "link", link)
		if strings.TrimSpace(udp) != "" {
			setScalar(entry, "udp", udp)
		} else {
			removeKey(entry, "udp")
		}
		settleCurrent(root, list, scalarValue(mappingValue(entry, "name")), makeCurrent)
		return false, writeConfigRoot(path, doc)
	}
	list.Content = append(list.Content, serverNode(name, link, udp))
	settleCurrent(root, list, name, makeCurrent)
	return true, writeConfigRoot(path, doc)
}

// settleCurrent 决定这次写入之后 current: 那一格是什么。**判据只有一份**,两个
// 写入分支(改写同名那一台 / 追加一台)共用 —— 此前它们各写了一份同样的条件,
// 而这个函数要守的性质恰恰是「两条路都不许挪动出口」。
//
// makeCurrent 是 UpsertServer(`bx setup`「用这一台」)那条路,它就是来换出口的:
// 指名 named 那一台。
//
// 否则**只在 current 空着时**填,而且填的是**此刻实际在用的那一台** ——
// config.resolveServers 对没有 current 的清单回落 servers[0],所以在用的是清单
// 里第一台,**不是刚加进来的那一台**。这半步曾经写成「填新加的那台」,后果是
// `bx setup` 写出来的 legacy `server:` 配置(迁移建清单时不写 current)与手改
// 出来的无 current 清单上,「Add Server…」会把出口悄悄挪到新加的那台;没有热切,
// 所以要到下一次 `bx up` 才发作 —— 比立刻切更难归因。
//
// 清单本来就是空的时候,servers[0] 就是刚加的那一台,于是「加第一台之后
// current 必须有值」照旧成立 —— 不是靠一条特例,是同一条规则的结果。
func settleCurrent(root, list *yaml.Node, named string, makeCurrent bool) {
	if makeCurrent {
		setScalar(root, "current", named)
		return
	}
	if strings.TrimSpace(scalarValue(mappingValue(root, "current"))) != "" {
		return
	}
	if len(list.Content) == 0 {
		return
	}
	// 名字为空的条目 config.Parse 本来就会拒;这里不拿它去写一个空 current。
	if first := strings.TrimSpace(scalarValue(mappingValue(list.Content[0], "name"))); first != "" {
		setScalar(root, "current", first)
	}
}

// ReplaceServerLink 就地换掉同名那一台的链接:凭据轮换,或者 VPS 重建换了地址。
//
// **它与 AddServer / UpsertServer 的区别只有一条,而那一条是要害:它任何情况下
// 都不动 current,连「本来是空的」也不填。** UpsertServer 会把 current 设成被改
// 的那一台(它服务的是 `bx setup`「用这一台」);AddServer 只在 current 空着时
// 填,填的是清单里第一台 —— 对**加一台**那是对的(把此刻实际在用的那一台写明白),
// 对换链接就是**多写了一个用户没写过的键**:一份没有 current: 的清单**照样在跑**
// (config.resolveServers 回落 servers[0]),而手改出来的配置正是这个样子;
// 替它把 current 钉死之后,RemoveServer 从此会拒绝删掉那一台,而用户只是换了
// 一条链接。(它**不再**像 2026-09-13 之前那样把出口挪到被改的那一台 ——
// 那半步已经修掉,见 settleCurrent;但「换链接不改配置形状」这条仍然成立。)
//
// **名字不在清单里报错,绝不顺手加一台** —— 敲错一个字母就凭空多出一台顶着
// 新链接的服务器,而调用方只会看到成功。
//
// UDP 那一格按参数字面处理(给空就删掉);「省略即保留」是调用方的判断,
// 不藏在这一层。
func ReplaceServerLink(path, name, link, udp string) error {
	if strings.TrimSpace(link) == "" {
		return fmt.Errorf("服务器 %q 的链接不能为空", name)
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	list := mappingValue(root, "servers")
	if list == nil || list.Kind != yaml.SequenceNode {
		return fmt.Errorf("配置里没有 servers 清单")
	}
	for _, entry := range list.Content {
		if !SameServerName(scalarValue(mappingValue(entry, "name")), name) {
			continue
		}
		setScalar(entry, "link", strings.TrimSpace(link))
		if strings.TrimSpace(udp) != "" {
			setScalar(entry, "udp", strings.TrimSpace(udp))
		} else {
			removeKey(entry, "udp")
		}
		return writeConfigRoot(path, doc)
	}
	return fmt.Errorf("没有名为 %q 的服务器", name)
}

// RemoveServer 从清单里删掉一台。
//
// **不许删掉当前正在用的那台**:那会让配置指向一个不存在的名字,下一次启动直接
// 起不来 —— 而用户只是想清理一条不用的记录。
func RemoveServer(path, name string) error {
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	current := strings.TrimSpace(scalarValue(mappingValue(root, "current")))
	if SameServerName(current, name) {
		return fmt.Errorf("%q 是当前正在用的服务器;先 `bx server use <别的名字>` 再删", name)
	}
	list := mappingValue(root, "servers")
	if list == nil || list.Kind != yaml.SequenceNode {
		return fmt.Errorf("配置里没有 servers 清单")
	}
	kept := list.Content[:0]
	removed := false
	for _, entry := range list.Content {
		if SameServerName(scalarValue(mappingValue(entry, "name")), name) {
			removed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !removed {
		return fmt.Errorf("没有名为 %q 的服务器", name)
	}
	list.Content = kept
	return writeConfigRoot(path, doc)
}

func serverNode(name, link, udp string) *yaml.Node {
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendKey(entry, "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
	appendKey(entry, "link", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: link})
	if strings.TrimSpace(udp) != "" {
		appendKey(entry, "udp", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: udp})
	}
	return entry
}

func readServers(root *yaml.Node) []config.Server {
	list := mappingValue(root, "servers")
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]config.Server, 0, len(list.Content))
	for _, entry := range list.Content {
		out = append(out, config.Server{
			Name: scalarValue(mappingValue(entry, "name")),
			Link: scalarValue(mappingValue(entry, "link")),
			UDP:  scalarValue(mappingValue(entry, "udp")),
		})
	}
	return out
}

func serverNames(servers []config.Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}
