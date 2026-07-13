package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

type appPreset struct {
	Name    string
	Summary string
	Direct  []string
}

var appPresets = map[string]appPreset{
	"gaming": {
		Name:    "gaming",
		Summary: "游戏更新/CDN 可用性;当前聚焦 Steam 更新与下载 CDN",
		Direct: []string{
			"client-update.akamai.steamstatic.com",
			"steamcdn-a.akamaihd.net",
			"media.steampowered.com",
			"*.steamcontent.com",
			"*.steamstatic.com",
		},
	},
	"apple": {
		Name:    "apple",
		Summary: "Apple 系统服务、Game Center、Arcade、iCloud 同步可用性",
		Direct: []string{
			"*.apple.com",
			"*.icloud.com",
			"*.icloud-content.com",
			"*.mzstatic.com",
			"*.aaplimg.com",
			"*.cdn-apple.com",
		},
	},
	"china-cdn": {
		Name:    "china-cdn",
		Summary: "国内 App/视频/电商 CDN 可用性;只含品牌自控域",
		Direct: []string{
			"*.alicdn.com",
			"*.taobao.com",
			"*.tmall.com",
			"*.jd.com",
			"*.bilibili.com",
			"*.bilivideo.com",
			"*.douyin.com",
			"*.douyinpic.com",
			"*.byteimg.com",
			"*.weixin.qq.com",
			"*.qq.com",
		},
	},
}

func presetCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "ls", Usage: "列出内置应用可用性 preset", Action: presetListAction},
		{Name: "show", Usage: "查看一个 preset", ArgsUsage: "<name>", Action: presetShowAction},
		{Name: "apply", Usage: "应用一个 preset 到 direct 规则并热加载", ArgsUsage: "<name>", Flags: presetApplyFlags(), Action: presetApplyAction},
	}
}

func presetApplyFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
	}
}

func presetNames() []string {
	names := make([]string, 0, len(appPresets))
	for name := range appPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func getPreset(name string) (appPreset, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	p, ok := appPresets[name]
	if !ok {
		return appPreset{}, fmt.Errorf("未知 preset %q;可用: %s", name, strings.Join(presetNames(), ", "))
	}
	return p, nil
}

func presetListAction(c *cli.Context) error {
	for _, name := range presetNames() {
		p := appPresets[name]
		fmt.Printf("%-10s %s\n", p.Name, p.Summary)
	}
	return nil
}

func presetShowAction(c *cli.Context) error {
	p, err := getPreset(c.Args().First())
	if err != nil {
		return err
	}
	fmt.Printf("bx preset: %s\n", p.Name)
	fmt.Printf("  %s\n", p.Summary)
	fmt.Println("  Direct:")
	for _, domain := range p.Direct {
		fmt.Printf("    %s\n", domain)
	}
	return nil
}

func presetApplyAction(c *cli.Context) error {
	p, err := getPreset(c.Args().First())
	if err != nil {
		return err
	}
	path := resolveConfigPath(c.String("config"))
	changed, err := applyPresetToConfig(path, p)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Printf("• preset %s 已经生效,无改动。\n", p.Name)
		return nil
	}
	fmt.Println(presetApplySuccessMessage(p.Name))
	if _, err := supervisor.FetchStatusReport(supervisor.SockPath); err != nil {
		fmt.Println("  (bx 未在运行,下次 sudo bx up 时生效)")
	} else if _, err := supervisor.ReloadControl(supervisor.SockPath); err != nil {
		fmt.Printf("  ⚠ 配置已写入,但热生效失败——请重启 bx(sudo bx down && sudo bx up):%v\n", err)
	} else {
		fmt.Println("  已热生效(未断隧道)。")
	}
	return nil
}

func presetApplySuccessMessage(name string) string {
	return fmt.Sprintf("✅ preset %s 已应用。", name)
}

func applyPresetToConfig(path string, p appPreset) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读配置 %s: %w", path, err)
	}
	if _, err := config.Parse(b); err != nil {
		return false, err
	}
	out, changed := editYAMLRuleList(b, "direct", p.Direct, nil)
	if !changed {
		return false, nil
	}
	if _, err := config.Parse(out); err != nil {
		return false, fmt.Errorf("改动后配置无法解析(已中止,未写入): %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return false, fmt.Errorf("写配置 %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return true, nil
}
