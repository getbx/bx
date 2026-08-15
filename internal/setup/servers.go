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
		if strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(name)) {
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
		if !strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(entry, "name"))), strings.TrimSpace(name)) {
			continue
		}
		setScalar(entry, "link", link)
		if strings.TrimSpace(udp) != "" {
			setScalar(entry, "udp", udp)
		} else {
			removeKey(entry, "udp")
		}
		setScalar(root, "current", scalarValue(mappingValue(entry, "name")))
		return false, writeConfigRoot(path, doc)
	}
	list.Content = append(list.Content, serverNode(name, link, udp))
	setScalar(root, "current", name)
	return true, writeConfigRoot(path, doc)
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
	if strings.EqualFold(current, strings.TrimSpace(name)) {
		return fmt.Errorf("%q 是当前正在用的服务器;先 `bx server use <别的名字>` 再删", name)
	}
	list := mappingValue(root, "servers")
	if list == nil || list.Kind != yaml.SequenceNode {
		return fmt.Errorf("配置里没有 servers 清单")
	}
	kept := list.Content[:0]
	removed := false
	for _, entry := range list.Content {
		if strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(entry, "name"))), strings.TrimSpace(name)) {
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
