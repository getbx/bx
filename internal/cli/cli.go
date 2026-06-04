package cli

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// New 返回配置好子命令的 bx App。
func New() *cli.App {
	return &cli.App{
		Name:  "bx",
		Usage: "基于 brook 的透明全局代理",
		Commands: []*cli.Command{
			{Name: "up", Usage: "启动全局代理", Action: notImpl("up")},
			{Name: "down", Usage: "停止", Action: notImpl("down")},
			{Name: "status", Usage: "查看状态", Action: notImpl("status")},
			{Name: "reload", Usage: "热重载规则", Action: notImpl("reload")},
			{Name: "install", Usage: "安装 systemd 自启服务", Action: notImpl("install")},
		},
	}
}

func notImpl(name string) cli.ActionFunc {
	return func(*cli.Context) error { return fmt.Errorf("%s: 尚未实现", name) }
}
