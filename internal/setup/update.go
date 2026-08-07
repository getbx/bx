package setup

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// TransportsBefore 是就地改传输之前的旧值,供调用方向用户展示「从什么换成了什么」。
type TransportsBefore struct {
	Server       string
	Transports   []string
	UDPTransport string
}

// UpdateTransports 就地替换配置里的传输链接,**其余一个字不动**。
//
// 为什么不能用「读进结构体 → 改 → 整份写回」:那会丢掉结构体里没有的字段、丢掉
// 注释、打乱顺序。而 /etc/bx/config.yaml 是用户会手写的(分流策略、注释),
// 真机事故 2026-08-06 里唯一能换服务器的路 `--force` 正是整份重写,会把用户手写的
// apple/steam 直连策略一起冲掉。故这里在 yaml.Node 上做外科手术。
//
// 配置不存在时报错:那条路该走全新写入(WriteConfig),不是就地改。
func UpdateTransports(path string, links []string, udpTransport string) (TransportsBefore, error) {
	var before TransportsBefore
	if len(links) == 0 {
		return before, fmt.Errorf("setup: 无传输链接")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return before, fmt.Errorf("读配置 %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return before, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	root := documentRoot(&doc)
	if root == nil {
		return before, fmt.Errorf("配置 %s 不是一个 YAML 映射", path)
	}

	before.Server = scalarValue(mappingValue(root, "server"))
	before.Transports = sequenceValues(mappingValue(root, "transports"))
	if udp := mappingValue(root, "udp"); udp != nil {
		before.UDPTransport = scalarValue(mappingValue(udp, "transport"))
	}

	// 单条走 server、多条走 transports,并清掉另一边——两处都填会让优先级含混。
	if len(links) == 1 {
		setScalar(root, "server", links[0])
		removeKey(root, "transports")
	} else {
		setSequence(root, "transports", links)
		removeKey(root, "server")
	}
	if udpTransport != "" {
		udp := mappingValue(root, "udp")
		if udp == nil {
			udp = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			appendKey(root, "udp", udp)
		}
		setScalar(udp, "transport", udpTransport)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return before, fmt.Errorf("序列化配置: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return before, fmt.Errorf("写配置 %s: %w", path, err)
	}
	return before, nil
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	node := doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

// mappingValue 返回映射里某个键的值节点;不存在返回 nil。
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func scalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func sequenceValues(node *yaml.Node) []string {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		out = append(out, item.Value)
	}
	return out
}

// setScalar 就地改写已有键的值(保住它自己那行的注释),没有就追加。
func setScalar(mapping *yaml.Node, key, value string) {
	if existing := mappingValue(mapping, key); existing != nil {
		existing.Kind = yaml.ScalarNode
		existing.Tag = "!!str"
		existing.Value = value
		existing.Content = nil
		existing.Style = 0
		return
	}
	appendKey(mapping, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setSequence(mapping *yaml.Node, key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	}
	if existing := mappingValue(mapping, key); existing != nil {
		*existing = *seq
		return
	}
	appendKey(mapping, key, seq)
}

func appendKey(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value)
}

func removeKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}
