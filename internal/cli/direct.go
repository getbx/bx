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

// editYAMLRuleList 在 config 的 rules[0].<field>(direct 或 proxy)序列上增删域名,
// 走 yaml.Node round-trip 保留其它段的注释/结构;缺 rules/元素/字段时自动补齐。
func editYAMLRuleList(in []byte, field string, add, remove []string) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return in
	}
	root := doc.Content[0]

	rules := mappingValue(root, "rules")
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

	list := mappingValue(elem, field)
	if list == nil {
		list = &yaml.Node{Kind: yaml.SequenceNode}
		elem.Content = append(elem.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: field}, list)
	}
	if list.Kind != yaml.SequenceNode {
		list.Kind, list.Tag, list.Value, list.Content = yaml.SequenceNode, "!!seq", "", nil
	}

	if len(remove) > 0 {
		rm := map[string]bool{}
		for _, d := range remove {
			rm[strings.ToLower(strings.TrimSpace(d))] = true
		}
		kept := list.Content[:0]
		for _, n := range list.Content {
			if rm[strings.ToLower(strings.TrimSpace(n.Value))] {
				continue
			}
			kept = append(kept, n)
		}
		list.Content = kept
	}

	existing := map[string]bool{}
	for _, n := range list.Content {
		existing[strings.ToLower(strings.TrimSpace(n.Value))] = true
	}
	for _, d := range add {
		d = strings.TrimSpace(d)
		if d == "" || existing[strings.ToLower(d)] {
			continue
		}
		existing[strings.ToLower(d)] = true
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: d})
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return in
	}
	return out
}

func directFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
		&cli.BoolFlag{Name: "force", Usage: "即便命中公有云/CDN 风险名单也强制加入"},
	}
}

func directCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "ls", Usage: "列出当前直连白名单", Flags: directFlags(), Action: func(c *cli.Context) error { return listRuleAction(c, "direct") }},
		{Name: "add", Usage: "加域名进直连白名单(命中公有云会提示风险)", Flags: directFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "direct", true) }},
		{Name: "rm", Usage: "从直连白名单移除域名", Flags: directFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "direct", false) }},
	}
}

func proxyCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "ls", Usage: "列出强制走隧道的域名", Flags: directFlags(), Action: func(c *cli.Context) error { return listRuleAction(c, "proxy") }},
		{Name: "add", Usage: "加域名进强制隧道列表", Flags: directFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "proxy", true) }},
		{Name: "rm", Usage: "从强制隧道列表移除域名", Flags: directFlags(), Action: func(c *cli.Context) error { return editRuleAction(c, "proxy", false) }},
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
		b = editYAMLRuleList(b, field, apply, nil)
	} else {
		apply = domains
		b = editYAMLRuleList(b, field, nil, apply)
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

	// 热生效:通知正在运行的 bx 重建 router(不断隧道、不碰 TUN/路由)。连不上=没在跑,下次 up 生效。
	if _, err := supervisor.ReloadControl(supervisor.SockPath); err != nil {
		fmt.Println("  (bx 未在运行,下次 sudo bx up 时生效)")
	} else {
		fmt.Println("  已热生效(未断隧道)。")
	}
	return nil
}
