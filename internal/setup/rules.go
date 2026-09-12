package setup

import (
	"fmt"
	"os"
	"strings"

	"github.com/getbx/bx/internal/policy"
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

// ValidateRulePattern 校验一条规则的写法,并返回归一化后要写进配置的那一行。
//
// **判定在 internal/policy,一份,两条写入路径共用。** 另一条路是
// `bx direct add` / `bx proxy add` 与 MCP 的 bx_apply_policy(policy.Apply/Edit);
// 两份校验器会让同一个串在菜单里被拒、在命令行里被接受,而用户无从分辨谁对。
//
// **2026-09-12 起也收 IP 与 CIDR**:supervisor.BuildRouter 一直把 rules 里的
// 网段条目分流给 CIDRSet,而这边的域名正则把它们全拒了 —— 菜单右键对一个 IP
// 目的地给出的候选(AppTrafficModel.ruleCandidates 的「IP 原样」)按构造加不进去。
// 校验器比它守着的那个面窄,拒的就是合法配置。
func ValidateRulePattern(pattern string) (string, error) {
	return policy.ValidateRulePattern(pattern)
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

// ApplyGroup 整组打开或关掉一批规则,**一次写盘**。
//
// 逐条调用 AddRule/RemoveRule 也能做到,但那会在中途留下半开半关的配置;
// 而且中途失败时用户看到的是一个既不是原样、也不是目标的状态。
//
// **不对称之处是刻意的**:
//   - 打开:已存在的跳过(幂等,用户会重复点)。
//   - 关掉:组里本来就没装的**不算失败** —— 单条 RemoveRule 对「不存在」如实
//     报错是对的(用户明确点了那一行),而整组关闭时组里本来就可能只装了一半,
//     那时报错会让一次完全正常的操作看起来失败了。
//
// 校验在**动盘之前全部做完**:半开的组比不开更难解释。
func ApplyGroup(path, kind string, patterns []string, enable bool) error {
	if err := validateKind(kind); err != nil {
		return err
	}
	clean := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, raw := range patterns {
		p, err := ValidateRulePattern(raw)
		if err != nil {
			return err
		}
		if !seen[p] {
			seen[p] = true
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	root, doc, err := loadConfigRoot(path)
	if err != nil {
		return err
	}
	if enable {
		list := findOrCreateRuleList(root, kind)
		present := map[string]bool{}
		for _, node := range list.Content {
			present[strings.ToLower(strings.TrimSpace(node.Value))] = true
		}
		for _, p := range clean {
			if present[p] {
				continue
			}
			list.Content = append(list.Content, &yaml.Node{
				Kind: yaml.ScalarNode, Tag: "!!str", Value: p, Style: quoteStyleFor(p),
			})
		}
		return writeConfigRoot(path, doc)
	}

	rules := mappingValue(root, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode {
		return nil // 没有 rules 段 = 这一组本来就没开,关掉它不是失败
	}
	drop := map[string]bool{}
	for _, p := range clean {
		drop[p] = true
	}
	for _, entry := range rules.Content {
		list := mappingValue(entry, kind)
		if list == nil || list.Kind != yaml.SequenceNode {
			continue
		}
		kept := list.Content[:0]
		for _, node := range list.Content {
			if drop[strings.ToLower(strings.TrimSpace(node.Value))] {
				continue
			}
			kept = append(kept, node)
		}
		list.Content = kept
	}
	return writeConfigRoot(path, doc)
}
