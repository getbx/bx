package setup

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RuleKindDirect / RuleKindProxy 是仅有的两种用户规则。
const (
	RuleKindDirect = "direct"
	RuleKindProxy  = "proxy"
)

// RuleSet 是配置里全部用户规则,按去向分成两组。
type RuleSet struct {
	Direct []string `json:"direct,omitempty"`
	Proxy  []string `json:"proxy,omitempty"`
}

// 域名/通配符的形状。**在写盘之前挡掉非法输入**:一条带空格或换行的「域名」
// 进了 config,要等到下一次 bx up 才会发现 —— 而那时用户已经断过一次网了。
//
// 允许 `*.` 前缀(bx 的 DomainSet 认它),其余必须是普通域名标签。
var domainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// ValidateRulePattern 校验一条规则的写法,并返回归一化后的形式(小写、去空白)。
func ValidateRulePattern(pattern string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return "", fmt.Errorf("规则不能为空")
	}
	if strings.ContainsAny(p, " \t\n\r'\"") {
		return "", fmt.Errorf("规则 %q 含空白或引号 —— 域名里不该有这些", pattern)
	}
	if !domainPattern.MatchString(p) {
		return "", fmt.Errorf("规则 %q 不是一个域名或 *.域名", pattern)
	}
	return p, nil
}

func validateKind(kind string) error {
	switch kind {
	case RuleKindDirect, RuleKindProxy:
		return nil
	default:
		return fmt.Errorf("规则类型只能是 %s 或 %s,得到 %q", RuleKindDirect, RuleKindProxy, kind)
	}
}

// ListRules 读出配置里的全部用户规则。
func ListRules(path string) (RuleSet, error) {
	var out RuleSet
	root, _, err := loadConfigRoot(path)
	if err != nil {
		return out, err
	}
	rules := mappingValue(root, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return out, nil
	}
	for _, entry := range rules.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		out.Direct = append(out.Direct, sequenceValues(mappingValue(entry, RuleKindDirect))...)
		out.Proxy = append(out.Proxy, sequenceValues(mappingValue(entry, RuleKindProxy))...)
	}
	return out, nil
}

// AddRule 往指定去向里加一条规则。**幂等**:已存在时原样返回成功而不写盘。
//
// 幂等是因为用户会重复点。写进去两条一样的规则除了让归因计数分裂之外没有任何效果。
func AddRule(path, kind, pattern string) error {
	if err := validateKind(kind); err != nil {
		return err
	}
	clean, err := ValidateRulePattern(pattern)
	if err != nil {
		return err
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	list := findOrCreateRuleList(root, kind)
	for _, node := range list.Content {
		if strings.EqualFold(strings.TrimSpace(node.Value), clean) {
			return nil // 已经在了 —— 不写盘,也不报错
		}
	}
	list.Content = append(list.Content, &yaml.Node{
		Kind: yaml.ScalarNode, Tag: "!!str", Value: clean,
		// 带 `*` 的值在 YAML 里是别名语法,必须加引号才是字面量。
		// 这不是美观问题:不加引号写出去的配置**读回来会报解析错误**。
		Style: quoteStyleFor(clean),
	})
	return writeConfigRoot(path, doc)
}

// RemoveRule 删掉一条规则。**不存在时如实报错。**
//
// 静默成功会让菜单显示「已删除」而配置一个字没变 —— 用户据此重启一次网络,
// 然后发现问题还在,而且再也不会怀疑这一步。
func RemoveRule(path, kind, pattern string) error {
	if err := validateKind(kind); err != nil {
		return err
	}
	clean, err := ValidateRulePattern(pattern)
	if err != nil {
		return err
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	rules := mappingValue(root, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return fmt.Errorf("配置里没有 rules 段,规则 %q 无从删起", pattern)
	}
	removed := false
	for _, entry := range rules.Content {
		list := mappingValue(entry, kind)
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		kept := list.Content[:0]
		for _, node := range list.Content {
			if strings.EqualFold(strings.TrimSpace(node.Value), clean) {
				removed = true
				continue
			}
			kept = append(kept, node)
		}
		list.Content = kept
	}
	if !removed {
		return fmt.Errorf("规则 %q 不在 %s 列表里", pattern, kind)
	}
	return writeConfigRoot(path, doc)
}

// quoteStyleFor 决定要不要给值加引号。
//
// 带 `*` 的值不加引号在 YAML 里是**别名语法**,写出去的配置读回来会报解析错误。
func quoteStyleFor(value string) yaml.Style {
	if strings.ContainsAny(value, "*&!%@`") {
		return yaml.SingleQuotedStyle
	}
	return 0
}

// findOrCreateRuleList 找到(或建出)指定去向的那个列表。
//
// rules 是一个「每项是 {direct: [...]} 或 {proxy: [...]}」的序列。优先复用**已有**
// 那一项:另起一项在语义上等价,但会把用户按主题分好的组打散。
func findOrCreateRuleList(root *yaml.Node, kind string) *yaml.Node {
	rules := mappingValue(root, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		rules = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		removeKey(root, "rules")
		appendKey(root, "rules", rules)
	}
	for _, entry := range rules.Content {
		if list := mappingValue(entry, kind); list != nil && list.Kind == yaml.SequenceNode {
			return list
		}
	}
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendKey(entry, kind, list)
	rules.Content = append(rules.Content, entry)
	return list
}

// loadConfigRoot 读配置并返回根映射与整个文档。
//
// 与 UpdateTransports 同一条纪律:**在 yaml.Node 上做外科手术**,不整份重写 ——
// 后者会把用户手写的注释与分组一起冲掉(2026-08-06 真机事故的形状)。
func loadConfigRoot(path string) (*yaml.Node, *yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读配置 %s: %w", path, err)
	}
	doc := &yaml.Node{}
	if err := yaml.Unmarshal(raw, doc); err != nil {
		return nil, nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	root := documentRoot(doc)
	if root == nil {
		return nil, nil, fmt.Errorf("配置 %s 不是一个 YAML 映射", path)
	}
	return root, doc, nil
}

func writeConfigRoot(path string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	// **原子替换。** 这个文件有两个进程会读(Core 与 CLI),写到一半被读到
	// 会让下一次 bx up 拿到半份配置 —— 与本仓库其它持久化状态同一条纪律。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("写配置 %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换配置 %s: %w", path, err)
	}
	return nil
}
