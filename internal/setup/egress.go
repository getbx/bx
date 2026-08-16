package setup

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"gopkg.in/yaml.v3"
)

// 具名出口的读写。**在 yaml.Node 上做外科手术**,与 AddRule 同一条纪律:
// 整份重写会把用户手写的注释与分组一起冲掉(2026-08-06 真机事故的形状)。
//
// 校验**在写盘之前**全部做完 —— 一份写了一半的配置会让 `bx up` 起不来,
// 而用户完全不知道刚才那条命令做了什么。

// EgressEntry 是清单里的一条:一个出口,以及交给它的网段。
type EgressEntry struct {
	Name   string
	Socks5 string
	CIDR   []string
}

// ListEgresses 读出全部出口与它们的网段。
func ListEgresses(path string) ([]EgressEntry, error) {
	root, _, err := loadConfigRoot(path)
	if err != nil {
		return nil, err
	}
	byName := map[string]*EgressEntry{}
	var order []string
	for _, node := range sequenceItems(root, "egress") {
		name := scalarValue(mappingValue(node, "name"))
		if name == "" {
			continue
		}
		byName[strings.ToLower(name)] = &EgressEntry{Name: name, Socks5: scalarValue(mappingValue(node, "socks5"))}
		order = append(order, strings.ToLower(name))
	}
	// 网段住在 rules 里(`- via: office` + `cidr:`),要并回来 ——
	// 分两处存是配置的形状,但用户问的是「office 现在管哪些网段」。
	for _, entry := range sequenceItems(root, "rules") {
		via := strings.TrimSpace(scalarValue(mappingValue(entry, "via")))
		if via == "" {
			continue
		}
		target, ok := byName[strings.ToLower(via)]
		if !ok {
			continue
		}
		for _, c := range sequenceItems(entry, "cidr") {
			if v := strings.TrimSpace(c.Value); v != "" {
				target.CIDR = append(target.CIDR, v)
			}
		}
	}
	out := make([]EgressEntry, 0, len(order))
	for _, key := range order {
		out = append(out, *byName[key])
	}
	return out, nil
}

// AddEgress 加一个出口(同名则更新地址)。**幂等。**
func AddEgress(path, name, socks5 string) error {
	// 用**生产那份**校验,不另写一套 —— 两份判据会漂,而漂开时 CLI 收下的
	// 配置会让 `bx up` 起不来。
	if err := (config.Egress{Name: name, Socks5: socks5}).Validate(); err != nil {
		return err
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	list := findOrCreateSequence(root, "egress")
	for _, node := range list.Content {
		if strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(node, "name"))), strings.TrimSpace(name)) {
			setScalar(node, "socks5", strings.TrimSpace(socks5))
			return writeConfigRoot(path, doc)
		}
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendKey(entry, "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(name)})
	appendKey(entry, "socks5", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(socks5)})
	list.Content = append(list.Content, entry)
	return writeConfigRoot(path, doc)
}

// RouteViaEgress 把一个网段交给某个出口。**幂等**;出口不存在时如实报错。
func RouteViaEgress(path, name, cidr string) error {
	clean := strings.TrimSpace(cidr)
	if err := config.ValidateEgressCIDR(clean); err != nil {
		return err
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	if !egressExists(root, name) {
		// **不许静默建一个出口。** 那样用户会得到一条指向不存在地址的规则,
		// 而 `bx up` 在加载期报错,信息却指向他没写过的东西。
		return fmt.Errorf("没有名为 %q 的出口;先 `bx egress add %s --socks5 <地址>`", name, name)
	}
	rules := findOrCreateSequence(root, "rules")
	for _, entry := range rules.Content {
		if !strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(entry, "via"))), strings.TrimSpace(name)) {
			continue
		}
		cidrList := mappingValue(entry, "cidr")
		if cidrList == nil || cidrList.Kind != yaml.SequenceNode {
			cidrList = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			appendKey(entry, "cidr", cidrList)
		}
		for _, node := range cidrList.Content {
			if strings.TrimSpace(node.Value) == clean {
				return nil // 已经在了 —— 不写盘,也不报错
			}
		}
		cidrList.Content = append(cidrList.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: clean})
		return writeConfigRoot(path, doc)
	}
	cidrList := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	cidrList.Content = append(cidrList.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: clean})
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendKey(entry, "via", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.TrimSpace(name)})
	appendKey(entry, "cidr", cidrList)
	rules.Content = append(rules.Content, entry)
	return writeConfigRoot(path, doc)
}

// RemoveEgress 删掉一个出口,**连同交给它的所有网段**。
//
// 只删出口、留下 via 规则的话,配置会在加载期被拒(via 指向不存在的出口)——
// 那等于这条命令把用户的 bx 弄成起不来。**不存在时如实报错**(与 RemoveRule 同)。
func RemoveEgress(path, name string) error {
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	if !egressExists(root, name) {
		return fmt.Errorf("没有名为 %q 的出口", name)
	}
	if list := mappingValue(root, "egress"); list != nil && list.Kind == yaml.SequenceNode {
		kept := list.Content[:0]
		for _, node := range list.Content {
			if strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(node, "name"))), strings.TrimSpace(name)) {
				continue
			}
			kept = append(kept, node)
		}
		list.Content = kept
		if len(list.Content) == 0 {
			removeKey(root, "egress")
		}
	}
	if rules := mappingValue(root, "rules"); rules != nil && rules.Kind == yaml.SequenceNode {
		kept := rules.Content[:0]
		for _, entry := range rules.Content {
			if strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(entry, "via"))), strings.TrimSpace(name)) {
				continue
			}
			kept = append(kept, entry)
		}
		rules.Content = kept
		if len(rules.Content) == 0 {
			removeKey(root, "rules")
		}
	}
	return writeConfigRoot(path, doc)
}

func egressExists(root *yaml.Node, name string) bool {
	for _, node := range sequenceItems(root, "egress") {
		if strings.EqualFold(strings.TrimSpace(scalarValue(mappingValue(node, "name"))), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func sequenceItems(parent *yaml.Node, key string) []*yaml.Node {
	list := mappingValue(parent, key)
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}
	return list.Content
}

func findOrCreateSequence(root *yaml.Node, key string) *yaml.Node {
	list := mappingValue(root, key)
	if list == nil || list.Kind != yaml.SequenceNode {
		list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		removeKey(root, key)
		appendKey(root, key, list)
	}
	return list
}
