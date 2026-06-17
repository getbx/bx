package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/setup"
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
			{Name: "setup", Usage: "首次配置:写配置+装服务+连通检测(不启动)", ArgsUsage: "blink://...", Flags: setupFlags(), Action: setupAction},
			{Name: "up", Usage: "启动并设为开机自启", Action: upAction},
			{Name: "down", Usage: "停止并取消开机自启", Action: downAction},
			{Name: "run", Usage: "前台运行(调试/服务内部用)", Flags: runFlags(), Action: runAction},
			{Name: "status", Usage: "查看状态面板", Action: statusAction},
			{Name: "blink", Usage: "由 brook 链接生成 blink://(发给用户)", ArgsUsage: "brook://...", Action: blinkAction},
			{Name: "darwin-plan", Usage: "打印 macOS 路由 dry-run 计划(不改网络)", Flags: darwinPlanFlags(), Action: darwinPlanAction},
			{Name: "uninstall", Usage: "卸载 systemd 服务", Action: uninstallAction},
		},
	}
}

func setupFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "配置写入路径"},
		&cli.StringFlag{Name: "probe", Value: "1.1.1.1:443", Usage: "连通检测目标"},
		&cli.BoolFlag{Name: "force", Usage: "覆盖已存在的配置"},
		&cli.BoolFlag{Name: "strict", Usage: "连通检测失败则中止(默认仅警告)"},
	}
}

func setupAction(c *cli.Context) error {
	arg := c.Args().First()
	if arg == "" {
		return fmt.Errorf("用法: sudo bx setup blink://...")
	}
	link, err := blink.Decode(arg)
	if err != nil {
		return err
	}
	cfgPath := c.String("config")
	brookPath, err := provision.EnsureBrook("/var/lib/bx", "", embedded.Brook(), embedded.BrookVersion())
	if err != nil {
		return fmt.Errorf("准备 brook: %w", err)
	}
	fmt.Println("⏳ 连通检测中…")
	if lat, perr := setup.ProbeServer(brookPath, link, c.String("probe"), 15*time.Second); perr != nil {
		if c.Bool("strict") {
			return fmt.Errorf("连通检测失败: %w", perr)
		}
		fmt.Printf("⚠️  连通检测未通过(仍写配置,稍后可排查): %v\n", perr)
	} else {
		fmt.Printf("✅ 服务器连通,延迟 %dms\n", lat)
	}
	if err := setup.WriteConfig(cfgPath, link, c.Bool("force")); err != nil {
		return err
	}
	bin, err := install.SelfInstall()
	if err != nil {
		return fmt.Errorf("安装 bx 到 PATH: %w", err)
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	if err := install.WriteUnit(buildExecStart(bin, abs)); err != nil {
		return err
	}
	fmt.Printf("✅ bx 已装到 %s、写好配置 %s、装好服务。下一步:sudo bx up\n", install.BinPath, cfgPath)
	return nil
}

func runFlags() []cli.Flag {
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

func runAction(c *cli.Context) error {
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

func darwinPlanFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "tun", Value: "utunX", Usage: "计划中的 utun 设备名"},
		&cli.StringFlag{Name: "tun-addr", Value: "198.51.100.1/30", Usage: "计划中的 TUN 接口地址"},
		&cli.StringFlag{Name: "gateway", Usage: "当前物理默认网关,例如 192.168.1.1"},
		&cli.StringSliceFlag{Name: "server-bypass", Usage: "brook 服务器旁路 CIDR,可重复"},
		&cli.StringSliceFlag{Name: "bypass", Usage: "用户旁路 CIDR,可重复"},
		&cli.BoolFlag{Name: "block-v6", Usage: "包含 macOS IPv6 reject 路由计划"},
	}
}

func darwinPlanAction(c *cli.Context) error {
	if c.String("gateway") == "" {
		return fmt.Errorf("必须显式传 --gateway,例如: bx darwin-plan --gateway 192.168.1.1 --server-bypass 1.2.3.4/32")
	}
	apply, cleanup := supervisor.DarwinRoutePlan(supervisor.DarwinRoutePlanOptions{
		TunName:      c.String("tun"),
		TunAddr:      c.String("tun-addr"),
		Gateway:      c.String("gateway"),
		ServerBypass: c.StringSlice("server-bypass"),
		UserBypass:   c.StringSlice("bypass"),
		BlockV6:      c.Bool("block-v6"),
	})
	fmt.Println("# dry-run only: no commands executed")
	fmt.Println("# apply")
	for _, line := range apply {
		fmt.Println(line)
	}
	fmt.Println("# cleanup")
	for _, line := range cleanup {
		fmt.Println(line)
	}
	return nil
}

func upAction(c *cli.Context) error {
	if !install.UnitInstalled() {
		return fmt.Errorf("尚未配置。先运行: sudo bx setup blink://...")
	}
	// 防呆:命令模型重排后 up=enable service、run=前台。旧 unit 的 ExecStart 仍写
	// `bx up`,配新二进制会让 service 启动时递归调用 up → 死锁。检测到就报错让用户重装。
	cmd, err := install.ExecStartCmd()
	if err != nil {
		return err
	}
	if cmd != "run" {
		return fmt.Errorf("检测到旧版 systemd unit(ExecStart 子命令是 %q,应为 run):直接 up 会让服务递归调用自身。请重跑 sudo bx setup blink://... 重写 unit", cmd)
	}
	if err := install.Enable(); err != nil {
		return err
	}
	fmt.Println("✅ bx 已启动并设为开机自启。`bx status` 看面板。")
	return nil
}

func downAction(c *cli.Context) error {
	if err := install.Disable(); err != nil {
		return err
	}
	fmt.Println("✅ bx 已停止并取消开机自启。")
	return nil
}

func blinkAction(c *cli.Context) error {
	arg := c.Args().First()
	if !strings.HasPrefix(arg, "brook://") {
		return fmt.Errorf("用法: bx blink brook://...")
	}
	fmt.Println(blink.Encode(arg))
	return nil
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

// buildExecStart 构造自洽的 systemd ExecStart:只需绝对 bx 与绝对 config,其余走二进制内默认。
func buildExecStart(bin, configPath string) string {
	return fmt.Sprintf("%s run -c %s", bin, configPath)
}

func uninstallAction(c *cli.Context) error {
	if err := install.Uninstall(); err != nil {
		return err
	}
	fmt.Println("已卸载 bx systemd 服务")
	return nil
}
