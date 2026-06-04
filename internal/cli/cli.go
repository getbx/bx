package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

// New 返回配置好子命令的 bx App。
func New() *cli.App {
	return &cli.App{
		Name:  "bx",
		Usage: "基于 brook 的透明全局代理",
		Commands: []*cli.Command{
			{
				Name:   "up",
				Usage:  "启动全局代理",
				Flags:  upFlags(),
				Action: upAction,
			},
			{Name: "down", Usage: "停止", Action: notImpl("down")},
			{Name: "status", Usage: "查看状态", Action: notImpl("status")},
			{Name: "reload", Usage: "热重载规则", Action: notImpl("reload")},
			{Name: "install", Usage: "安装 systemd 自启服务", Action: notImpl("install")},
		},
	}
}

func upFlags() []cli.Flag {
	home, _ := os.UserHomeDir()
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: filepath.Join(home, ".config/bx/config.yaml"), Usage: "配置文件路径"},
		&cli.StringFlag{Name: "tun", Value: "bx0", Usage: "TUN 设备名"},
		&cli.StringFlag{Name: "tun-addr", Value: "172.19.0.1/24", Usage: "TUN 接口地址"},
		&cli.UintFlag{Name: "mtu", Value: 1500},
		&cli.StringFlag{Name: "brook", Value: filepath.Join(home, ".nami/bin/brook"), Usage: "brook 二进制路径"},
		&cli.StringFlag{Name: "china-domain", Value: filepath.Join(home, ".brook/china_domain.txt")},
		&cli.StringFlag{Name: "china-cidr", Value: filepath.Join(home, ".brook/china_cidr4.txt")},
		&cli.StringFlag{Name: "probe", Value: "1.1.1.1:443", Usage: "隧道健康检查目标"},
		&cli.DurationFlag{Name: "test-timeout", Usage: "死手定时器:到点自动还原(远程实测保命)"},
	}
}

func upAction(c *cli.Context) error {
	b, err := os.ReadFile(c.String("config"))
	if err != nil {
		return fmt.Errorf("读配置 %s: %w", c.String("config"), err)
	}
	cfg, err := config.Parse(b)
	if err != nil {
		return err
	}
	opts := supervisor.Options{
		TunName:         c.String("tun"),
		TunAddr:         c.String("tun-addr"),
		MTU:             uint32(c.Uint("mtu")),
		BrookBin:        c.String("brook"),
		ChinaDomainPath: c.String("china-domain"),
		ChinaCIDRPath:   c.String("china-cidr"),
		Probe:           c.String("probe"),
		Deadman:         c.Duration("test-timeout"),
	}
	return supervisor.Run(c.Context, cfg, opts)
}

func notImpl(name string) cli.ActionFunc {
	return func(*cli.Context) error { return fmt.Errorf("%s: 尚未实现", name) }
}
