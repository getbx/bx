package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

const defaultConfigPath = "/etc/bx/config.yaml"

// New 返回配置好子命令的 bx App。
func New() *cli.App {
	return &cli.App{
		Name:  "bx",
		Usage: "基于 brook 的透明全局代理",
		Commands: []*cli.Command{
			{Name: "up", Usage: "启动全局代理", Flags: upFlags(), Action: upAction},
			{Name: "down", Usage: "停止运行中的 bx", Action: downAction},
			{Name: "status", Usage: "查看状态面板", Action: statusAction},
			{Name: "reload", Usage: "热重载规则", Action: notImpl("reload")},
			{Name: "install", Usage: "安装 systemd 自启服务", Flags: upFlags(), Action: installAction},
			{Name: "uninstall", Usage: "卸载 systemd 服务", Action: uninstallAction},
		},
	}
}

func upFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "配置文件路径(默认 /etc/bx/config.yaml,非 root 回退 ~/.config/bx/config.yaml)"},
		&cli.StringFlag{Name: "tun", Value: "bx0", Usage: "TUN 设备名"},
		&cli.StringFlag{Name: "tun-addr", Value: "198.51.100.1/30", Usage: "TUN 接口地址(TEST-NET-2,避开 docker 默认地址池 172.16/12 防撞段)"},
		&cli.UintFlag{Name: "mtu", Value: 1500},
		&cli.StringFlag{Name: "brook", Value: "", Usage: "brook 二进制路径(留空=用内嵌 brook)"},
		&cli.StringFlag{Name: "china-domain", Value: "", Usage: "china 域名列表(留空=用内嵌/自动刷新快照)"},
		&cli.StringFlag{Name: "china-cidr", Value: "", Usage: "china IP 段(留空=用内嵌/自动刷新快照)"},
		&cli.StringFlag{Name: "probe", Value: "1.1.1.1:443", Usage: "隧道健康检查目标"},
		&cli.DurationFlag{Name: "test-timeout", Usage: "死手定时器:到点自动还原(远程实测保命)"},
		&cli.BoolFlag{Name: "global", Aliases: []string{"g"}, Usage: "全局模式:除内网(bypass)/用户 direct 规则外,一切(含中国)走代理"},
	}
}

func upAction(c *cli.Context) error {
	cfg, err := loadConfig(c.String("config"))
	if err != nil {
		return err
	}
	return supervisor.Run(c.Context, cfg, optsFromFlags(c))
}

func optsFromFlags(c *cli.Context) supervisor.Options {
	return supervisor.Options{
		TunName:         c.String("tun"),
		TunAddr:         c.String("tun-addr"),
		MTU:             uint32(c.Uint("mtu")),
		BrookBin:        c.String("brook"),
		ChinaDomainPath: c.String("china-domain"),
		ChinaCIDRPath:   c.String("china-cidr"),
		Probe:           c.String("probe"),
		Deadman:         c.Duration("test-timeout"),
		Global:          c.Bool("global"),
	}
}

func loadConfig(path string) (*config.Config, error) {
	path = resolveConfigPath(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读配置 %s: %w", path, err)
	}
	return config.Parse(b)
}

// resolveConfigPath: 默认路径不存在时回退到家目录配置(便于非 root 只读命令)。
func resolveConfigPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	// 仅默认路径才回退到家目录;用户显式 -c 的路径原样返回,让错误带上用户路径
	if path == defaultConfigPath {
		home, _ := os.UserHomeDir()
		alt := filepath.Join(home, ".config/bx/config.yaml")
		if _, err := os.Stat(alt); err == nil {
			return alt
		}
	}
	return path
}

func statusAction(c *cli.Context) error {
	conn, err := net.Dial("unix", supervisor.SockPath)
	if err != nil {
		return fmt.Errorf("连接 bx 失败(bx 是否在运行?): %w", err)
	}
	defer conn.Close()
	var rep stats.Report
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		return fmt.Errorf("读状态: %w", err)
	}
	fmt.Print(stats.Render(rep))
	return nil
}

func downAction(c *cli.Context) error {
	b, err := os.ReadFile(supervisor.PidPath)
	if err != nil {
		return fmt.Errorf("找不到运行中的 bx(%s): %w", supervisor.PidPath, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return fmt.Errorf("pid 文件损坏: %w", err)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("停止 bx(pid %d): %w", pid, err)
	}
	fmt.Printf("已向 bx (pid %d) 发送停止信号\n", pid)
	return nil
}

func installAction(c *cli.Context) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	cfgPath, err := filepath.Abs(resolveConfigPath(c.String("config")))
	if err != nil {
		return err
	}
	if err := install.Install(buildExecStart(bin, cfgPath)); err != nil {
		return err
	}
	fmt.Println("✅ bx 已安装为 systemd 服务并启动(开机自启)。`systemctl status bx` 查看,`bx status` 看面板。")
	return nil
}

// buildExecStart 构造自洽的 systemd ExecStart:只需绝对 bx 与绝对 config,其余走二进制内默认。
func buildExecStart(bin, configPath string) string {
	return fmt.Sprintf("%s up -c %s", bin, configPath)
}

func uninstallAction(c *cli.Context) error {
	if err := install.Uninstall(); err != nil {
		return err
	}
	fmt.Println("已卸载 bx systemd 服务")
	return nil
}

func notImpl(name string) cli.ActionFunc {
	return func(*cli.Context) error { return fmt.Errorf("%s: 尚未实现", name) }
}
