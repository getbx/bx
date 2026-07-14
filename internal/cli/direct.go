package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

// openCloudDenylist 是「任何人都能注册子域」的公有云存储/CDN/托管平台顶级域。
// 把它们加进直连白名单会重新打开去匿名化洞:攻击者开一个桶/子域(如
// evil.oss-cn-xx.aliyuncs.com)即命中白名单 → 直连 → 泄漏真实 IP。
// 只应白名单「品牌自控 DNS 区」的顶级域(taobao.com 等,攻击者拿不到其子域)。
// directRuleRisk 返回把 domain 加入直连白名单的风险提示(空=品牌自控域,无需提示)。
func directRuleRisk(domain string) string {
	if policy.DirectRisk(domain) {
		return "⚠ 公有云存储/CDN/开放子域平台——任何人都能注册它的子域;加进直连白名单 = " +
			"攻击者能用一个子域让你的真实 IP 暴露(去匿名化)。建议只白名单品牌自控顶级域。"
	}
	return ""
}

// editYAMLRuleList 在 config 的 rules[].<field>(direct/proxy)上增删域名,返回改后字节与
// 是否真的发生了变化。走 yaml.Node round-trip 保留其它段注释。语义与 ruleField 读取对齐:
// remove 扫「所有」rule 元素(域名可能在 rules[1]+,别漏删导致以为删了其实还在直连/泄漏);
// add 跨所有元素去重后追加到 rules[0]。无有效变化时 changed=false(调用方据此避免误报成功)。
func editYAMLRuleList(in []byte, field string, add, remove []string) (out []byte, changed bool) {
	out, changed, err := policy.Edit(in, policy.Request{Mode: field, Add: add, Remove: remove, AllowRisk: true})
	if err != nil {
		return in, false
	}
	return out, changed
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
