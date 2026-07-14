package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// openCloudDenylist 是「任何人都能注册子域」的公有云存储/CDN/托管平台顶级域。
// 把它们加进直连白名单会重新打开去匿名化洞:攻击者开一个桶/子域(如
// evil.oss-cn-xx.aliyuncs.com)即命中白名单 → 直连 → 泄漏真实 IP。
// 只应白名单「品牌自控 DNS 区」的顶级域(taobao.com 等,攻击者拿不到其子域)。
var openCloudDenylist = []string{
	// 国内公有云对象存储/CDN
	"aliyuncs.com", "myqcloud.com", "bcebos.com",
	"qiniucdn.com", "qbox.me", "clouddn.com", "upaiyun.com",
	"myhuaweicloud.com",
	// 全球公有云/开放子域托管
	"amazonaws.com", "cloudfront.net", "core.windows.net", "googleapis.com",
	"r2.dev", "workers.dev", "pages.dev", "github.io",
	"vercel.app", "netlify.app", "b-cdn.net",
}

var openCloudSet = route.NewDomainSet(openCloudDenylist)

// directRuleRisk 返回把 domain 加入直连白名单的风险提示(空=品牌自控域,无需提示)。
func directRuleRisk(domain string) string {
	if openCloudSet.Match(strings.ToLower(strings.TrimSpace(domain))) {
		return "⚠ 公有云存储/CDN/开放子域平台——任何人都能注册它的子域;加进直连白名单 = " +
			"攻击者能用一个子域让你的真实 IP 暴露(去匿名化)。建议只白名单品牌自控顶级域。"
	}
	return ""
}

func normDomain(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// editYAMLRuleList 在 config 的 rules[].<field>(direct/proxy)上增删域名,返回改后字节与
// 是否真的发生了变化。走 yaml.Node round-trip 保留其它段注释。语义与 ruleField 读取对齐:
// remove 扫「所有」rule 元素(域名可能在 rules[1]+,别漏删导致以为删了其实还在直连/泄漏);
// add 跨所有元素去重后追加到 rules[0]。无有效变化时 changed=false(调用方据此避免误报成功)。
func editYAMLRuleList(in []byte, field string, add, remove []string) (out []byte, changed bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return in, false
	}
	root := doc.Content[0]
	rules := mappingValue(root, "rules")
	oppositeField := "proxy"
	if field == "proxy" {
		oppositeField = "direct"
	}

	fieldSeq := func(elem *yaml.Node, name string) *yaml.Node {
		if elem == nil || elem.Kind != yaml.MappingNode {
			return nil
		}
		return mappingValue(elem, name)
	}

	removeFromSeq := func(seq *yaml.Node, rm map[string]bool) bool {
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return false
		}
		localChanged := false
		kept := seq.Content[:0]
		for _, n := range seq.Content {
			if rm[normDomain(n.Value)] {
				localChanged = true
				continue
			}
			kept = append(kept, n)
		}
		seq.Content = kept
		return localChanged
	}
	removeFromField := func(elem *yaml.Node, name string, rm map[string]bool) bool {
		seq := fieldSeq(elem, name)
		if !removeFromSeq(seq, rm) {
			return false
		}
		if seq.Kind == yaml.SequenceNode && len(seq.Content) == 0 {
			for i := 0; i+1 < len(elem.Content); i += 2 {
				if elem.Content[i].Value == name {
					elem.Content = append(elem.Content[:i], elem.Content[i+2:]...)
					break
				}
			}
		}
		return true
	}

	// REMOVE:扫所有 rule 元素的 field 序列
	if len(remove) > 0 && rules != nil && rules.Kind == yaml.SequenceNode {
		rm := map[string]bool{}
		for _, d := range remove {
			rm[normDomain(d)] = true
		}
		for _, elem := range rules.Content {
			changed = removeFromField(elem, field, rm) || changed
		}
	}

	// ADD:跨所有元素去重,追加到 rules[0]
	if len(add) > 0 {
		existing := map[string]bool{}
		if rules != nil && rules.Kind == yaml.SequenceNode {
			for _, elem := range rules.Content {
				if seq := fieldSeq(elem, field); seq != nil {
					for _, n := range seq.Content {
						existing[normDomain(n.Value)] = true
					}
				}
			}
		}
		var toAdd []string
		conflicts := map[string]bool{}
		for _, d := range add {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			conflicts[normDomain(d)] = true
			if existing[normDomain(d)] {
				continue
			}
			existing[normDomain(d)] = true
			toAdd = append(toAdd, d)
		}
		// direct/proxy 是互斥意图。加入一侧前,先从另一侧跨所有 rule 块移除同名项,
		// 避免写出「显示已强制代理但 router 仍因 direct 优先而直连」的配置。
		// 即使目标字段已存在,也要修复 opposite 里的旧冲突项。
		if len(conflicts) > 0 && rules != nil && rules.Kind == yaml.SequenceNode {
			for _, elem := range rules.Content {
				changed = removeFromField(elem, oppositeField, conflicts) || changed
			}
		}
		if len(toAdd) > 0 {
			if rules == nil {
				rules = &yaml.Node{Kind: yaml.SequenceNode}
				root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "rules"}, rules)
			}
			if rules.Kind != yaml.SequenceNode {
				rules.Kind, rules.Tag, rules.Value, rules.Content = yaml.SequenceNode, "!!seq", "", nil
			}
			if len(rules.Content) == 0 {
				rules.Content = append(rules.Content, &yaml.Node{Kind: yaml.MappingNode})
			}
			elem := rules.Content[0]
			if elem.Kind != yaml.MappingNode {
				elem.Kind, elem.Tag, elem.Value, elem.Content = yaml.MappingNode, "!!map", "", nil
			}
			seq := mappingValue(elem, field)
			if seq == nil {
				seq = &yaml.Node{Kind: yaml.SequenceNode}
				elem.Content = append(elem.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: field}, seq)
			}
			if seq.Kind != yaml.SequenceNode {
				seq.Kind, seq.Tag, seq.Value, seq.Content = yaml.SequenceNode, "!!seq", "", nil
			}
			for _, d := range toAdd {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: d})
			}
			changed = true
		}
	}

	if !changed {
		return in, false
	}
	if rules != nil && rules.Kind == yaml.SequenceNode {
		kept := rules.Content[:0]
		for _, elem := range rules.Content {
			if elem != nil && elem.Kind == yaml.MappingNode && len(elem.Content) == 0 {
				continue
			}
			kept = append(kept, elem)
		}
		rules.Content = kept
	}
	m, err := yaml.Marshal(&doc)
	if err != nil {
		return in, false
	}
	return m, true
}

// ruleBaseFlags 只有 config;ls/rm 和 proxy 各子命令无风险门,故不挂 --force(避免死 UX)。
func ruleBaseFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
	}
}

// directAddFlags 仅 `bx direct add` 用:多一个 --force(唯一有风险门控的命令)。
func directAddFlags() []cli.Flag {
	return append(ruleBaseFlags(), &cli.BoolFlag{Name: "force", Usage: "即便命中公有云/CDN 风险名单也强制加入"})
}

func directCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "ls", Usage: "列出当前直连白名单", Flags: ruleBaseFlags(), Action: func(c *cli.Context) error { return listRuleAction(c, "direct") }},
		{Name: "add", Usage: "加域名进直连白名单(命中公有云会提示风险)", Flags: directAddFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "direct", true) }},
		{Name: "rm", Usage: "从直连白名单移除域名", Flags: ruleBaseFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "direct", false) }},
	}
}

func proxyCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "ls", Usage: "列出强制走隧道的域名", Flags: ruleBaseFlags(), Action: func(c *cli.Context) error { return listRuleAction(c, "proxy") }},
		{Name: "add", Usage: "加域名进强制隧道列表", Flags: ruleBaseFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "proxy", true) }},
		{Name: "rm", Usage: "从强制隧道列表移除域名", Flags: ruleBaseFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "proxy", false) }},
	}
}

// ruleField 从解析好的配置里收集 direct 或 proxy 域名(bx 会把所有 rules 扁平成一个集合)。
func ruleField(cfg *config.Config, field string) []string {
	var out []string
	for _, r := range cfg.Rules {
		if field == "direct" {
			out = append(out, r.Direct...)
		} else {
			out = append(out, r.Proxy...)
		}
	}
	return out
}

func listRuleAction(c *cli.Context, field string) error {
	cfg, err := loadConfig(c.String("config"))
	if err != nil {
		return err
	}
	doms := ruleField(cfg, field)
	label := "直连白名单"
	if field == "proxy" {
		label = "强制隧道列表"
	}
	if len(doms) == 0 {
		fmt.Printf("%s为空。\n", label)
		return nil
	}
	fmt.Printf("%s(%d):\n", label, len(doms))
	for _, d := range doms {
		fmt.Printf("  %s\n", d)
	}
	return nil
}

func editRuleAction(c *cli.Context, field string, isAdd bool) error {
	domains := c.Args().Slice()
	if len(domains) == 0 {
		return fmt.Errorf("需指定至少一个域名,例如: bx %s %s taobao.com", field, map[bool]string{true: "add", false: "rm"}[isAdd])
	}
	path := resolveConfigPath(c.String("config"))
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读配置 %s: %w", path, err)
	}
	if _, err := config.Parse(b); err != nil {
		return err
	}

	var apply []string
	var changed bool
	if isAdd {
		for _, d := range domains {
			if field == "direct" {
				if w := directRuleRisk(d); w != "" {
					fmt.Printf("%s  (%s)\n", w, d)
					if !c.Bool("force") {
						fmt.Printf("  → 已跳过 %s;确要加入请加 --force。\n", d)
						continue
					}
				}
			}
			apply = append(apply, d)
		}
		if len(apply) == 0 {
			return fmt.Errorf("没有域名被加入(全部命中风险名单且未 --force)")
		}
		b, changed = editYAMLRuleList(b, field, apply, nil)
	} else {
		apply = domains
		b, changed = editYAMLRuleList(b, field, nil, apply)
	}

	// 没有实际变化(add 时已在名单 / rm 时本就不在):不误报成功、不写盘、不热生效。
	if !changed {
		state := "已在"
		if !isAdd {
			state = "不在"
		}
		fmt.Printf("• 无改动:%s %s%s。\n", strings.Join(apply, " "), state, ruleLabel(field))
		return nil
	}

	if _, err := config.Parse(b); err != nil {
		return fmt.Errorf("改动后配置无法解析(已中止,未写入): %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("写配置 %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)

	verb := "已加入"
	if !isAdd {
		verb = "已移除"
	}
	fmt.Printf("✅ %s %s: %s\n", verb, field, strings.Join(apply, " "))

	// 热生效:先探 bx 是否在跑(GET /v0/status),再触发 reload(POST)。区分三态,别把
	// 「在跑但 reload 失败」误报成「没在跑,下次 up 生效」——那会让旧的泄漏规则仍在生效却谎报安全。
	if _, err := supervisor.FetchStatusReport(supervisor.SockPath); err != nil {
		fmt.Println("  (bx 未在运行,下次 sudo bx up 时生效)")
	} else if _, err := supervisor.ReloadControl(supervisor.SockPath); err != nil {
		fmt.Printf("  ⚠ 配置已写入,但热生效失败——旧规则仍在运行;请查看 bx logs,新规则将在下次启动保护时生效:%v\n", err)
	} else {
		fmt.Println("  已热生效(未断隧道)。")
	}
	return nil
}

func ruleLabel(field string) string {
	if field == "proxy" {
		return "强制隧道列表"
	}
	return "直连白名单"
}
