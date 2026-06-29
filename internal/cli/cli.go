package cli

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/gateway"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/mcp"
	"github.com/getbx/bx/internal/procredact"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

const defaultConfigPath = "/etc/bx/config.yaml"
const defaultServerConfigPath = "/etc/bx/server.yaml"
const defaultShareDir = "/etc/bx/shares"

// 健康探测目标:必须是隧道出口能稳定连上的东西。github.com 本身常被黑洞/限速(尤其从代理出口),
// 用它当探针会把"github 慢"误判成"隧道挂了"导致无谓重连。1.1.1.1:443 是裸 IP(免 DNS)、全球稳定。
const defaultProbeTarget = "1.1.1.1:443"
const darwinDNSListen = "127.0.0.1:53"
const defaultLogArchiveDir = ".bx-log-archives"
const defaultAutoArchiveLimit = 12

// New 返回配置好子命令的 bx App。
func New() *cli.App {
	return &cli.App{
		Name:    "bx",
		Usage:   "透明全局代理",
		Version: version.String(),
		Commands: []*cli.Command{
			{Name: "setup", Usage: "首次配置:写配置+装服务+连通检测(不启动)", ArgsUsage: "bx://...", Flags: setupFlags(), Action: setupAction},
			{Name: "probe", Usage: "检测 bx:// 链接连通性(不写配置/不改路由)", ArgsUsage: "bx://...", Flags: probeFlags(), Action: probeAction},
			{Name: "server", Usage: "管理 bx server", Subcommands: serverCommands()},
			{Name: "doctor", Usage: "诊断客户端配置和运行状态", Flags: doctorFlags(), Action: doctorAction},
			{Name: "inspect", Usage: "输出 agent 可读诊断包", Flags: inspectFlags(), Action: inspectAction},
			{Name: "capabilities", Usage: "输出机器可读能力清单", Action: capabilitiesAction},
			{Name: "up", Usage: "启动并设为开机自启", Action: upAction},
			{Name: "down", Usage: "停止并取消开机自启", Action: downAction},
			{Name: "dns", Usage: "管理 macOS 系统 DNS 接管", Subcommands: dnsCommands()},
			{Name: "realtime", Usage: "查看实时 UDP 策略", Subcommands: realtimeCommands()},
			{Name: "run", Usage: "前台运行(调试/服务内部用)", Flags: runFlags(), Action: runAction},
			{Name: "serve", Usage: "运行 bx server", Hidden: true, Flags: serveFlags(), Action: serveAction},
			{Name: "mcp", Usage: "启动 agent 控制面 MCP server(stdio)", Hidden: false, Flags: mcpFlags(), Action: mcpAction, Subcommands: []*cli.Command{
				{Name: "install", Usage: "打印把 bx 接入你的 agent 的 MCP 配对指令(只打印,不自跑)", Action: mcpInstallAction},
			}},
			{Name: "status", Usage: "查看状态面板", Flags: statusFlags(), Action: statusAction},
			{Name: "logs", Usage: "查看客户端日志", Flags: logsFlags(), Action: logsAction},
			{Name: "link", Usage: "生成 bx:// 链接", ArgsUsage: "<internal-link>", Hidden: true, Action: linkAction},
			{Name: "blink", Usage: "兼容旧链接生成命令", ArgsUsage: "<internal-link>", Hidden: true, Action: linkAction},
			{Name: "darwin-plan", Usage: "打印 macOS 路由 dry-run 计划(不改网络)", Flags: darwinPlanFlags(), Action: darwinPlanAction},
			{Name: "router-plan", Usage: "打印 router 模式 dry-run 计划(ip + nft,不改网络)", Flags: routerPlanFlags(), Action: routerPlanAction},
			{Name: "uninstall", Usage: "卸载客户端服务", Action: uninstallAction},
		},
	}
}

type serverConfig struct {
	Listen   string `yaml:"listen"`
	Password string `yaml:"password"`
}

type shareInfo struct {
	Name   string
	Config serverConfig
}

type sharesReport struct {
	OK              bool        `json:"ok"`
	SecretsRedacted bool        `json:"secrets_redacted"`
	Shares          []shareView `json:"shares"`
}

type checkReport struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type doctorReport struct {
	OK              bool          `json:"ok"`
	Kind            string        `json:"kind"`
	Version         string        `json:"version"`
	SecretsRedacted bool          `json:"secrets_redacted"`
	ChangesSystem   bool          `json:"changes_system"`
	ChangesNetwork  bool          `json:"changes_network"`
	RequiresRoot    bool          `json:"requires_root"`
	Checks          []checkReport `json:"checks"`
}

type inspectReport struct {
	OK              bool               `json:"ok"`
	Kind            string             `json:"kind"`
	Version         string             `json:"version"`
	SecretsRedacted bool               `json:"secrets_redacted"`
	ChangesSystem   bool               `json:"changes_system"`
	ChangesNetwork  bool               `json:"changes_network"`
	Capabilities    capabilitiesReport `json:"capabilities"`
	Status          *stats.Report      `json:"status,omitempty"`
	StatusError     string             `json:"status_error,omitempty"`
	Doctor          doctorReport       `json:"doctor"`
	NextActions     []string           `json:"next_actions,omitempty"`
}

type capabilitiesReport struct {
	SchemaVersion   int                 `json:"schema_version"`
	Product         string              `json:"product"`
	Version         string              `json:"version"`
	SecretsRedacted bool                `json:"secrets_redacted"`
	Commands        []commandCapability `json:"commands"`
}

type commandCapability struct {
	Command        string   `json:"command"`
	Category       string   `json:"category"`
	Summary        string   `json:"summary"`
	Stable         bool     `json:"stable"`
	RequiresRoot   bool     `json:"requires_root"`
	ChangesSystem  bool     `json:"changes_system"`
	ChangesNetwork bool     `json:"changes_network"`
	ReadsSecrets   bool     `json:"reads_secrets"`
	Outputs        []string `json:"outputs,omitempty"`
	Arguments      []string `json:"arguments,omitempty"`
	Examples       []string `json:"examples,omitempty"`
	SafeNotes      []string `json:"safe_notes,omitempty"`
}

func serverCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "install", Usage: "安装 bx server 服务", Flags: serverInstallFlags(), Action: serverInstallAction},
		{Name: "link", Usage: "生成客户端 bx:// 链接", Flags: serverLinkFlags(), Action: serverLinkAction},
		{Name: "share", Usage: "分享给一个人", ArgsUsage: "<name>", Flags: serverShareFlags(), Action: serverShareAction},
		{Name: "shares", Usage: "查看已分享的链接", Flags: serverSharesFlags(), Action: serverSharesAction},
		{Name: "revoke", Usage: "撤销一个分享", ArgsUsage: "<name>", Flags: serverRevokeFlags(), Action: serverRevokeAction},
		{Name: "rotate", Usage: "轮换 server 密码并生成新链接", Flags: serverRotateFlags(), Action: serverRotateAction},
		{Name: "start", Usage: "启动并设为开机自启", Action: serverStartAction},
		{Name: "stop", Usage: "停止并取消开机自启", Action: serverStopAction},
		{Name: "status", Usage: "查看服务状态", Action: serverStatusAction},
		{Name: "doctor", Usage: "诊断 bx server 配置和运行状态", Flags: serverDoctorFlags(), Action: serverDoctorAction},
		{Name: "logs", Usage: "查看 bx server 日志", Flags: serverLogsFlags(), Action: serverLogsAction},
		{Name: "ui", Usage: "启动本地 Web 管理界面", Flags: serverUIFlags(), Action: serverUIAction},
		{Name: "uninstall", Usage: "卸载 bx server 服务", Action: serverUninstallAction},
	}
}

func dnsCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "status", Usage: "查看 macOS 系统 DNS 接管状态", Flags: dnsFlags(), Action: dnsStatusAction},
		{Name: "on", Usage: "将当前网络服务 DNS 临时切到 bx", Flags: dnsFlags(), Action: dnsOnAction},
		{Name: "off", Usage: "恢复 bx 保存的原始 DNS", Flags: dnsFlags(), Action: dnsOffAction},
	}
}

func realtimeCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "status", Usage: "查看 UDP / 实时应用策略", Flags: realtimeFlags(), Action: realtimeStatusAction},
		{Name: "on", Usage: "开启非 DNS UDP 中继模式", Flags: realtimeFlags(), Hidden: true, Action: realtimeOnAction},
		{Name: "off", Usage: "恢复默认 UDP 阻断模式", Flags: realtimeFlags(), Hidden: true, Action: realtimeOffAction},
	}
}

func realtimeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
		&cli.BoolFlag{Name: "no-restart", Usage: "只写配置,不自动重启正在运行的 bx"},
	}
}

func serverInstallFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置写入路径"},
		&cli.StringFlag{Name: "listen", Value: ":9999", Usage: "监听地址"},
		&cli.StringFlag{Name: "password", Usage: "连接密码(留空自动生成)"},
		&cli.StringFlag{Name: "host", Usage: "生成链接使用的公网地址或域名"},
		&cli.BoolFlag{Name: "force", Usage: "覆盖已存在的 server 配置"},
	}
}

func serverLinkFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置路径"},
		&cli.StringFlag{Name: "host", Usage: "公网地址或域名"},
	}
}

func serverRotateFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置路径"},
		&cli.StringFlag{Name: "host", Usage: "生成新链接使用的公网地址或域名"},
		&cli.StringFlag{Name: "password", Usage: "新连接密码(留空自动生成)"},
		&cli.BoolFlag{Name: "no-restart", Usage: "只写配置,不重启正在运行的 server"},
	}
}

func serverShareFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "share 配置目录"},
		&cli.StringFlag{Name: "host", Usage: "生成链接使用的公网地址或域名"},
		&cli.StringFlag{Name: "listen", Usage: "监听地址(留空自动分配端口)"},
		&cli.StringFlag{Name: "password", Usage: "连接密码(留空自动生成)"},
		&cli.BoolFlag{Name: "open-ufw", Usage: "创建后自动执行 ufw allow <port>/tcp"},
	}
}

func serverSharesFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "share 配置目录"},
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
	}
}

func serverRevokeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "share 配置目录"},
	}
}

func serveFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置路径"},
	}
}

func mcpFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
	}
}

func mcpAction(c *cli.Context) error {
	ops := mcp.NewLiveOps(c.String("config"))
	return mcp.Serve(c.Context, ops)
}

// mcpInstallText 构建把 bx 接入 agent 的 MCP 配对指令(纯函数,无副作用)。
// bxPath 由调用方用 os.Executable() 解析为绝对路径(robust)。
func mcpInstallText(bxPath string) string {
	return fmt.Sprintf(`把 bx 接入你的 agent(MCP 控制面)——下面指令只需运行一次:

Claude Code:
  claude mcp add --scope user bx -- %s mcp

其他 MCP 客户端(如 Claude Desktop)在 mcpServers 加:
  {"mcpServers": {"bx": {"command": "%s", "args": ["mcp"]}}}

若你是 AI agent:运行上面 Claude Code 命令即可获得 bx 控制面(状态/换传输/重劫持,
commit-confirmed 死手兜底),以业主身份授权、无需 sudo(业主 = 运行 sudo bx setup 的用户)。
`, bxPath, bxPath)
}

// mcpInstallAction 打印配对指令(只打印,不自跑)。
func mcpInstallAction(c *cli.Context) error {
	bxPath, err := os.Executable()
	if err != nil || bxPath == "" {
		bxPath = "bx" // 退化:取不到自身路径时用裸名(假定在 PATH)
	}
	fmt.Fprint(os.Stdout, mcpInstallText(bxPath))
	return nil
}

func doctorFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
		&cli.DurationFlag{Name: "timeout", Value: 8 * time.Second, Usage: "链接探测超时"},
		&cli.StringFlag{Name: "target", Value: defaultProbeTarget, Usage: "链接探测目标"},
		&cli.BoolFlag{Name: "skip-probe", Usage: "跳过 bx:// 链接探测"},
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
	}
}

func inspectFlags() []cli.Flag {
	return doctorFlags()
}

func serverDoctorFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置路径"},
		&cli.StringFlag{Name: "shares-dir", Value: defaultShareDir, Usage: "share 配置目录"},
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
	}
}

func serverLogsFlags() []cli.Flag {
	return logsFlags()
}

func logsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{Name: "lines", Aliases: []string{"n"}, Value: 100, Usage: "显示最近 N 行日志"},
		&cli.BoolFlag{Name: "follow", Aliases: []string{"f"}, Usage: "持续跟随日志"},
		&cli.BoolFlag{Name: "archive", Usage: "保存原始日志和诊断快照到本地目录"},
		&cli.StringFlag{Name: "dir", Value: ".bx-log-archives", Usage: "日志归档目录"},
	}
}

func statusFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
	}
}

func serverUIFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "listen", Value: "127.0.0.1:8787", Usage: "Web UI 监听地址"},
		&cli.StringFlag{Name: "host", Usage: "生成链接使用的公网地址或域名"},
		&cli.StringFlag{Name: "shares-dir", Value: defaultShareDir, Usage: "share 配置目录"},
	}
}

func dnsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "service", Usage: "macOS 网络服务名(默认自动检测当前默认出口)"},
	}
}

func serverInstallAction(c *cli.Context) error {
	password := c.String("password")
	if password == "" {
		var err error
		password, err = randomPassword()
		if err != nil {
			return err
		}
	}
	cfg := serverConfig{Listen: c.String("listen"), Password: password}
	if err := writeServerConfig(c.String("config"), cfg, c.Bool("force")); err != nil {
		return err
	}
	bin, err := install.SelfInstall()
	if err != nil {
		return fmt.Errorf("安装 bx 到 PATH: %w", err)
	}
	abs, err := filepath.Abs(c.String("config"))
	if err != nil {
		return err
	}
	if err := install.WriteServerUnit(fmt.Sprintf("%s serve -c %s", bin, abs)); err != nil {
		return err
	}
	fmt.Printf("✅ bx server 已安装。下一步:sudo bx server start\n")
	if hint := serverFirewallHint(cfg.Listen); hint != "" {
		fmt.Println(hint)
	}
	if host := c.String("host"); host != "" {
		link, err := bxServerLink(host, cfg)
		if err != nil {
			return err
		}
		fmt.Println(link)
	} else {
		fmt.Println("需要客户端链接时运行: sudo bx server link --host <VPS_IP或域名>")
	}
	return nil
}

func serverLinkAction(c *cli.Context) error {
	host := c.String("host")
	if host == "" {
		return fmt.Errorf("用法: sudo bx server link --host <VPS_IP或域名>")
	}
	cfg, err := readServerConfig(c.String("config"))
	if err != nil {
		return err
	}
	link, err := bxServerLink(host, cfg)
	if err != nil {
		return err
	}
	fmt.Println(link)
	return nil
}

func serverShareAction(c *cli.Context) error {
	name, err := cleanShareName(c.Args().First())
	if err != nil {
		return err
	}
	dir := stringFlag(c, "dir")
	password := stringFlag(c, "password")
	if password == "" {
		password, err = randomPassword()
		if err != nil {
			return err
		}
	}
	listen := stringFlag(c, "listen")
	if listen == "" {
		listen, err = nextShareListen(dir)
		if err != nil {
			return err
		}
	}
	host := stringFlag(c, "host")
	link, listen, err := createShare(name, host, dir, listen, password)
	if err != nil {
		return err
	}
	fmt.Printf("✅ share %s 已创建。\n", name)
	if c.Bool("open-ufw") {
		if err := openUFW(listen); err != nil {
			return err
		}
	}
	if hint := serverFirewallHint(listen); hint != "" {
		fmt.Println(hint)
	}
	if host != "" {
		fmt.Println(link)
	} else {
		fmt.Println("需要链接时运行: sudo bx server share " + name + " --host <VPS_IP或域名>")
	}
	return nil
}

func serverSharesAction(c *cli.Context) error {
	shares, err := readShares(c.String("dir"))
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return writeJSON(os.Stdout, sharesReport{OK: true, SecretsRedacted: true, Shares: shareViews(shares)})
	}
	if len(shares) == 0 {
		fmt.Println("No shares.")
		return nil
	}
	fmt.Println("NAME\tLISTEN\tSTATUS")
	for _, s := range shares {
		fmt.Printf("%s\t%s\t%s\n", s.Name, s.Config.Listen, serviceState("is-active", install.ShareServiceName(s.Name)))
	}
	return nil
}

func serverRevokeAction(c *cli.Context) error {
	name, err := cleanShareName(c.Args().First())
	if err != nil {
		return err
	}
	if err := revokeShare(name, c.String("dir")); err != nil {
		return err
	}
	fmt.Printf("✅ share %s 已撤销。\n", name)
	return nil
}

func createShare(name, host, dir, listen, password string) (link string, effectiveListen string, err error) {
	if password == "" {
		password, err = randomPassword()
		if err != nil {
			return "", "", err
		}
	}
	if listen == "" {
		listen, err = nextShareListen(dir)
		if err != nil {
			return "", "", err
		}
	}
	cfg := serverConfig{Listen: listen, Password: password}
	path := shareConfigPath(dir, name)
	if err := writeServerConfig(path, cfg, false); err != nil {
		return "", "", err
	}
	bin, err := install.SelfInstall()
	if err != nil {
		return "", "", fmt.Errorf("安装 bx 到 PATH: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	if err := install.WriteShareUnit(name, fmt.Sprintf("%s serve -c %s", bin, abs)); err != nil {
		return "", "", err
	}
	if err := install.EnableShare(name); err != nil {
		return "", "", err
	}
	if host != "" {
		link, err = bxServerLink(host, cfg)
		if err != nil {
			return "", "", err
		}
	}
	return link, listen, nil
}

func revokeShare(name, dir string) error {
	if err := install.UninstallShare(name); err != nil {
		return err
	}
	if err := os.Remove(shareConfigPath(dir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func serverRotateAction(c *cli.Context) error {
	password := c.String("password")
	if password == "" {
		var err error
		password, err = randomPassword()
		if err != nil {
			return err
		}
	}
	cfg, err := rotateServerConfig(c.String("config"), password)
	if err != nil {
		return err
	}
	fmt.Println("✅ bx server 密码已轮换。旧 bx:// 链接将失效。")
	if !c.Bool("no-restart") {
		switch state := serviceState("is-active", install.ServerServiceName); state {
		case "active":
			if err := install.RestartServer(); err != nil {
				return err
			}
			fmt.Println("✅ bx server 已重启,新链接已生效。")
		default:
			fmt.Printf("server 当前状态: %s。启动后新链接生效: sudo bx server start\n", state)
		}
	}
	if host := c.String("host"); host != "" {
		link, err := bxServerLink(host, cfg)
		if err != nil {
			return err
		}
		fmt.Println(link)
	} else {
		fmt.Println("需要新客户端链接时运行: sudo bx server link --host <VPS_IP或域名>")
	}
	return nil
}

func serverStartAction(c *cli.Context) error {
	if !install.ServerUnitInstalled() {
		return fmt.Errorf("尚未安装 bx server。先运行: sudo bx server install")
	}
	if err := install.EnableServer(); err != nil {
		return err
	}
	fmt.Println("✅ bx server 已启动并设为开机自启。")
	return nil
}

func serverStopAction(c *cli.Context) error {
	if err := install.DisableServer(); err != nil {
		return err
	}
	fmt.Println("✅ bx server 已停止并取消开机自启。")
	return nil
}

func serverStatusAction(c *cli.Context) error {
	active := serviceState("is-active", install.ServerServiceName)
	enabled := serviceState("is-enabled", install.ServerServiceName)
	fmt.Printf("bx server: %s, boot: %s\n", active, enabled)
	return nil
}

func serverLogsAction(c *cli.Context) error {
	return install.ShowLogs(install.ServerServiceName, c.Int("lines"), c.Bool("follow"))
}

func serviceState(action, service string) string {
	return install.ServiceState(action, service)
}

func serverUninstallAction(c *cli.Context) error {
	if err := install.UninstallServer(); err != nil {
		return err
	}
	fmt.Println("已卸载 bx server 服务")
	return nil
}

func serveAction(c *cli.Context) error {
	cfg, err := readServerConfig(c.String("config"))
	if err != nil {
		return err
	}
	path, err := provision.EnsureBrook("/var/lib/bx", "", embedded.Brook(), embedded.BrookVersion())
	if err != nil {
		return fmt.Errorf("准备运行环境: %w", err)
	}
	cmd := exec.CommandContext(c.Context, path, "server", "-l", cfg.Listen, "-p", cfg.Password)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := procredact.RedactArg(cmd.Process.Pid, cfg.Password); err != nil && os.Getenv("BX_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "warning: could not redact server secret from child argv: %v\n", err)
	}
	return cmd.Wait()
}

func doctorAction(c *cli.Context) (err error) {
	if !c.Bool("json") {
		defer autoArchiveAfterClientCommand("doctor", &err, true)
	}
	if c.Bool("json") {
		return writeJSON(os.Stdout, collectClientDoctor(c.String("config"), c.String("target"), c.Duration("timeout"), c.Bool("skip-probe")))
	}
	fmt.Println("bx doctor")
	doctorLine("ok", "version", version.String())
	cfgPath := resolveConfigPath(c.String("config"))
	doctorLine("info", "config", cfgPath)
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		doctorLine("fail", "config readable", err.Error())
		doctorLine("hint", "setup", "sudo bx setup <client-link>")
	} else {
		doctorLine("ok", "config readable", "yes")
		checkFileMode(cfgPath, 0o600)
		cfg, err := config.Parse(b)
		if err != nil {
			doctorLine("fail", "config parse", err.Error())
		} else {
			doctorLine("ok", "config parse", "yes")
			if cfg.Server == "" {
				doctorLine("fail", "server link", "empty")
			} else if _, err := blink.Decode(cfg.Server); err != nil && !strings.HasPrefix(cfg.Server, "brook://") {
				doctorLine("fail", "server link", err.Error())
			} else {
				doctorLine("ok", "server link", redactLink(cfg.Server))
				if !c.Bool("skip-probe") {
					doctorProbe(cfg.Server, c.String("target"), c.Duration("timeout"))
				}
			}
		}
	}
	doctorLine(boolStatus(install.UnitInstalled()), "service installed", install.ServiceName)
	activeState := serviceState("is-active", install.ServiceName)
	doctorLine(serviceStatusFromState("is-active", activeState), "service active", activeState)
	if activeState != "active" {
		doctorLine("hint", "logs", "bx logs")
	}
	enabledState := serviceState("is-enabled", install.ServiceName)
	doctorLine(serviceStatusFromState("is-enabled", enabledState), "service enabled", enabledState)
	if err := checkStatusSocket(); err != nil {
		doctorLine("warn", "status socket", err.Error())
		doctorLine("hint", "logs", "bx logs")
	} else {
		doctorLine("ok", "status socket", "reachable")
	}
	return nil
}

func inspectAction(c *cli.Context) error {
	return writeJSON(os.Stdout, collectClientInspect(c.String("config"), c.String("target"), c.Duration("timeout"), c.Bool("skip-probe")))
}

func serverDoctorAction(c *cli.Context) error {
	if c.Bool("json") {
		return writeJSON(os.Stdout, collectServerDoctor(c.String("config"), c.String("shares-dir")))
	}
	fmt.Println("bx server doctor")
	doctorLine("ok", "version", version.String())
	cfgPath := c.String("config")
	doctorLine("info", "config", cfgPath)
	cfg, err := readServerConfig(cfgPath)
	if err != nil {
		doctorLine("fail", "config parse", err.Error())
		doctorLine("hint", "install", "sudo bx server install --host <VPS_IP或域名>")
	} else {
		doctorLine("ok", "config parse", "yes")
		checkFileMode(cfgPath, 0o600)
		if port := listenPort(cfg.Listen); port == "" {
			doctorLine("fail", "listen", cfg.Listen)
		} else {
			doctorLine("ok", "listen", cfg.Listen)
			if isListening(port) {
				doctorLine("ok", "port listening", "tcp/"+port)
			} else {
				doctorLine("warn", "port listening", "tcp/"+port+" not detected")
			}
			if hint := serverFirewallHint(cfg.Listen); hint != "" {
				doctorLine("hint", "firewall", hint)
			}
		}
	}
	doctorLine(boolStatus(install.ServerUnitInstalled()), "service installed", install.ServerServiceName)
	doctorLine(serviceStatus("is-active", install.ServerServiceName), "service active", serviceState("is-active", install.ServerServiceName))
	doctorLine(serviceStatus("is-enabled", install.ServerServiceName), "service enabled", serviceState("is-enabled", install.ServerServiceName))
	doctorShares(c.String("shares-dir"))
	return nil
}

func capabilitiesAction(c *cli.Context) error {
	return writeJSON(os.Stdout, capabilities())
}

func capabilities() capabilitiesReport {
	return capabilitiesReport{
		SchemaVersion:   1,
		Product:         "bx",
		Version:         version.String(),
		SecretsRedacted: true,
		Commands: []commandCapability{
			{
				Command:        "bx capabilities",
				Category:       "discovery",
				Summary:        "List stable machine-readable bx commands and their safety properties.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"json"},
				Examples:       []string{"bx capabilities"},
				SafeNotes:      []string{"Read-only. Use this before choosing another bx command."},
			},
			{
				Command:        "bx doctor --json",
				Category:       "diagnostics",
				Summary:        "Diagnose client config, service state, status socket, and optional link probe.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--skip-probe", "--config <path>", "--target <host:port>", "--timeout <duration>"},
				Examples:       []string{"bx doctor --json", "bx doctor --json --skip-probe"},
				SafeNotes:      []string{"Read-only.", "Secrets are redacted.", "Pass --skip-probe to avoid network probing."},
			},
			{
				Command:        "bx inspect --json",
				Category:       "diagnostics",
				Summary:        "Bundle capabilities, status, doctor checks, and next actions for an agent.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--skip-probe", "--config <path>", "--target <host:port>", "--timeout <duration>"},
				Examples:       []string{"bx inspect --json", "bx inspect --json --skip-probe"},
				SafeNotes:      []string{"Read-only.", "Secrets are redacted.", "Status socket failures are reported as data."},
			},
			{
				Command:        "sudo bx server doctor --json",
				Category:       "diagnostics",
				Summary:        "Diagnose server config, service state, listening port, and share services.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--config <path>", "--shares-dir <dir>"},
				Examples:       []string{"sudo bx server doctor --json"},
				SafeNotes:      []string{"Read-only.", "Secrets are redacted."},
			},
			{
				Command:        "sudo bx server shares --json",
				Category:       "inspection",
				Summary:        "List share names, listen addresses, and service states.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--dir <dir>"},
				Examples:       []string{"sudo bx server shares --json"},
				SafeNotes:      []string{"Read-only.", "Share passwords and links are not included."},
			},
			{
				Command:        "bx probe <client-link>",
				Category:       "diagnostics",
				Summary:        "Probe a bx link without writing config or changing routes.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<client-link>", "--target <host:port>", "--timeout <duration>"},
				Examples:       []string{"bx probe '<client-link>'"},
				SafeNotes:      []string{"Network probe only.", "Does not install services or change routing."},
			},
			{
				Command:        "bx status --json",
				Category:       "diagnostics",
				Summary:        "Show current client status as machine-readable JSON.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json"},
				Examples:       []string{"bx status --json"},
				SafeNotes:      []string{"Read-only.", "Used by lightweight status surfaces such as a menu bar helper."},
			},
			{
				Command:        "bx logs",
				Category:       "diagnostics",
				Summary:        "Show recent client service logs.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Arguments:      []string{"--lines <n>", "--follow", "--archive", "--dir <path>"},
				Examples:       []string{"bx logs", "bx logs -n 200", "bx logs --archive"},
				SafeNotes:      []string{"Read-only.", "May require sudo depending on system log permissions.", "Use --archive to preserve raw logs and diagnostic snapshots.", "Automatic diagnostics are kept under the platform log directory for bx up/down/doctor."},
			},
			{
				Command:        "bx realtime status",
				Category:       "udp",
				Summary:        "Inspect the advanced UDP policy. bx up enables UDP relay by default.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"bx realtime status", "bx doctor --json"},
				SafeNotes:      []string{"Read-only.", "UDP policy is currently visible through bx status and bx doctor --json."},
			},
			{
				Command:        "sudo bx realtime on",
				Category:       "udp",
				Summary:        "Return the advanced UDP policy to the default relay mode.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"--config <path>", "--no-restart"},
				Examples:       []string{"sudo bx realtime on"},
				SafeNotes:      []string{"Writes client config and restarts bx automatically when the service is active.", "Use --no-restart to write config only.", "Relays non-DNS UDP through bx instead of using the local real network path."},
			},
			{
				Command:        "sudo bx realtime off",
				Category:       "udp",
				Summary:        "Advanced: block non-DNS UDP explicitly.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"--config <path>", "--no-restart"},
				Examples:       []string{"sudo bx realtime off"},
				SafeNotes:      []string{"Writes client config and restarts bx automatically when the service is active.", "Use --no-restart to write config only.", "Block mode blocks non-DNS UDP."},
			},
			{
				Command:        "bx dns status",
				Category:       "dns",
				Summary:        "Inspect macOS system DNS takeover state.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Arguments:      []string{"--service <name>"},
				Examples:       []string{"bx dns status"},
				SafeNotes:      []string{"Read-only.", "Only supported on macOS."},
			},
			{
				Command:        "sudo bx dns on",
				Category:       "dns",
				Summary:        "Manually set the active macOS network service DNS to bx and save the original DNS for rollback.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: true,
				Outputs:        []string{"text"},
				Arguments:      []string{"--service <name>"},
				Examples:       []string{"sudo bx dns on"},
				SafeNotes:      []string{"Only supported on macOS.", "sudo bx up already does this on macOS.", "Use sudo bx dns off to restore the saved DNS."},
			},
			{
				Command:        "sudo bx dns off",
				Category:       "dns",
				Summary:        "Restore the macOS DNS values saved by bx dns on.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: true,
				Outputs:        []string{"text"},
				Arguments:      []string{"--service <name>"},
				Examples:       []string{"sudo bx dns off"},
				SafeNotes:      []string{"Only supported on macOS.", "Restores the saved DNS state instead of guessing."},
			},
			{
				Command:        "scripts/install-macos-menu.sh install",
				Category:       "macos",
				Summary:        "Install and start the macOS menu bar app.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"scripts/install-macos-menu.sh install"},
				SafeNotes:      []string{"macOS only.", "Installs Bx.app under ~/Applications and a user LaunchAgent.", "Does not start protection, change DNS, routes, or client config."},
			},
			{
				Command:        "scripts/package-macos-release.sh",
				Category:       "macos",
				Summary:        "Build a distributable macOS release folder with bx CLI, Bx.app, install.sh, uninstall.sh, and README.txt.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"files", "tar.gz"},
				Arguments:      []string{"BX_ARCH=arm64|amd64", "BX_VERSION=<version>", "BX_RELEASE_DIR=<dir>"},
				Examples:       []string{"scripts/package-macos-release.sh", "BX_ARCH=amd64 scripts/package-macos-release.sh"},
				SafeNotes:      []string{"macOS only.", "Builds release artifacts under dist/release by default.", "Does not install bx, start protection, change DNS, routes, or client config."},
			},
			{
				Command:        "scripts/verify-macos-release.sh",
				Category:       "macos",
				Summary:        "Verify the macOS release folder, archive, plist, safety notes, and SHA256SUMS.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Arguments:      []string{"BX_ARCH=arm64|amd64", "BX_RELEASE_DIR=<dir>"},
				Examples:       []string{"scripts/verify-macos-release.sh"},
				SafeNotes:      []string{"Read-only.", "macOS only.", "Does not install bx, start protection, change DNS, routes, or client config."},
			},
			{
				Command:        "scripts/install-macos-menu.sh status",
				Category:       "macos",
				Summary:        "Inspect the macOS menu bar app install and launch state.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"scripts/install-macos-menu.sh status"},
				SafeNotes:      []string{"Read-only.", "macOS only."},
			},
			{
				Command:        "scripts/install-macos-menu.sh restart",
				Category:       "macos",
				Summary:        "Restart the macOS menu bar app LaunchAgent.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"scripts/install-macos-menu.sh restart"},
				SafeNotes:      []string{"macOS only.", "Restarts only the menu bar app, not protection."},
			},
			{
				Command:        "scripts/install-macos-menu.sh uninstall",
				Category:       "macos",
				Summary:        "Remove the macOS menu bar app and its user LaunchAgent.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"scripts/install-macos-menu.sh uninstall"},
				SafeNotes:      []string{"macOS only.", "Does not turn off protection, change DNS, routes, or client config."},
			},
			{
				Command:        "sudo bx setup <client-link>",
				Category:       "client",
				Summary:        "Install bx client service and write client config.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<client-link>", "--config <path>", "--force", "--strict"},
				Examples:       []string{"sudo bx setup '<client-link>'"},
				SafeNotes:      []string{"Does not start traffic routing by itself."},
			},
			{
				Command:        "sudo bx up",
				Category:       "client",
				Summary:        "Start bx client service, enable it at boot, and enter runtime traffic takeover.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: true,
				Outputs:        []string{"text"},
				Examples:       []string{"sudo bx up"},
				SafeNotes:      []string{"On macOS, this also switches system DNS to bx after the service is ready.", "If DNS takeover fails, bx rolls the service start back."},
			},
			{
				Command:        "sudo bx down",
				Category:       "client",
				Summary:        "Leave runtime traffic takeover, restore DNS on macOS, and stop bx client service.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: true,
				Outputs:        []string{"text"},
				Examples:       []string{"sudo bx down"},
				SafeNotes:      []string{"On macOS, DNS is restored before the service is stopped."},
			},
			{
				Command:        "sudo bx server install --host <host>",
				Category:       "server",
				Summary:        "Install bx server service, generate config, and optionally print a bx link.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"--host <host>", "--listen <addr>", "--password <password>", "--force"},
				Examples:       []string{"sudo bx server install --host <host>"},
			},
			{
				Command:        "sudo bx server share <name> --host <host>",
				Category:       "server",
				Summary:        "Create an independent share service and print a bx link for that user.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<name>", "--host <host>", "--listen <addr>", "--password <password>", "--open-ufw"},
				Examples:       []string{"sudo bx server share alice --host <host>"},
				SafeNotes:      []string{"May change firewall only when --open-ufw is passed."},
			},
			{
				Command:        "sudo bx server revoke <name>",
				Category:       "server",
				Summary:        "Stop and remove one share service.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Arguments:      []string{"<name>"},
				Examples:       []string{"sudo bx server revoke alice"},
			},
			{
				Command:        "bx mcp install",
				Category:       "onboarding",
				Summary:        "Print the MCP pairing instruction so an agent can register bx's control plane with itself.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"bx mcp install"},
				SafeNotes:      []string{"Print-only; runs nothing. An AI agent reading the output can run the printed `claude mcp add` to gain bx's control plane, authorized as the machine owner (no sudo)."},
			},
		},
	}
}

func collectClientDoctor(configPath, target string, timeout time.Duration, skipProbe bool) doctorReport {
	rep := doctorReport{Kind: "client", Version: version.String(), SecretsRedacted: true}
	cfgPath := resolveConfigPath(configPath)
	udpMode := "proxy"
	rep.addCheck("config", "info", cfgPath, "")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		rep.addCheck("config_readable", "fail", err.Error(), "sudo bx setup <client-link>")
	} else {
		rep.addCheck("config_readable", "ok", "yes", "")
		if modeCheck(cfgPath, 0o600) {
			rep.addCheck("config_permissions", "ok", "0600", "")
		} else {
			rep.addCheck("config_permissions", "warn", "not 0600", "chmod 600 "+cfgPath)
		}
		cfg, err := config.Parse(b)
		if err != nil {
			rep.addCheck("config_parse", "fail", err.Error(), "")
		} else {
			rep.addCheck("config_parse", "ok", "yes", "")
			udpMode = cfg.UDP.Mode
			if cfg.Server == "" {
				rep.addCheck("server_link", "fail", "empty", "sudo bx setup <client-link>")
			} else if _, err := blink.Decode(cfg.Server); err != nil && !strings.HasPrefix(cfg.Server, "brook://") {
				rep.addCheck("server_link", "fail", err.Error(), "")
			} else {
				rep.addCheck("server_link", "ok", redactLink(cfg.Server), "")
				if !skipProbe {
					rep.addReport(probeCheck(cfg.Server, target, timeout))
				}
			}
		}
	}
	rep.addCheck("service_installed", boolStatus(install.UnitInstalled()), install.ServiceName, "sudo bx setup <client-link>")
	activeState := serviceState("is-active", install.ServiceName)
	rep.addCheck("service_active", serviceStatusFromState("is-active", activeState), activeState, hintForState(activeState, "sudo bx up", "bx logs"))
	enabledState := serviceState("is-enabled", install.ServiceName)
	rep.addCheck("service_enabled", serviceStatusFromState("is-enabled", enabledState), enabledState, "sudo bx up")
	if err := checkStatusSocket(); err != nil {
		rep.addCheck("status_socket", "warn", err.Error(), "bx logs")
	} else {
		rep.addCheck("status_socket", "ok", "reachable", "")
	}
	status, detail, hint := udpPolicyDoctor(udpMode)
	rep.addCheck("udp_policy", status, detail, hint)
	rep.OK = !rep.hasFail()
	return rep
}

func collectClientInspect(configPath, target string, timeout time.Duration, skipProbe bool) inspectReport {
	doctor := collectClientDoctor(configPath, target, timeout, skipProbe)
	rep := inspectReport{
		Kind:            "client",
		Version:         version.String(),
		SecretsRedacted: true,
		Capabilities:    capabilities(),
		Doctor:          doctor,
	}
	if status, err := readStatusReport(); err == nil {
		rep.Status = &status
	} else {
		rep.StatusError = err.Error()
		rep.NextActions = append(rep.NextActions, "sudo bx up")
	}
	rep.NextActions = appendUnique(rep.NextActions, doctorNextActions(doctor)...)
	rep.OK = doctor.OK && rep.StatusError == ""
	return rep
}

func doctorNextActions(rep doctorReport) []string {
	var out []string
	for _, check := range rep.Checks {
		if check.Hint == "" || check.Status == "ok" || check.Status == "info" {
			continue
		}
		for _, part := range strings.Split(check.Hint, ";") {
			if a := strings.TrimSpace(part); a != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

func appendUnique(base []string, add ...string) []string {
	seen := map[string]bool{}
	for _, v := range base {
		seen[v] = true
	}
	for _, v := range add {
		if seen[v] {
			continue
		}
		base = append(base, v)
		seen[v] = true
	}
	return base
}

func udpPolicyDoctor(mode string) (status, detail, hint string) {
	switch mode {
	case "proxy":
		return "ok", "non-DNS UDP relayed through bx tunnel", ""
	case "direct-realtime":
		return "warn", "non-DNS UDP direct; may expose real network path", "Use sudo bx realtime on to relay UDP through bx, or sudo bx realtime off to block it"
	default:
		return "warn", "non-DNS UDP blocked", "Google Meet/WebRTC may stutter; use sudo bx realtime on"
	}
}

func collectServerDoctor(configPath, sharesDir string) doctorReport {
	rep := doctorReport{Kind: "server", Version: version.String(), SecretsRedacted: true, RequiresRoot: true}
	cfg, err := readServerConfig(configPath)
	if err != nil {
		rep.addCheck("config_parse", "fail", err.Error(), "sudo bx server install --host <host>")
	} else {
		rep.addCheck("config_parse", "ok", "yes", "")
		if modeCheck(configPath, 0o600) {
			rep.addCheck("config_permissions", "ok", "0600", "")
		} else {
			rep.addCheck("config_permissions", "warn", "not 0600", "chmod 600 "+configPath)
		}
		if port := listenPort(cfg.Listen); port == "" {
			rep.addCheck("listen", "fail", cfg.Listen, "")
		} else {
			rep.addCheck("listen", "ok", cfg.Listen, "")
			status := "warn"
			detail := "tcp/" + port + " not detected"
			if isListening(port) {
				status = "ok"
				detail = "tcp/" + port
			}
			rep.addCheck("port_listening", status, detail, serverFirewallHint(cfg.Listen))
		}
	}
	rep.addCheck("service_installed", boolStatus(install.ServerUnitInstalled()), install.ServerServiceName, "sudo bx server install --host <host>")
	rep.addCheck("service_active", serviceStatus("is-active", install.ServerServiceName), serviceState("is-active", install.ServerServiceName), "sudo bx server start")
	rep.addCheck("service_enabled", serviceStatus("is-enabled", install.ServerServiceName), serviceState("is-enabled", install.ServerServiceName), "sudo bx server start")
	for _, check := range shareChecks(sharesDir) {
		rep.addReport(check)
	}
	rep.OK = !rep.hasFail()
	return rep
}

func probeCheck(link, target string, timeout time.Duration) checkReport {
	raw, err := blink.Decode(link)
	if err != nil {
		raw = link
	}
	dir, err := userRuntimeDir()
	if err != nil {
		return checkReport{Name: "probe", Status: "warn", Detail: err.Error()}
	}
	lat, err := setup.ProbeServer(dir, raw, target, timeout)
	if err != nil {
		return checkReport{Name: "probe", Status: "fail", Detail: err.Error()}
	}
	return checkReport{Name: "probe", Status: "ok", Detail: fmt.Sprintf("%s %dms", target, lat)}
}

func doctorShares(dir string) {
	shares, err := readShares(dir)
	if err != nil {
		doctorLine("warn", "shares", err.Error())
		return
	}
	if len(shares) == 0 {
		doctorLine("info", "shares", "none")
		return
	}
	for _, s := range shares {
		state := serviceState("is-active", install.ShareServiceName(s.Name))
		port := listenPort(s.Config.Listen)
		if port == "" {
			doctorLine("fail", "share "+s.Name, "bad listen "+s.Config.Listen)
			continue
		}
		listenState := shareListenState(port)
		status := shareDoctorStatus(state, listenState)
		doctorLine(status, "share "+s.Name, fmt.Sprintf("%s, tcp/%s %s", state, port, listenState))
	}
}

func shareChecks(dir string) []checkReport {
	shares, err := readShares(dir)
	if err != nil {
		return []checkReport{{Name: "shares", Status: "warn", Detail: err.Error()}}
	}
	if len(shares) == 0 {
		return []checkReport{{Name: "shares", Status: "info", Detail: "none"}}
	}
	var checks []checkReport
	for _, s := range shares {
		state := serviceState("is-active", install.ShareServiceName(s.Name))
		port := listenPort(s.Config.Listen)
		if port == "" {
			checks = append(checks, checkReport{Name: "share." + s.Name, Status: "fail", Detail: "bad listen " + s.Config.Listen})
			continue
		}
		listenState := shareListenState(port)
		checks = append(checks, checkReport{
			Name:   "share." + s.Name,
			Status: shareDoctorStatus(state, listenState),
			Detail: fmt.Sprintf("%s, tcp/%s %s", state, port, listenState),
			Hint:   serverFirewallHint(s.Config.Listen),
		})
	}
	return checks
}

func shareListenState(port string) string {
	if isListening(port) {
		return "listening"
	}
	return "not-listening"
}

func shareDoctorStatus(serviceState, listenState string) string {
	if serviceState == "active" && listenState == "listening" {
		return "ok"
	}
	return "warn"
}

func (r *doctorReport) addCheck(name, status, detail, hint string) {
	r.addReport(checkReport{Name: name, Status: status, Detail: detail, Hint: hint})
}

func (r *doctorReport) addReport(check checkReport) {
	r.Checks = append(r.Checks, check)
}

func (r doctorReport) hasFail() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

func doctorProbe(link, target string, timeout time.Duration) {
	raw, err := blink.Decode(link)
	if err != nil {
		raw = link
	}
	dir, err := userRuntimeDir()
	if err != nil {
		doctorLine("warn", "probe", err.Error())
		return
	}
	lat, err := setup.ProbeServer(dir, raw, target, timeout)
	if err != nil {
		doctorLine("fail", "probe", err.Error())
		return
	}
	doctorLine("ok", "probe", fmt.Sprintf("%s %dms", target, lat))
}

func setupFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "配置写入路径"},
		&cli.StringFlag{Name: "probe", Value: defaultProbeTarget, Usage: "连通检测目标"},
		&cli.BoolFlag{Name: "force", Usage: "覆盖已存在的配置"},
		&cli.BoolFlag{Name: "strict", Usage: "连通检测失败则中止(默认仅警告)"},
	}
}

func probeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "target", Value: defaultProbeTarget, Usage: "连通检测目标"},
		&cli.DurationFlag{Name: "timeout", Value: 15 * time.Second, Usage: "检测超时"},
	}
}

func probeAction(c *cli.Context) error {
	arg := c.Args().First()
	if arg == "" {
		return fmt.Errorf("用法: bx probe <客户端链接>")
	}
	link, _, err := normalizeClientLink(arg)
	if err != nil {
		return err
	}
	if w := rawLinkRisk(arg); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	dir, err := userRuntimeDir()
	if err != nil {
		return err
	}
	fmt.Println("⏳ 连通检测中…")
	lat, err := setup.ProbeServer(dir, link, c.String("target"), c.Duration("timeout"))
	if err != nil {
		return fmt.Errorf("连通检测失败: %w", err)
	}
	fmt.Printf("✅ 服务器连通,延迟 %dms\n", lat)
	return nil
}

func userRuntimeDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bx"), nil
}

func setupAction(c *cli.Context) error {
	arg := c.Args().First()
	if arg == "" {
		return fmt.Errorf("用法: sudo bx setup <客户端链接>")
	}
	link, configLink, err := normalizeClientLink(arg)
	if err != nil {
		return err
	}
	if w := rawLinkRisk(arg); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	cfgPath := c.String("config")
	fmt.Println("⏳ 连通检测中…")
	if lat, perr := setup.ProbeServer("/var/lib/bx", link, c.String("probe"), 15*time.Second); perr != nil {
		if c.Bool("strict") {
			return fmt.Errorf("连通检测失败: %w", perr)
		}
		fmt.Printf("⚠️  连通检测未通过(仍写配置,稍后可排查): %v\n", perr)
	} else {
		fmt.Printf("✅ 服务器连通,延迟 %dms\n", lat)
	}
	if err := setup.WriteConfig(cfgPath, configLink, c.Bool("force")); err != nil {
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

// rawLinkRisk 返回裸凭据链接的风险提示(空=无需提示)。裸 vless/brook 含明文凭据,
// 一旦敲在命令行就会留进 shell 历史、转发时也是明文;配置本身已 0600+blink 换壳,
// 但命令行/分享面是裸的——建议先 bx blink 换壳成 bx:// 再用。bx://blink:// 已换壳不提示。
func rawLinkRisk(arg string) string {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "vless://") || strings.HasPrefix(arg, "brook://") {
		return "⚠ 这是含明文凭据的裸链接,已留进 shell 历史;分享/留存前建议先用 `bx blink <link>` 换壳成 bx://"
	}
	return ""
}

func normalizeClientLink(arg string) (link string, configLink string, err error) {
	arg = strings.TrimSpace(arg)
	switch {
	case strings.HasPrefix(arg, "brook://"), strings.HasPrefix(arg, "vless://"):
		return arg, blink.Encode(arg), nil
	case strings.HasPrefix(arg, "bx://"), strings.HasPrefix(arg, "blink://"):
		link, err := blink.Decode(arg)
		if err != nil {
			return "", "", err
		}
		if strings.HasPrefix(arg, "blink://") {
			return link, blink.Encode(link), nil
		}
		return link, arg, nil
	default:
		return "", "", fmt.Errorf("不是支持的客户端链接")
	}
}

func runFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "配置文件路径(默认 /etc/bx/config.yaml,非 root 回退 ~/.config/bx/config.yaml)"},
		&cli.StringFlag{Name: "tun", Value: "bx0", Usage: "TUN 设备名"},
		&cli.StringFlag{Name: "tun-addr", Value: "198.51.100.1/30", Usage: "TUN 接口地址(TEST-NET-2,避开 docker 默认地址池 172.16/12 防撞段)"},
		&cli.UintFlag{Name: "mtu", Value: 1500},
		&cli.StringFlag{Name: "brook", Value: "", Usage: "内部传输二进制路径", Hidden: true},
		&cli.StringFlag{Name: "china-domain", Value: "", Usage: "china 域名列表(留空=用内嵌/自动刷新快照)"},
		&cli.StringFlag{Name: "china-cidr", Value: "", Usage: "china IP 段(留空=用内嵌/自动刷新快照)"},
		&cli.StringFlag{Name: "probe", Value: defaultProbeTarget, Usage: "隧道健康检查目标"},
		&cli.DurationFlag{Name: "health-timeout", Value: 20 * time.Second, Usage: "等待隧道健康的启动超时"},
		&cli.DurationFlag{Name: "test-timeout", Usage: "死手定时器:到点自动还原(远程实测保命)"},
		&cli.BoolFlag{Name: "global", Aliases: []string{"g"}, Usage: "全局模式:除内网(bypass)/用户 direct 规则外,一切(含中国)走代理"},
		&cli.StringFlag{Name: "listen-dns", Value: "", Usage: "本地 DNS 监听地址(默认关闭;macOS 测试可用 127.0.0.1:53)"},
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
		HealthTimeout:   c.Duration("health-timeout"),
		Deadman:         c.Duration("test-timeout"),
		Global:          c.Bool("global"),
		DNSListen:       c.String("listen-dns"),
	}
}

func darwinPlanFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "tun", Value: "utunX", Usage: "计划中的 utun 设备名"},
		&cli.StringFlag{Name: "tun-addr", Value: "198.51.100.1/30", Usage: "计划中的 TUN 接口地址"},
		&cli.StringFlag{Name: "gateway", Usage: "当前物理默认网关,例如 192.168.1.1"},
		&cli.StringSliceFlag{Name: "server-bypass", Usage: "服务器旁路 CIDR,可重复"},
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

func routerPlanFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径(取 router.lan_cidrs)"},
		&cli.StringFlag{Name: "tun", Value: "bx0", Usage: "计划中的 TUN 设备名"},
		&cli.StringFlag{Name: "lan-ifaces", Value: "br-lan", Usage: "LAN 接口名(逗号分隔;真机由 lan_cidrs 自动探测)"},
	}
}

// routerPlanAction 打印 router 模式会下发的 ip + nft 命令(不执行),供部署前审阅。
// serverHostFromLink 从 brook:// 或 vless:// 链接解析出 server 主机(用于 router-plan 显示 server bypass)。
func serverHostFromLink(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if u.Scheme == "vless" { // reality: host is in the authority, not a ?server= param
		return u.Hostname()
	}
	s := u.Query().Get("server")
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		return host
	}
	return s
}

func routerPlanAction(c *cli.Context) error {
	cfg, err := loadConfig(c.String("config"))
	if err != nil {
		return err
	}
	if cfg.Mode != "router" {
		fmt.Printf("# 注意: 配置 mode=%q(非 router);以下为「若启用 router」的计划\n", cfg.Mode)
	}
	if len(cfg.Router.LANCIDRs) == 0 {
		return fmt.Errorf("router.lan_cidrs 为空:dry-run 需要显式网段(真机可自动探测)")
	}
	tun := c.String("tun")
	var ifaces []string
	for _, s := range strings.Split(c.String("lan-ifaces"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			ifaces = append(ifaces, s)
		}
	}
	var serverBypass []string
	if h := serverHostFromLink(cfg.Server); h != "" {
		serverBypass = []string{h + "/32"}
	}
	rp := gateway.DefaultRoutePlan(tun, serverBypass, cfg.Bypass, route.DefaultPrivateCIDRs, route.DefaultPrivateV6CIDRs)
	fp := gateway.DefaultFirewallPlan(tun, ifaces)

	fmt.Println("# dry-run only: no commands executed")
	fmt.Printf("# mode=router lan_cidrs=%v tun=%s lan_ifaces=%v server_bypass=%v\n", cfg.Router.LANCIDRs, tun, ifaces, serverBypass)
	fmt.Println("# apply (routing — catch-all pref 6600 after tailscale; fail-closed blackhole; bx/server/private bypass):")
	for _, s := range rp.InstallArgs() {
		fmt.Println("ip " + strings.Join(s, " "))
	}
	fmt.Println("# apply (firewall — LAN→tun accept, LAN IPv6 drop):")
	for _, r := range fp.InstallRules() {
		fmt.Println("nft " + strings.Join(r, " "))
	}
	fmt.Println("# cleanup (routing):")
	for _, s := range rp.TeardownArgs() {
		fmt.Println("ip " + strings.Join(s, " "))
	}
	fmt.Println("# cleanup (firewall): delete forward rules whose comment matches", gateway.DefaultComment)
	return nil
}

func upAction(c *cli.Context) (err error) {
	defer autoArchiveAfterClientCommand("up", &err, true)
	if !install.UnitInstalled() {
		return fmt.Errorf("尚未配置。先运行: sudo bx setup <client-link>")
	}
	stepLine("服务", "启动 bx")
	// 防呆:命令模型重排后 up=enable service、run=前台。旧 unit 的 ExecStart 仍写
	// `bx up`,配新二进制会让 service 启动时递归调用 up → 死锁。检测到就报错让用户重装。
	cmd, err := install.ExecStartCmd()
	if err != nil {
		return err
	}
	if cmd != "run" {
		return fmt.Errorf("检测到旧版服务配置(启动子命令是 %q,应为 run):直接 up 会让服务递归调用自身。请重跑 sudo bx setup <client-link> 重写服务配置", cmd)
	}
	if err := install.Enable(); err != nil {
		return err
	}
	stepDone("服务", "已启动并设为开机自启")
	if runtime.GOOS == "darwin" {
		stepLine("状态", "等待 bx 就绪")
		if err := waitStatusSocket(15 * time.Second); err != nil {
			_ = install.Disable()
			return fmt.Errorf("bx 服务已启动但状态 socket 未就绪,已回滚: %w", err)
		}
		stepDone("状态", "bx 已就绪")
		stepLine("DNS", "接管 macOS 系统 DNS")
		if _, err := install.EnableDNS(""); err != nil {
			_ = install.Disable()
			return fmt.Errorf("macOS DNS 接管失败,已回滚 bx 服务: %w", err)
		}
		stepDone("DNS", "macOS 系统 DNS 已切到 bx")
	}
	if rep, err := readStatusReport(); err == nil {
		printUpSummary(rep)
		return nil
	}
	fmt.Println("✅ bx 已启动。")
	return nil
}

func downAction(c *cli.Context) (err error) {
	defer autoArchiveAfterClientCommand("down", &err, true)
	if runtime.GOOS == "darwin" {
		st, err := install.DisableDNS("")
		if err != nil {
			return fmt.Errorf("恢复 macOS DNS 失败,未停止 bx: %w", err)
		}
		if st.Supported {
			fmt.Println("✅ macOS 系统 DNS 已确认恢复。")
		}
	}
	if err := install.Disable(); err != nil {
		return err
	}
	fmt.Println("✅ bx 已停止并取消开机自启。")
	return nil
}

func dnsStatusAction(c *cli.Context) error {
	st, err := install.InspectDNS(c.String("service"))
	if err != nil {
		return err
	}
	printDNSStatus(st)
	return nil
}

func dnsOnAction(c *cli.Context) error {
	st, err := install.EnableDNS(c.String("service"))
	if err != nil {
		return err
	}
	printDNSStatus(st)
	fmt.Println("✅ macOS 系统 DNS 已切到 bx。恢复: sudo bx dns off")
	return nil
}

func dnsOffAction(c *cli.Context) error {
	st, err := install.DisableDNS(c.String("service"))
	if err != nil {
		return err
	}
	printDNSStatus(st)
	fmt.Println("✅ macOS 系统 DNS 已确认恢复。")
	return nil
}

func linkAction(c *cli.Context) error {
	arg := c.Args().First()
	if !strings.HasPrefix(arg, "brook://") {
		return fmt.Errorf("用法: bx link <internal-link>")
	}
	fmt.Println(blink.Encode(arg))
	return nil
}

func statusAction(c *cli.Context) error {
	rep, err := readStatusReport()
	if err != nil {
		if c.Bool("json") {
			return err // 机器面:不变(返回错误)
		}
		fmt.Print(stats.RenderNotRunning()) // 人面:友好 + exit 0
		return nil
	}
	if c.Bool("json") {
		return writeJSON(os.Stdout, rep)
	}
	fmt.Print(stats.Render(rep))
	return nil
}

func readStatusReport() (stats.Report, error) {
	rep, err := supervisor.FetchStatusReport(supervisor.SockPath)
	if err != nil {
		return stats.Report{}, fmt.Errorf("连接 bx 失败(bx 是否在运行?): %w", err)
	}
	return rep, nil
}

func printUpSummary(rep stats.Report) {
	state := "Protected"
	if !rep.TunnelHealthy {
		state = "Needs Attention"
	}
	fmt.Println()
	fmt.Println("bx is on")
	fmt.Printf("  Status     %s\n", state)
	fmt.Printf("  Tunnel     %dms\n", rep.LatencyMS)
	fmt.Printf("  UDP Relay  %s\n", onOff(rep.UDPMode == "proxy"))
}

func stepLine(name, detail string) {
	fmt.Printf("• %-8s %s\n", name, detail)
}

func stepDone(name, detail string) {
	fmt.Printf("✓ %-8s %s\n", name, detail)
}

func onOff(ok bool) string {
	if ok {
		return "On"
	}
	return "Off"
}

func realtimeStatusAction(c *cli.Context) error {
	rep := readRealtimeReport()
	if rep == nil {
		rep = realtimeReportFromConfig(c.String("config"))
	}
	fmt.Print(renderRealtimeStatus(rep))
	return nil
}

func realtimeOnAction(c *cli.Context) error {
	if err := setRealtimeMode(c.String("config"), "proxy"); err != nil {
		return err
	}
	fmt.Println("✅ realtime 已开启: 非 DNS UDP 将通过 bx 隧道中继。")
	return applyRealtimePostChange(c)
}

func realtimeOffAction(c *cli.Context) error {
	if err := setRealtimeMode(c.String("config"), "block"); err != nil {
		return err
	}
	fmt.Println("✅ realtime 已关闭: 非 DNS UDP 将恢复阻断。")
	return applyRealtimePostChange(c)
}

type realtimePostChangePlan struct {
	Restart bool
	Message string
}

func planRealtimePostChange(noRestart, unitInstalled bool, activeState string) realtimePostChangePlan {
	if noRestart {
		return realtimePostChangePlan{Message: "重启 bx 生效: sudo bx down && sudo bx up"}
	}
	if !unitInstalled {
		return realtimePostChangePlan{Message: "尚未安装服务。下次运行 sudo bx up 时生效。"}
	}
	if activeState == "active" {
		return realtimePostChangePlan{Restart: true, Message: "已重启 bx,配置已生效。"}
	}
	return realtimePostChangePlan{Message: "bx 当前未运行。下次 sudo bx up 时生效。"}
}

func applyRealtimePostChange(c *cli.Context) error {
	installed := install.UnitInstalled()
	active := "inactive"
	if installed {
		active = serviceState("is-active", install.ServiceName)
	}
	plan := planRealtimePostChange(c.Bool("no-restart"), installed, active)
	if plan.Restart {
		if err := install.Restart(); err != nil {
			return fmt.Errorf("重启 bx 失败,配置已写入;可手动执行 sudo bx down && sudo bx up: %w", err)
		}
		if runtime.GOOS == "darwin" {
			if err := waitStatusSocket(15 * time.Second); err != nil {
				return fmt.Errorf("bx 已重启但状态 socket 未就绪: %w", err)
			}
		}
	}
	fmt.Println("✅ " + plan.Message)
	return nil
}

func readRealtimeReport() *stats.Report {
	rep, err := supervisor.FetchStatusReport(supervisor.SockPath)
	if err != nil {
		return nil
	}
	return &rep
}

func renderRealtimeStatus(rep *stats.Report) string {
	mode := "proxy"
	note := "non-DNS UDP relayed through bx tunnel"
	blocked := "unknown"
	if rep != nil {
		if rep.UDPMode != "" {
			mode = rep.UDPMode
		}
		if rep.UDPNote != "" {
			note = rep.UDPNote
		}
		blocked = fmt.Sprint(rep.UDPBlocked)
	}
	var b strings.Builder
	fmt.Fprintln(&b, "realtime supported: true")
	fmt.Fprintf(&b, "udp mode: %s\n", mode)
	fmt.Fprintf(&b, "udp blocked: %s\n", blocked)
	fmt.Fprintf(&b, "detail: %s\n", note)
	return b.String()
}

func realtimeReportFromConfig(path string) *stats.Report {
	cfg, err := loadConfig(path)
	if err != nil {
		return nil
	}
	return &stats.Report{
		UDPMode: cfg.UDP.Mode,
		UDPNote: realtimeNote(cfg.UDP.Mode),
	}
}

func realtimeNote(mode string) string {
	switch mode {
	case "direct-realtime":
		return "non-DNS UDP direct; may expose real network path"
	case "proxy":
		return "non-DNS UDP relayed through bx tunnel"
	default:
		return "non-DNS UDP blocked; WebRTC/Google Meet may stutter"
	}
}

func setRealtimeMode(path, mode string) error {
	path = resolveConfigPath(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读配置 %s: %w", path, err)
	}
	if _, err := config.Parse(b); err != nil {
		return err
	}
	out := setYAMLScalar(b, "udp", "mode", mode)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("写配置 %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

func setYAMLScalar(in []byte, section, key, value string) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return in
	}
	root := doc.Content[0]
	sec := mappingValue(root, section)
	if sec == nil {
		sec = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: section}, sec)
	}
	if sec.Kind != yaml.MappingNode {
		sec.Kind = yaml.MappingNode
		sec.Tag = "!!map"
		sec.Value = ""
		sec.Content = nil
	}
	val := mappingValue(sec, key)
	if val == nil {
		sec.Content = append(sec.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	} else {
		val.Kind = yaml.ScalarNode
		val.Tag = "!!str"
		val.Value = value
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return in
	}
	return out
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func logsAction(c *cli.Context) error {
	if c.Bool("archive") {
		if c.Bool("follow") {
			return fmt.Errorf("--archive 不能和 --follow 同时使用")
		}
		dir, err := archiveClientLogs(c.String("dir"))
		if err != nil {
			return err
		}
		fmt.Println("Logs archived:", dir)
		return nil
	}
	return install.ShowLogs(install.ServiceName, c.Int("lines"), c.Bool("follow"))
}

func archiveClientLogs(root string) (string, error) {
	return archiveClientLogsWithReason(root, "manual")
}

func archiveClientLogsWithReason(root, reason string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = defaultLogArchiveDir
	}
	now := time.Now()
	dir := filepath.Join(root, "bx-logs-"+now.Format("20060102-150405.000000000"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	meta := []string{
		"created_at=" + now.Format(time.RFC3339Nano),
		"version=" + version.String(),
		"reason=" + reason,
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.txt"), []byte(strings.Join(meta, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	if rep, err := readStatusReport(); err == nil {
		if err := writeJSONFile(filepath.Join(dir, "status.json"), rep); err != nil {
			return "", err
		}
	} else {
		_ = os.WriteFile(filepath.Join(dir, "status-error.txt"), []byte(err.Error()+"\n"), 0o600)
	}
	doctor := collectClientDoctor(defaultConfigPath, defaultProbeTarget, 0, true)
	if err := writeJSONFile(filepath.Join(dir, "doctor.json"), doctor); err != nil {
		return "", err
	}
	for _, src := range install.ClientLogPaths() {
		if err := copyIfExists(src, filepath.Join(dir, filepath.Base(src))); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func autoArchiveAfterClientCommand(command string, commandErr *error, announce bool) {
	dir, err := archiveClientLogsWithReason(defaultLogArchiveRoot(), command)
	if err != nil {
		if announce || (commandErr != nil && *commandErr != nil) {
			fmt.Fprintf(os.Stderr, "Diagnostics archive failed: %v\n", err)
		}
		return
	}
	if err := pruneLogArchives(filepath.Dir(dir), defaultAutoArchiveLimit); err != nil && os.Getenv("BX_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "Diagnostics archive prune failed: %v\n", err)
	}
	if announce || (commandErr != nil && *commandErr != nil) {
		fmt.Fprintf(os.Stderr, "Diagnostics archived: %s\n", dir)
	}
}

func defaultLogArchiveRoot() string {
	if root := strings.TrimSpace(os.Getenv("BX_LOG_ARCHIVE_DIR")); root != "" {
		return root
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Logs/bx/diagnostics"
	case "linux":
		return "/var/log/bx/diagnostics"
	default:
		return filepath.Join(os.TempDir(), "bx-diagnostics")
	}
}

func pruneLogArchives(root string, keep int) error {
	if keep < 1 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "bx-logs-") {
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	if len(dirs) <= keep {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, dir := range dirs[keep:] {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONFile(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeJSON(f, v)
}

func copyIfExists(src, dst string) error {
	in, err := os.Open(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func printDNSStatus(st install.DNSStatus) {
	fmt.Printf("dns supported: %v\n", st.Supported)
	if st.Detail != "" {
		fmt.Printf("detail: %s\n", st.Detail)
	}
	if st.Service != "" {
		fmt.Printf("service: %s\n", st.Service)
	}
	if len(st.Servers) > 0 {
		fmt.Printf("servers: %s\n", strings.Join(st.Servers, ", "))
	}
	fmt.Printf("enabled: %v\n", st.Enabled)
	fmt.Printf("saved original: %v\n", st.StateSaved)
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

func writeServerConfig(path string, cfg serverConfig, force bool) error {
	if cfg.Listen == "" {
		return fmt.Errorf("listen 不能为空")
	}
	if cfg.Password == "" {
		return fmt.Errorf("password 不能为空")
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("server 配置已存在 %s(加 --force 覆盖)", path)
		}
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func readServerConfig(path string) (serverConfig, error) {
	var cfg serverConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读 server 配置 %s: %w", path, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("解析 server 配置: %w", err)
	}
	if cfg.Listen == "" || cfg.Password == "" {
		return cfg, fmt.Errorf("server 配置不完整")
	}
	return cfg, nil
}

func rotateServerConfig(path, password string) (serverConfig, error) {
	if password == "" {
		return serverConfig{}, fmt.Errorf("password 不能为空")
	}
	cfg, err := readServerConfig(path)
	if err != nil {
		return cfg, err
	}
	cfg.Password = password
	if err := writeServerConfig(path, cfg, true); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func bxServerLink(host string, cfg serverConfig) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("host 不能为空")
	}
	if strings.Contains(host, "://") {
		return "", fmt.Errorf("host 应只填公网地址或域名,不要带 scheme")
	}
	if h, p, err := net.SplitHostPort(host); err == nil && h != "" && p != "" {
		return "", fmt.Errorf("host 不要带端口;端口来自 server listen(%s)", cfg.Listen)
	}
	port := listenPort(cfg.Listen)
	if port == "" {
		return "", fmt.Errorf("无法从 listen=%q 推导端口", cfg.Listen)
	}
	target := net.JoinHostPort(strings.Trim(host, "[]"), port)
	raw := "brook://server?server=" + url.QueryEscape(target) + "&password=" + url.QueryEscape(cfg.Password)
	return blink.Encode(raw), nil
}

func listenPort(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		return port
	}
	if strings.HasPrefix(listen, ":") && len(listen) > 1 {
		return strings.TrimPrefix(listen, ":")
	}
	return ""
}

func serverFirewallHint(listen string) string {
	port := listenPort(listen)
	if port == "" {
		return ""
	}
	return fmt.Sprintf("如果 VPS 启用了防火墙,请确认已放行 TCP %s; ufw 可用: sudo ufw allow %s/tcp", port, port)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func openUFW(listen string) error {
	port := listenPort(listen)
	if port == "" {
		return fmt.Errorf("无法从 listen=%q 推导端口", listen)
	}
	cmd := exec.Command("ufw", "allow", port+"/tcp")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ufw allow %s/tcp: %w", port, err)
	}
	return nil
}

func cleanShareName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("share name 不能为空")
	}
	if len(name) > 48 {
		return "", fmt.Errorf("share name 太长")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return "", fmt.Errorf("share name 只能包含字母、数字、-、_")
	}
	return name, nil
}

func stringFlag(c *cli.Context, name string) string {
	if v := c.String(name); v != "" {
		return v
	}
	return stringFlagFromArgs(c.Args().Slice(), name)
}

func stringFlagFromArgs(args []string, name string) string {
	prefix := "--" + name + "="
	for i := 0; i < len(args); i++ {
		if args[i] == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(args[i], prefix) {
			return strings.TrimPrefix(args[i], prefix)
		}
	}
	return ""
}

func shareConfigPath(dir, name string) string {
	return filepath.Join(dir, name+".yaml")
}

func readShares(dir string) ([]shareInfo, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var shares []shareInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		cfg, err := readServerConfig(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		shares = append(shares, shareInfo{Name: name, Config: cfg})
	}
	sort.Slice(shares, func(i, j int) bool { return shares[i].Name < shares[j].Name })
	return shares, nil
}

func nextShareListen(dir string) (string, error) {
	used := map[string]bool{}
	if cfg, err := readServerConfig(defaultServerConfigPath); err == nil {
		if port := listenPort(cfg.Listen); port != "" {
			used[port] = true
		}
	}
	shares, err := readShares(dir)
	if err != nil {
		return "", err
	}
	for _, s := range shares {
		if port := listenPort(s.Config.Listen); port != "" {
			used[port] = true
		}
	}
	for port := 10000; port <= 10999; port++ {
		p := fmt.Sprint(port)
		if used[p] || isListening(p) {
			continue
		}
		return ":" + p, nil
	}
	return "", fmt.Errorf("没有可用 share 端口(10000-10999)")
}

func doctorLine(status, name, detail string) {
	fmt.Printf("[%s] %s: %s\n", strings.ToUpper(status), name, detail)
}

func boolStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "fail"
}

func serviceStatus(action, service string) string {
	return serviceStatusFromState(action, serviceState(action, service))
}

func serviceStatusFromState(action, state string) string {
	switch action {
	case "is-active":
		if state == "active" {
			return "ok"
		}
	case "is-enabled":
		if state == "enabled" {
			return "ok"
		}
	}
	if state == "unknown" {
		return "warn"
	}
	return "fail"
}

func hintForState(state, primary, logs string) string {
	if state == "active" {
		return primary
	}
	if primary == "" {
		return logs
	}
	return primary + "; " + logs
}

func checkFileMode(path string, want os.FileMode) {
	fi, err := os.Stat(path)
	if err != nil {
		doctorLine("warn", "config permissions", err.Error())
		return
	}
	got := fi.Mode().Perm()
	if got == want {
		doctorLine("ok", "config permissions", fmt.Sprintf("%#o", got))
		return
	}
	doctorLine("warn", "config permissions", fmt.Sprintf("%#o, want %#o", got, want))
}

func modeCheck(path string, want os.FileMode) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().Perm() == want
}

func checkStatusSocket() error {
	conn, err := net.DialTimeout("unix", supervisor.SockPath, 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

func waitStatusSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := checkStatusSocket(); err != nil {
			last = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		return nil
	}
	if last != nil {
		return last
	}
	return fmt.Errorf("timeout waiting for %s", supervisor.SockPath)
}

func redactLink(link string) string {
	switch {
	case strings.HasPrefix(link, "bx://"):
		return "bx://<redacted>"
	case strings.HasPrefix(link, "blink://"):
		return "blink://<legacy-redacted>"
	case strings.HasPrefix(link, "brook://"):
		return "internal-link:<redacted>"
	default:
		return "<redacted>"
	}
}

func isListening(port string) bool {
	for _, addr := range []string{net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("::1", port)} {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

func randomPassword() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成密码: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// buildExecStart 构造自洽的服务启动命令:只需绝对 bx 与绝对 config,其余走二进制内默认。
func buildExecStart(bin, configPath string) string {
	return buildExecStartForGOOS(runtime.GOOS, bin, configPath)
}

func buildExecStartForGOOS(goos, bin, configPath string) string {
	if goos == "darwin" {
		return fmt.Sprintf("%s run -c %s --listen-dns %s", bin, configPath, darwinDNSListen)
	}
	return fmt.Sprintf("%s run -c %s", bin, configPath)
}

func uninstallAction(c *cli.Context) error {
	if err := install.Uninstall(); err != nil {
		return err
	}
	fmt.Println("已卸载 bx 服务")
	return nil
}
