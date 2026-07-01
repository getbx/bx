package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
	"github.com/getbx/bx/internal/srvgen"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/tunnel"
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
			{Name: "webrtc-check", Usage: "检查 WebRTC 泄漏风险(只读)", Flags: webrtcCheckFlags(), Action: webrtcCheckAction},
			{Name: "capabilities", Usage: "输出机器可读能力清单", Action: capabilitiesAction},
			{Name: "up", Usage: "启动并设为开机自启", Action: upAction},
			{Name: "down", Usage: "停止并取消开机自启", Action: downAction},
			{Name: "kick", Usage: "强制立即重连隧道(不碰 TUN/路由,比 down+up 轻)", Action: kickAction},
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
			{Name: "blink", Usage: "把 brook/vless 链接换壳成 bx://(多个=容灾 bundle),避免裸凭据外泄", ArgsUsage: "<link> [link2 ...]", Action: linkAction},
			{Name: "darwin-plan", Usage: "打印 macOS 路由 dry-run 计划(不改网络)", Flags: darwinPlanFlags(), Action: darwinPlanAction},
			{Name: "router-plan", Usage: "打印 router 模式 dry-run 计划(ip + nft,不改网络)", Flags: routerPlanFlags(), Action: routerPlanAction},
			{Name: "uninstall", Usage: "卸载客户端服务", Action: uninstallAction},
		},
	}
}

type serverConfig struct {
	Type     string `yaml:"type,omitempty"`     // brook(默认/空) | reality | hysteria2
	Listen   string `yaml:"listen,omitempty"`   // brook:监听地址
	Password string `yaml:"password,omitempty"` // brook:连接密码
	SNI      string `yaml:"sni,omitempty"`      // reality/hysteria2:借用的真站
	Port     int    `yaml:"port,omitempty"`     // reality/hysteria2:监听端口(默认 443)
	Link     string `yaml:"link,omitempty"`     // reality/hysteria2:生成的客户端裸链接(host 已填)
	UDPLink  string `yaml:"udp_link,omitempty"` // reality+hysteria2 合体:hys2 链接(客户端作 udp.transport 按类分流)
}

// serverSingboxPath 是 reality/hysteria2 服务端的 sing-box 配置落盘路径(含私钥/证书,0600)。
// var(非 const)便于测试覆盖到 t.TempDir()。
var serverSingboxPath = "/var/lib/bx/sbserver.json"

// normalizeServerProtocol 校验并归一服务端协议(空→brook)。
func normalizeServerProtocol(p string) (string, error) {
	switch p {
	case "", "brook":
		return "brook", nil
	case "reality", "hysteria2":
		return p, nil
	default:
		return "", fmt.Errorf("不支持的 server 协议 %q(支持 brook/reality/hysteria2)", p)
	}
}

// serverConfigComplete 报告配置是否自洽(brook 需 listen+password;reality/hys2 需 link)。
func serverConfigComplete(cfg serverConfig) error {
	switch t, _ := normalizeServerProtocol(cfg.Type); t {
	case "reality", "hysteria2":
		if cfg.Link == "" {
			return fmt.Errorf("%s server 配置缺 link", t)
		}
	default: // brook
		if cfg.Listen == "" || cfg.Password == "" {
			return fmt.Errorf("brook server 配置缺 listen/password")
		}
	}
	return nil
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

type webrtcCheckReport struct {
	OK                          bool          `json:"ok"`
	Kind                        string        `json:"kind"`
	Version                     string        `json:"version"`
	SecretsRedacted             bool          `json:"secrets_redacted"`
	ChangesSystem               bool          `json:"changes_system"`
	ChangesNetwork              bool          `json:"changes_network"`
	RequiresRoot                bool          `json:"requires_root"`
	Risk                        string        `json:"risk"`
	LeakProof                   string        `json:"leak_proof"`
	BrowserVerificationRequired bool          `json:"browser_verification_required"`
	Checks                      []checkReport `json:"checks"`
	Evidence                    []string      `json:"evidence,omitempty"`
	NextActions                 []string      `json:"next_actions,omitempty"`
}

type browserICEResult struct {
	UserAgent  string   `json:"user_agent,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	IPs        []string `json:"ips,omitempty"`
	Errors     []string `json:"errors,omitempty"`
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
		{Name: "up", Usage: "一键装好(默认 reality+hys2、自动探测公网IP)并启动", Flags: serverInstallFlags(), Action: serverUpAction},
		{Name: "down", Usage: "停止并取消开机自启", Action: serverDownAction},
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

func webrtcCheckFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出 agent 可读 JSON"},
		&cli.BoolFlag{Name: "browser", Usage: "打开本地浏览器页面,实际收集 WebRTC ICE candidates"},
		&cli.DurationFlag{Name: "browser-timeout", Value: 20 * time.Second, Usage: "等待浏览器 ICE 结果的最长时间"},
		&cli.StringSliceFlag{Name: "expected-ip", Usage: "允许出现的代理/VPS 公网 IP(可重复)"},
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
		&cli.StringFlag{Name: "dns-service", Usage: "macOS 网络服务名(留空自动探测)"},
	}
}

func serverInstallFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置写入路径"},
		&cli.StringFlag{Name: "protocol", Value: "reality", Usage: "协议:reality(默认,强封锁首选)| hysteria2(速度档)| brook(简单兜底)"},
		&cli.StringFlag{Name: "sni", Usage: "reality/hysteria2 借用的真站(默认 www.cloudflare.com;别用 microsoft 证书过大)"},
		&cli.IntFlag{Name: "port", Usage: "reality/hysteria2 监听端口(默认 443,最自然;被占/受限才换)"},
		&cli.BoolFlag{Name: "tcp-only", Usage: "reality 只开 TCP,不附带 hysteria2 UDP 加速(默认附带,既安全又有速度)"},
		&cli.StringFlag{Name: "listen", Value: ":9999", Usage: "brook 监听地址"},
		&cli.StringFlag{Name: "password", Usage: "brook 连接密码(留空自动生成)"},
		&cli.StringFlag{Name: "host", Usage: "公网地址或域名(留空自动探测公网 IP)"},
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

// generateServerConfig 是 buildServerConfig 的纯核心(不依赖 cli.Context、不碰文件):
// 按协议产出 serverConfig + (reality/hysteria2 的)sing-box 服务端配置字节。便于单测。
// port<=0 → reality/hysteria2 用 443。
func generateServerConfig(proto, host, sni, listen, password string, port int, withHysteria2 bool) (serverConfig, []byte, error) {
	p, err := normalizeServerProtocol(proto)
	if err != nil {
		return serverConfig{}, nil, err
	}
	if withHysteria2 && p != "reality" {
		return serverConfig{}, nil, fmt.Errorf("--with-hysteria2 只能配 --protocol reality(主 TCP)用")
	}
	host = strings.TrimSpace(host)
	switch p {
	case "reality":
		if host == "" {
			return serverConfig{}, nil, fmt.Errorf("reality 需要 --host <公网IP或域名>(链接生成时要用)")
		}
		rp, err := srvgen.GenerateReality(host, sni, port)
		if err != nil {
			return serverConfig{}, nil, err
		}
		// reality + hysteria2 合体:一台 server 同供隐蔽 TCP + 加速 UDP,客户端按类分流。
		if withHysteria2 {
			hp, err := srvgen.GenerateHysteria2(host, sni, port)
			if err != nil {
				return serverConfig{}, nil, err
			}
			sb, err := srvgen.CombinedServerConfig(rp, hp)
			if err != nil {
				return serverConfig{}, nil, err
			}
			return serverConfig{Type: "reality", SNI: rp.SNI, Port: rp.Port, Link: rp.ClientLink(), UDPLink: hp.ClientLink()}, sb, nil
		}
		sb, err := rp.ServerConfig()
		if err != nil {
			return serverConfig{}, nil, err
		}
		return serverConfig{Type: "reality", SNI: rp.SNI, Port: rp.Port, Link: rp.ClientLink()}, sb, nil
	case "hysteria2":
		if host == "" {
			return serverConfig{}, nil, fmt.Errorf("hysteria2 需要 --host <公网IP或域名>(链接生成时要用)")
		}
		hp, err := srvgen.GenerateHysteria2(host, sni, port)
		if err != nil {
			return serverConfig{}, nil, err
		}
		sb, err := hp.ServerConfig()
		if err != nil {
			return serverConfig{}, nil, err
		}
		return serverConfig{Type: "hysteria2", SNI: hp.SNI, Port: hp.Port, Link: hp.ClientLink()}, sb, nil
	default: // brook
		if password == "" {
			if password, err = randomPassword(); err != nil {
				return serverConfig{}, nil, err
			}
		}
		return serverConfig{Type: "brook", Listen: listen, Password: password}, nil, nil
	}
}

// buildServerConfig 按 --protocol 生成 serverConfig;reality/hysteria2 还会把含私钥/证书的
// sing-box 服务端配置落盘到 serverSingboxPath(0600)。返回的 cfg 写进 server.yaml。
func buildServerConfig(c *cli.Context) (serverConfig, error) {
	proto, _ := normalizeServerProtocol(c.String("protocol"))
	host := strings.TrimSpace(c.String("host"))
	// 缺 --host 自动探测公网 IP(reality/hys2 需要它生成链接)——让 server 端也"零配置"。
	if host == "" && (proto == "reality" || proto == "hysteria2") {
		if ip := detectPublicIP(); ip != "" {
			host = ip
			fmt.Fprintf(os.Stderr, "自动用探测到的公网 IP:%s(不对请 --host 指定)\n", ip)
		}
	}
	// reality 默认附带 hysteria2(UDP 加速),--tcp-only 关掉。
	withHys2 := proto == "reality" && !c.Bool("tcp-only")
	cfg, sb, err := generateServerConfig(c.String("protocol"), host, c.String("sni"), c.String("listen"), c.String("password"), c.Int("port"), withHys2)
	if err != nil {
		return serverConfig{}, err
	}
	if sb != nil {
		if err := writeServerSingbox(sb, c.Bool("force")); err != nil {
			return serverConfig{}, err
		}
	}
	return cfg, nil
}

// writeServerSingbox 把 reality/hysteria2 的 sing-box 服务端配置落盘(含私钥/证书,0600)。
func writeServerSingbox(b []byte, force bool) error {
	if !force {
		if _, err := os.Stat(serverSingboxPath); err == nil {
			return fmt.Errorf("%s 已存在(加 --force 覆盖)", serverSingboxPath)
		}
	}
	if err := os.MkdirAll(filepath.Dir(serverSingboxPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(serverSingboxPath, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(serverSingboxPath, 0o600)
}

func serverInstallAction(c *cli.Context) error {
	proto, _ := normalizeServerProtocol(c.String("protocol"))
	// 重装防呆:reality/hys2 重装(--force)会重生成密钥/UUID,已发出的客户端链接全失效。
	if (proto == "reality" || proto == "hysteria2") && c.Bool("force") {
		if _, e := os.Stat(serverSingboxPath); e == nil {
			fmt.Fprintln(os.Stderr, "⚠ 重装(--force)会重新生成密钥/UUID——所有已发出的客户端链接将失效;\n   装完用 `bx server link`(或 `bx server share`)重新分发。")
		}
	}
	// 缺 --host 时,best-effort 探测本机公网 IP 给个建议(不擅自用,避免探到错 IP)。
	if (proto == "reality" || proto == "hysteria2") && strings.TrimSpace(c.String("host")) == "" {
		if ip := detectPublicIP(); ip != "" {
			fmt.Fprintf(os.Stderr, "提示:本机公网 IP 可能是 %s,若正确请: --host %s\n", ip, ip)
		}
	}
	cfg, err := buildServerConfig(c)
	if err != nil {
		return err
	}
	// reality 借壳 SNI 适配性检查(TLS1.3+X25519 + 证书链不过大):过大会静默挂握手,
	// 装机时就当场警告(best-effort,网络问题也不阻断),省得用户事后踩坑。
	if cfg.Type == "reality" {
		for _, w := range srvgen.CheckRealitySNI(cfg.SNI) {
			fmt.Fprintln(os.Stderr, w)
		}
	}
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
	fmt.Printf("✅ bx server 已安装(协议 %s)。下一步:sudo bx server start\n", cfg.Type)
	if hint := serverFirewallHintFor(cfg); hint != "" {
		fmt.Println(hint)
	}
	// reality/hysteria2:链接已在生成时含 host,直接给(换壳成 bx://)。
	if cfg.Type == "reality" || cfg.Type == "hysteria2" {
		printClientSetup(cfg)
		return nil
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

// detectPublicIP best-effort 探测本机公网 IPv4(短超时,失败返回 "")。
// 强制 tcp4 拨号:很多 VPS 偏好 IPv6 出站,不强制会探到 v6 地址,而客户端链接通常要 v4 host。
func detectPublicIP() string {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", addr)
	}}
	cl := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	for _, u := range []string{"https://api.ipify.org", "https://icanhazip.com"} {
		resp, err := cl.Get(u)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		ip := strings.TrimSpace(string(b))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// uuidFromVlessLink 取 vless://<uuid>@… 里的 uuid(非 vless 返回 "")。
func uuidFromVlessLink(link string) string {
	const p = "vless://"
	if !strings.HasPrefix(link, p) {
		return ""
	}
	rest := link[len(p):]
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		return rest[:at]
	}
	return ""
}

// swapVlessUUID 把 vless 链接里的 uuid 换成 newUUID(其余部分不动)。用于给多用户 share 派新链接。
func swapVlessUUID(link, newUUID string) string {
	const p = "vless://"
	if !strings.HasPrefix(link, p) {
		return link
	}
	rest := link[len(p):]
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		return p + newUUID + rest[at:]
	}
	return link
}

// printClientSetup 打印 reality/hysteria2(及 reality+hys2 合体)的客户端接入信息。
// 合体时给一条「按类分流」的 bx setup 命令(主 reality TCP + udp.transport 走 hys2)。
func printClientSetup(cfg serverConfig) {
	main := blink.Encode(cfg.Link)
	if cfg.UDPLink != "" {
		fmt.Println("🔀 reality(TCP/隐蔽)+ hysteria2(UDP/加速)就绪。客户端一条命令配齐(按类分流,既安全又有速度):")
		fmt.Printf("  sudo bx setup %s --udp %s\n", main, blink.Encode(cfg.UDPLink))
		return
	}
	fmt.Println(main)
}

func serverLinkAction(c *cli.Context) error {
	cfg, err := readServerConfig(c.String("config"))
	if err != nil {
		return err
	}
	// reality/hysteria2:链接已在安装时生成(含 host),直接换壳输出,无需 --host。
	if cfg.Type == "reality" || cfg.Type == "hysteria2" {
		printClientSetup(cfg)
		return nil
	}
	host := c.String("host")
	if host == "" {
		return fmt.Errorf("用法: sudo bx server link --host <VPS_IP或域名>")
	}
	link, err := bxServerLink(host, cfg)
	if err != nil {
		return err
	}
	fmt.Println(link)
	return nil
}

// realityShare 给 reality 主 server 加一个用户(uuid),重启生效,出新用户链接 + 记录到 share 文件。
// reality 多用户 = 一个 inbound 多 uuid(不同于 brook 的每用户一端口一服务)。
func realityShare(name, dir string, mainCfg serverConfig) (serverConfig, error) {
	newUUID, err := srvgen.NewUUID()
	if err != nil {
		return serverConfig{}, err
	}
	sb, err := os.ReadFile(serverSingboxPath)
	if err != nil {
		return serverConfig{}, fmt.Errorf("读 server sing-box 配置: %w", err)
	}
	sb2, err := srvgen.AddRealityUser(sb, newUUID)
	if err != nil {
		return serverConfig{}, err
	}
	if err := os.WriteFile(serverSingboxPath, sb2, 0o600); err != nil {
		return serverConfig{}, err
	}
	rec := serverConfig{Type: "reality", SNI: mainCfg.SNI, Port: mainCfg.Port,
		Link: swapVlessUUID(mainCfg.Link, newUUID), UDPLink: mainCfg.UDPLink}
	if err := writeServerConfig(shareConfigPath(dir, name), rec, true); err != nil {
		return serverConfig{}, err
	}
	if err := install.RestartServer(); err != nil {
		return rec, fmt.Errorf("用户已加并落盘,但重启 server 失败(下次启动生效): %w", err)
	}
	return rec, nil
}

func serverShareAction(c *cli.Context) error {
	name, err := cleanShareName(c.Args().First())
	if err != nil {
		return err
	}
	dir := stringFlag(c, "dir")
	// 主 server 是 reality → 多用户走「加 uuid」;hys2 暂不支持多用户;其余(brook)走多端口 share。
	if mainCfg, merr := readServerConfig(defaultServerConfigPath); merr == nil {
		switch mainCfg.Type {
		case "reality":
			rec, err := realityShare(name, dir, mainCfg)
			if err != nil {
				return err
			}
			fmt.Printf("✅ reality share %s 已创建(主 server 加了一个用户并重启生效)。\n", name)
			printClientSetup(rec)
			return nil
		case "hysteria2":
			return fmt.Errorf("hysteria2 主 server 暂不支持多用户 share;reality(默认附带 hys2)支持")
		}
	}
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
	fmt.Println("NAME\tLISTEN/类型\tSTATUS")
	for _, s := range shares {
		if s.Config.Type == "reality" {
			fmt.Printf("%s\treality\t主 server 内一用户\n", s.Name)
			continue
		}
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
	// reality share:从主 server 删该用户 uuid、重启、删记录(不是独立服务)。
	if shareCfg, err := readServerConfig(shareConfigPath(dir, name)); err == nil && shareCfg.Type == "reality" {
		uuid := uuidFromVlessLink(shareCfg.Link)
		if uuid == "" {
			return fmt.Errorf("share 记录里没有有效 uuid")
		}
		sb, err := os.ReadFile(serverSingboxPath)
		if err != nil {
			return fmt.Errorf("读 server sing-box 配置: %w", err)
		}
		sb2, err := srvgen.RemoveRealityUser(sb, uuid)
		if err != nil {
			return err
		}
		if err := os.WriteFile(serverSingboxPath, sb2, 0o600); err != nil {
			return err
		}
		// 先删 share 记录(配置已落盘),再重启——这样即便重启失败,记录与配置仍一致、可重试,
		// 不会留下一条「config 已删 uuid 但记录还在」的不可撤销僵尸。
		if err := os.Remove(shareConfigPath(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := install.RestartServer(); err != nil {
			return fmt.Errorf("撤销已落盘,但重启 server 失败(下次启动生效): %w", err)
		}
		return nil
	}
	// brook share:每用户独立服务,卸单元 + 删配置。
	if err := install.UninstallShare(name); err != nil {
		return err
	}
	if err := os.Remove(shareConfigPath(dir, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func serverRotateAction(c *cli.Context) error {
	// reality/hys2 没有"换密码"语义——轮换=重生成密钥,等价于带 --force 重装。导到正确命令,
	// 避免给它们套 brook 的密码轮换(无意义且会在生成 brook 链接时出错)。
	if cfg, err := readServerConfig(c.String("config")); err == nil && (cfg.Type == "reality" || cfg.Type == "hysteria2") {
		host := "<VPS_IP或域名>"
		if h := serverHostFromLink(cfg.Link); h != "" {
			host = h
		}
		return fmt.Errorf("%s 轮换密钥请用:sudo bx server install --protocol %s --host %s --force\n(会重生成密钥/UUID,主链接 + 所有 share 链接全失效,需重新分发)", cfg.Type, cfg.Type, host)
	}
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

// serverUpAction 一键:没装过就用好默认装一遍(reality+hys2、自动探测公网 IP),然后启动。
// 让 server 端像客户端 bx up/down/status 一样简单。
func serverUpAction(c *cli.Context) error {
	if install.ServerUnitInstalled() {
		fmt.Println("bx server 已安装,直接启动(要换协议/重生成密钥:sudo bx server install --force)。")
	} else if err := serverInstallAction(c); err != nil {
		return err
	}
	if err := install.EnableServer(); err != nil {
		return err
	}
	fmt.Println("✅ bx server 已启动并开机自启。看状态:bx server status;停:sudo bx server down")
	return nil
}

// serverDownAction = 停止(与 client 的 bx down 对称)。
func serverDownAction(c *cli.Context) error { return serverStopAction(c) }

// serverStatusSummary 给协议/端口/SNI/用户数的可读摘要(纯函数,可测)。
func serverStatusSummary(cfg serverConfig, shareCount int) string {
	proto, _ := normalizeServerProtocol(cfg.Type)
	var b strings.Builder
	switch proto {
	case "reality", "hysteria2":
		port := cfg.Port
		if port <= 0 {
			port = 443
		}
		fmt.Fprintf(&b, "协议: %s", proto)
		if cfg.UDPLink != "" {
			b.WriteString(" + hysteria2(UDP 加速,按类分流)")
		}
		fmt.Fprintf(&b, "\n端口: %d  借用 SNI: %s", port, cfg.SNI)
	default:
		fmt.Fprintf(&b, "协议: brook  监听: %s", cfg.Listen)
	}
	if shareCount > 0 {
		fmt.Fprintf(&b, "\n用户/分享: %d", shareCount)
	}
	return b.String()
}

func serverStatusAction(c *cli.Context) error {
	active := serviceState("is-active", install.ServerServiceName)
	enabled := serviceState("is-enabled", install.ServerServiceName)
	fmt.Printf("bx server: %s, boot: %s\n", active, enabled)
	if cfg, err := readServerConfig(defaultServerConfigPath); err == nil {
		shareCount := 0
		if shares, serr := readShares(defaultShareDir); serr == nil {
			shareCount = len(shares)
		}
		fmt.Println(serverStatusSummary(cfg, shareCount))
	}
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
	// 卸载即清秘密:sbserver.json(reality 私钥/hys2 自签证书)、server.yaml(hys2 密码/obfs 密码
	// 在 link 里)、shares 下每份(brook 密码 / reality 用户链接)。
	rm := func(p string) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: 删除 %s 失败: %v\n", p, err)
		}
	}
	rm(serverSingboxPath)
	rm(defaultServerConfigPath)
	if entries, err := os.ReadDir(defaultShareDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
				rm(filepath.Join(defaultShareDir, e.Name()))
			}
		}
	}
	fmt.Println("已卸载 bx server 服务(配置与秘密已清除)")
	return nil
}

func serveAction(c *cli.Context) error {
	cfg, err := readServerConfig(c.String("config"))
	if err != nil {
		return err
	}
	// reality/hysteria2:跑内嵌 sing-box(配置含私钥/证书,安装时已落盘 serverSingboxPath)。
	if cfg.Type == "reality" || cfg.Type == "hysteria2" {
		sbPath, err := provision.EnsureSingbox("/var/lib/bx", "", embedded.Singbox(), embedded.SingboxVersion(), "", "")
		if err != nil {
			return fmt.Errorf("准备 sing-box: %w", err)
		}
		cmd := exec.CommandContext(c.Context, sbPath, "run", "-c", serverSingboxPath)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
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
			} else {
				// cfg.Server 经 Parse 已校验解码;不再 blink.Decode 重校验(裸 vless/hysteria2 会误报)。
				doctorLine("ok", "server link", redactLink(cfg.Server))
				if len(cfg.Transports) > 1 {
					doctorLine("ok", "transports", fmt.Sprintf("%d 个(自动容灾)", len(cfg.Transports)))
				}
				if cfg.UDP.Transport != "" {
					doctorLine("ok", "udp transport", redactLink(cfg.UDP.Transport))
				}
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

func webrtcCheckAction(c *cli.Context) error {
	rep := collectWebRTCCheck(c.String("config"), c.String("dns-service"))
	if c.Bool("browser") {
		expected := append([]string{}, c.StringSlice("expected-ip")...)
		expected = appendUnique(expected, expectedWebRTCIPs(c.String("config"))...)
		ice, err := runBrowserICECheck(c.Context, c.Duration("browser-timeout"))
		if err != nil {
			rep.addCheck("browser_ice", "fail", err.Error(), "")
			rep.Risk = maxRisk(rep.Risk, "high")
			rep.OK = false
		} else {
			applyBrowserICECandidates(&rep, ice, expected)
		}
	}
	if c.Bool("json") {
		return writeJSON(os.Stdout, rep)
	}
	fmt.Println("WebRTC 检查")
	fmt.Printf("  风险    %s\n", rep.Risk)
	for _, check := range rep.Checks {
		if check.Name == "udp_path" || check.Name == "dns" || check.Name == "service" {
			doctorLine(check.Status, check.Name, check.Detail)
		}
	}
	fmt.Printf("  结论    %s\n", rep.LeakProof)
	if rep.BrowserVerificationRequired {
		fmt.Println("  限制    需要浏览器 ICE candidate 才能证明是否真实泄漏")
	}
	for _, action := range rep.NextActions {
		fmt.Printf("  下一步  %s\n", action)
	}
	return nil
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
		proto, _ := normalizeServerProtocol(cfg.Type)
		doctorLine("ok", "protocol", proto)
		if proto == "reality" || proto == "hysteria2" {
			// reality/hysteria2:监听在 sing-box 配置里,检查配置落盘 + 端口真在听 + SNI 适配。
			if _, serr := os.Stat(serverSingboxPath); serr != nil {
				doctorLine("fail", "singbox config", serr.Error())
			} else {
				doctorLine("ok", "singbox config", serverSingboxPath)
				checkFileMode(serverSingboxPath, 0o600)
			}
			sport := cfg.Port
			if sport <= 0 {
				sport = 443
			}
			portStr := fmt.Sprintf("%d", sport)
			if proto == "reality" { // reality=TCP,可探;hys2=UDP,isListening 探不到 → 跳过
				if isListening(portStr) {
					doctorLine("ok", "port listening", "tcp/"+portStr)
				} else {
					doctorLine("warn", "port listening", "tcp/"+portStr+" 未在听(server 没起?bx server start)")
				}
				for _, w := range srvgen.CheckRealitySNI(cfg.SNI) {
					doctorLine("warn", "reality sni", w)
				}
			}
			if hint := serverFirewallHintFor(cfg); hint != "" {
				doctorLine("hint", "firewall", hint)
			}
		} else if port := listenPort(cfg.Listen); port == "" {
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
				Command:        "bx webrtc-check --json",
				Category:       "diagnostics",
				Summary:        "Assess WebRTC leak risk from bx config, service status, DNS state, and UDP policy.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--browser", "--browser-timeout <duration>", "--expected-ip <ip>", "--config <path>", "--dns-service <name>"},
				Examples:       []string{"bx webrtc-check --json", "bx webrtc-check --browser --json --expected-ip <proxy-ip>"},
				SafeNotes:      []string{"Read-only for system settings.", "Secrets are redacted.", "--browser opens a local 127.0.0.1 test page and collects browser ICE candidates; this is the command that can prove a WebRTC public-IP leak."},
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
			} else {
				// cfg.Server 经 config.Parse 已校验并解码成裸内部链接(brook/vless/hysteria2);
				// 不再 blink.Decode 重校验(对非 bx:// 的裸 vless/hysteria2 会误报 fail)。
				rep.addCheck("server_link", "ok", redactLink(cfg.Server), "")
				if len(cfg.Transports) > 1 {
					rep.addCheck("transports", "ok", fmt.Sprintf("%d 个传输(自动容灾)", len(cfg.Transports)), "")
				}
				if cfg.UDP.Transport != "" {
					rep.addCheck("udp_transport", "ok", redactLink(cfg.UDP.Transport), "")
				}
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

func collectWebRTCCheck(configPath, dnsService string) webrtcCheckReport {
	cfg, cfgErr := loadConfig(configPath)
	status, statusErr := readStatusReport()
	dnsStatus, dnsErr := install.InspectDNS(dnsService)
	if runtime.GOOS != "darwin" {
		dnsErr = nil
	}
	rep := assessWebRTCCheck(cfg, statusPtr(status, statusErr), statusErr, dnsStatus, dnsErr)
	if cfgErr != nil {
		updateCheck(&rep, "config", "fail", cfgErr.Error(), "sudo bx setup <client-link>")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx setup <client-link>")
		rep.OK = false
	}
	return rep
}

func statusPtr(rep stats.Report, err error) *stats.Report {
	if err != nil {
		return nil
	}
	return &rep
}

func assessWebRTCCheck(cfg *config.Config, status *stats.Report, statusErr error, dnsStatus install.DNSStatus, dnsErr error) webrtcCheckReport {
	rep := webrtcCheckReport{
		Kind:                        "webrtc",
		Version:                     version.String(),
		SecretsRedacted:             true,
		Risk:                        "low",
		LeakProof:                   "not_proven",
		BrowserVerificationRequired: true,
	}
	if cfg == nil {
		rep.addCheck("config", "fail", "missing or unreadable", "sudo bx setup <client-link>")
		rep.Risk = "high"
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx setup <client-link>")
	} else {
		rep.addCheck("config", "ok", "readable", "")
	}

	if statusErr != nil {
		rep.addCheck("service", "fail", statusErr.Error(), "sudo bx up")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx up", "bx logs")
	} else if status == nil {
		rep.addCheck("service", "fail", "status unavailable", "sudo bx up")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx up")
	} else if !status.TunnelHealthy {
		rep.addCheck("service", "fail", "tunnel unhealthy", "bx logs")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.NextActions = appendUnique(rep.NextActions, "bx logs")
	} else {
		rep.addCheck("service", "ok", fmt.Sprintf("active %dms", status.LatencyMS), "")
		rep.Evidence = append(rep.Evidence, "status_socket: tunnel healthy")
	}

	mode := ""
	transport := ""
	if cfg != nil {
		mode = cfg.UDP.Mode
		transport = cfg.UDP.Transport
	}
	if status != nil && status.UDPMode != "" {
		mode = status.UDPMode
	}
	if status != nil && status.UDPTransport != "" {
		transport = status.UDPTransport
	}
	switch mode {
	case "proxy":
		detail := "non-DNS UDP relayed through bx tunnel"
		statusName := "ok"
		if status == nil {
			detail = "configured for UDP relay, but runtime status is unavailable"
			statusName = "warn"
			rep.Risk = maxRisk(rep.Risk, "high")
		}
		if transport != "" {
			detail += " via " + redactLink(transport)
		}
		rep.addCheck("udp_path", statusName, detail, "")
		rep.Evidence = append(rep.Evidence, "udp_mode: proxy")
	case "direct-realtime":
		rep.addCheck("udp_path", "fail", "non-DNS UDP uses local real network path", "sudo bx realtime on")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx realtime on")
	case "":
		rep.addCheck("udp_path", "warn", "UDP policy unknown; config and runtime status are unavailable", "sudo bx up")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx up")
	default:
		rep.addCheck("udp_path", "warn", "non-DNS UDP blocked; WebRTC may fail but should not leak by UDP", "sudo bx realtime on")
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx realtime on")
	}

	if dnsErr != nil {
		rep.addCheck("dns", "warn", dnsErr.Error(), "sudo bx dns on")
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx dns on")
	} else if !dnsStatus.Supported {
		rep.addCheck("dns", "info", dnsStatus.Detail, "")
	} else if dnsStatus.Enabled {
		rep.addCheck("dns", "ok", "system DNS -> 127.0.0.1", "")
		rep.Evidence = append(rep.Evidence, "dns: system DNS uses bx")
	} else {
		rep.addCheck("dns", "warn", "system DNS is not using bx", "sudo bx dns on")
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.NextActions = appendUnique(rep.NextActions, "sudo bx dns on")
	}

	if status != nil {
		if status.UDPBlocked > 0 {
			rep.addCheck("udp_recent_blocks", "warn", fmt.Sprintf("%d blocked", status.UDPBlocked), "bx logs")
			if mode == "proxy" {
				rep.Risk = maxRisk(rep.Risk, "medium")
			}
			rep.NextActions = appendUnique(rep.NextActions, "bx logs")
		} else {
			rep.addCheck("udp_recent_blocks", "ok", "0 blocked", "")
		}
	}
	rep.addCheck("browser_candidates", "info", "not inspected by this command", "open a WebRTC leak page and compare ICE candidates with this JSON")
	rep.NextActions = appendUnique(rep.NextActions, "bx webrtc-check --json")
	rep.OK = rep.Risk == "low"
	return rep
}

func (r *webrtcCheckReport) addCheck(name, status, detail, hint string) {
	r.Checks = append(r.Checks, checkReport{Name: name, Status: status, Detail: detail, Hint: hint})
}

func applyBrowserICECandidates(rep *webrtcCheckReport, result browserICEResult, expected []string) {
	rep.BrowserVerificationRequired = false
	updateCheck(rep, "browser_candidates", "ok", "inspected by browser ICE test", "")
	if result.UserAgent != "" {
		rep.Evidence = append(rep.Evidence, "browser_user_agent: "+result.UserAgent)
	}
	if len(result.Errors) > 0 {
		rep.addCheck("browser_ice", "warn", strings.Join(result.Errors, "; "), "")
		rep.Risk = maxRisk(rep.Risk, "medium")
	}
	ips := uniqueStrings(append(result.IPs, extractCandidateIPs(result.Candidates...)...))
	if len(result.Candidates) == 0 && len(ips) == 0 {
		rep.addCheck("browser_ice", "warn", "no ICE candidates returned", "try again with the target browser open")
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.LeakProof = "not_proven"
		rep.OK = false
		return
	}
	rep.addCheck("browser_ice", "ok", fmt.Sprintf("%d candidates, %d IPs", len(result.Candidates), len(ips)), "")
	expectedSet := stringSet(expected)
	var publicLeaks, expectedPublic, privateIPs []string
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if isIgnoredCandidateIP(parsed) {
			continue
		}
		if isPrivateCandidateIP(parsed) {
			privateIPs = append(privateIPs, ip)
			continue
		}
		if expectedSet[ip] {
			expectedPublic = append(expectedPublic, ip)
		} else {
			publicLeaks = append(publicLeaks, ip)
		}
	}
	if len(publicLeaks) > 0 {
		rep.addCheck("browser_public_ip", "fail", strings.Join(publicLeaks, ", "), "real public IP exposed in WebRTC ICE candidates")
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.LeakProof = "leaked"
		rep.OK = false
		return
	}
	if len(privateIPs) > 0 {
		rep.addCheck("browser_local_ip", "warn", strings.Join(privateIPs, ", "), "browser exposed local network candidates")
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.LeakProof = "local_network_candidate_detected"
		rep.OK = false
		return
	}
	detail := "no unexpected public IP in browser ICE candidates"
	if len(expectedPublic) > 0 {
		detail += "; expected public IP: " + strings.Join(expectedPublic, ", ")
	}
	rep.addCheck("browser_public_ip", "ok", detail, "")
	rep.LeakProof = "no_public_leak_detected"
	rep.OK = rep.Risk == "low"
}

func extractCandidateIPs(candidates ...string) []string {
	var out []string
	for _, candidate := range candidates {
		for _, field := range strings.Fields(candidate) {
			field = strings.Trim(field, "[](),;")
			ip := net.ParseIP(field)
			if ip == nil {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return uniqueStrings(out)
}

func isPrivateCandidateIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

func isIgnoredCandidateIP(ip net.IP) bool {
	return ip.IsUnspecified() || ip.IsMulticast()
}

func stringSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, v := range values {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			set[ip.String()] = true
			continue
		}
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			set[trimmed] = true
		}
	}
	return set
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func runBrowserICECheck(ctx context.Context, timeout time.Duration) (browserICEResult, error) {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resultCh := make(chan browserICEResult, 1)
	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, webrtcCheckHTML)
	})
	mux.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var result browserICEResult
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&result); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case resultCh <- result:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return browserICEResult{}, err
	}
	defer ln.Close()
	go func() {
		_ = srv.Serve(ln)
	}()
	defer srv.Close()
	u := "http://" + ln.Addr().String() + "/"
	if err := openBrowserURL(ctx, u); err != nil {
		return browserICEResult{}, err
	}
	select {
	case result := <-resultCh:
		return result, nil
	case <-ctx.Done():
		return browserICEResult{}, fmt.Errorf("browser ICE check timed out after %s; opened %s", timeout, u)
	}
}

func openBrowserURL(ctx context.Context, u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", u)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", u)
	default:
		if path, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.CommandContext(ctx, path, u)
		} else {
			return fmt.Errorf("no browser opener found; open manually: %s", u)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open browser: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

const webrtcCheckHTML = `<!doctype html>
<meta charset="utf-8">
<title>bx WebRTC check</title>
<body style="font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;margin:40px;line-height:1.45">
<h1>bx WebRTC check</h1>
<p>Running local ICE candidate test. You can close this tab after bx returns.</p>
<pre id="out">starting...</pre>
<script>
(async function(){
  const out = document.getElementById('out');
  const result = { user_agent: navigator.userAgent, candidates: [], ips: [], errors: [] };
  const ipRe = /(?:^|[\s])((?:\d{1,3}\.){3}\d{1,3}|[a-f0-9:]{2,})(?:[\s]|$)/ig;
  function addCandidate(c) {
    if (!c) return;
    result.candidates.push(c);
    let m;
    while ((m = ipRe.exec(c)) !== null) {
      if (!result.ips.includes(m[1])) result.ips.push(m[1]);
    }
  }
  try {
    const pc = new RTCPeerConnection({
      iceServers: [
        {urls: 'stun:stun.l.google.com:19302'},
        {urls: 'stun:stun.cloudflare.com:3478'}
      ]
    });
    pc.createDataChannel('bx');
    pc.onicecandidate = ev => {
      if (ev.candidate) addCandidate(ev.candidate.candidate);
    };
    const offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    await new Promise(resolve => {
      const started = Date.now();
      const timer = setInterval(() => {
        if (pc.iceGatheringState === 'complete' || Date.now() - started > 9000) {
          clearInterval(timer);
          resolve();
        }
      }, 150);
    });
    if (pc.localDescription && pc.localDescription.sdp) {
      pc.localDescription.sdp.split('\n').filter(l => l.startsWith('a=candidate:')).forEach(addCandidate);
    }
    pc.close();
  } catch (e) {
    result.errors.push(String(e && e.message ? e.message : e));
  }
  out.textContent = JSON.stringify(result, null, 2);
  try {
    await fetch('/result', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(result)});
    out.textContent += '\n\nsent to bx';
  } catch (e) {
    out.textContent += '\n\nsend failed: ' + e;
  }
})();
</script>
</body>`

func expectedWebRTCIPs(configPath string) []string {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil
	}
	var hosts []string
	for _, link := range cfg.Transports {
		hosts = append(hosts, hostFromClientLink(link))
	}
	hosts = append(hosts, hostFromClientLink(cfg.UDP.Transport))
	return uniqueStrings(hosts)
}

func hostFromClientLink(link string) string {
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return host
	}
	return ""
}

func updateCheck(r *webrtcCheckReport, name, status, detail, hint string) {
	for i := range r.Checks {
		if r.Checks[i].Name == name {
			r.Checks[i] = checkReport{Name: name, Status: status, Detail: detail, Hint: hint}
			return
		}
	}
	r.addCheck(name, status, detail, hint)
}

func maxRisk(a, b string) string {
	rank := map[string]int{"low": 0, "medium": 1, "high": 2}
	if rank[b] > rank[a] {
		return b
	}
	if _, ok := rank[a]; !ok {
		return b
	}
	return a
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
		proto, _ := normalizeServerProtocol(cfg.Type)
		rep.addCheck("protocol", "ok", proto, "")
		if proto == "reality" || proto == "hysteria2" {
			if _, serr := os.Stat(serverSingboxPath); serr != nil {
				rep.addCheck("singbox_config", "fail", serr.Error(), "sudo bx server install --protocol "+proto+" --host <host>")
			} else {
				rep.addCheck("singbox_config", "ok", serverSingboxPath, serverFirewallHintFor(cfg))
			}
		} else if port := listenPort(cfg.Listen); port == "" {
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
		if s.Config.Type == "reality" { // reality share = 主 server 内一 uuid,无独立服务/端口
			doctorLine("ok", "share "+s.Name, "reality（主 server 内一用户）")
			continue
		}
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
		if s.Config.Type == "reality" { // reality share = 主 server 内一 uuid,无独立服务/端口
			checks = append(checks, checkReport{Name: "share." + s.Name, Status: "ok", Detail: "reality user in main server"})
			continue
		}
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
		&cli.StringFlag{Name: "udp", Usage: "按类分流:UDP 走的专用传输链接(如 hysteria2,bx server install 默认就给)"},
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
	if a := protocolAdvisory(link); a != "" {
		fmt.Fprintln(os.Stderr, a)
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
	link, configLinks, err := resolveConfigLinks(arg)
	if err != nil {
		return err
	}
	if w := rawLinkRisk(arg); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	if a := protocolAdvisory(link); a != "" {
		fmt.Fprintln(os.Stderr, a)
	}
	cfgPath := c.String("config")
	if len(configLinks) > 1 {
		fmt.Printf("🔀 多传输:%d 个,自动容灾(主传输优先)\n", len(configLinks))
	}
	// 按类分流:--udp <link> → udp.transport(UDP/QUIC 走它加速,TCP 走主传输)。
	var udpTransport string
	if u := strings.TrimSpace(c.String("udp")); u != "" {
		_, udpLinks, uerr := resolveConfigLinks(u)
		if uerr != nil {
			return fmt.Errorf("--udp 链接无效: %w", uerr)
		}
		udpTransport = udpLinks[0]
		fmt.Printf("⚡ 按类分流:UDP 走专用传输(%s)\n", redactLink(udpTransport))
	}
	fmt.Println("⏳ 连通检测中…")
	if lat, perr := setup.ProbeServer("/var/lib/bx", link, c.String("probe"), 15*time.Second); perr != nil {
		if c.Bool("strict") {
			return fmt.Errorf("连通检测失败: %w", perr)
		}
		fmt.Printf("⚠️  连通检测未通过(仍写配置,稍后可排查): %v\n", perr)
	} else {
		fmt.Printf("✅ 服务器连通,延迟 %dms\n", lat)
	}
	if err := setup.WriteConfig(cfgPath, configLinks, udpTransport, c.Bool("force")); err != nil {
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
	if tunnel.IsClientLink(arg) {
		return "⚠ 这是含明文凭据的裸链接,已留进 shell 历史;分享/留存前建议先用 `bx blink <link>` 换壳成 bx://"
	}
	return ""
}

// protocolAdvisory 按协议在「当今强 DPI + 主动探测 + 服务端风控」下的强弱给建议(空=无需)。
// 不阻断——bx 照样直接用用户的链接;只提示并建议改 server 端,帮用户在强对抗下做对选择。
// 依据(2025-2026 实测):GFW 对 trojan/vmess/ss 主动探测检出 80-95%;弱协议还更易让 server IP
// 被各类服务(含 Claude/OpenAI/Google 等)风控封禁。reality 是当前最隐蔽(98-99% 突破),
// hysteria2 是速度档但裸 QUIC 会被 SNI 识别/限速,需 salamander 混淆。
func protocolAdvisory(link string) string {
	switch tunnel.Kind(strings.TrimSpace(link)) {
	case "trojan", "shadowsocks", "vmess":
		return "⚠ " + tunnel.Kind(link) + " 协议对当今强 DPI/主动探测较弱(2025 起 GFW 检出 80-95%),\n" +
			"   也更易让 server IP 被各类服务(含 Claude/OpenAI/Google 等)风控封禁。\n" +
			"   作 client 能直接用;但强封锁或需稳定访问 AI 服务时,建议 server 端改用 VLESS-REALITY\n" +
			"   (隐蔽性最强),速度档再叠 hysteria2(UDP,见 docs/multi-transport-guide.md)。"
	case "hysteria2":
		if !strings.Contains(link, "obfs=") {
			return "💡 hysteria2 是速度档(UDP/QUIC)。裸 QUIC 在部分网络(如中国电信)会被 SNI 识别/限速;\n" +
				"   建议 server 端开 salamander 混淆,链接加 ?obfs=salamander&obfs-password=<pw>。"
		}
		return ""
	default: // reality(最隐蔽)、brook(bx 默认)无需提示
		return ""
	}
}

// resolveConfigLinks 把 setup 的输入解析为「主传输(供连通探测)+ 各传输的 bx:// 换壳(供写配置)」。
// 支持裸 brook/vless(单)、bx://blink:// 单格式、bx:// 多传输 bundle(→ transports 列表,接 S1 容灾)。
func resolveConfigLinks(arg string) (probe string, configLinks []string, err error) {
	arg = strings.TrimSpace(arg)
	var internal []string
	switch {
	case strings.HasPrefix(arg, "bx://"), strings.HasPrefix(arg, "blink://"):
		internal, err = blink.DecodeAll(arg)
		if err != nil {
			return "", nil, err
		}
	case tunnel.IsClientLink(arg):
		internal = []string{arg}
	default:
		return "", nil, fmt.Errorf("不是支持的客户端链接")
	}
	configLinks = make([]string, len(internal))
	for i, l := range internal {
		configLinks[i] = blink.Encode(l) // 各自换壳存配置(0600 + 混淆)
	}
	return internal[0], configLinks, nil
}

func normalizeClientLink(arg string) (link string, configLink string, err error) {
	arg = strings.TrimSpace(arg)
	switch {
	case tunnel.IsClientLink(arg):
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
// serverHostFromLink 从各传输链接解析出 server 主机(用于 router-plan 显示 server bypass)。
func serverHostFromLink(link string) string {
	// ss:// / vmess:// 的 authority 是 base64,url.Parse 取不到 host,走专用解析。
	if strings.HasPrefix(link, "ss://") {
		if h, err := tunnel.SSHost(link); err == nil {
			return h
		}
		return ""
	}
	if strings.HasPrefix(link, "vmess://") {
		if h, err := tunnel.VmessHost(link); err == nil {
			return h
		}
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	switch u.Scheme { // host 在 authority(非 ?server=):reality/trojan/hysteria2/hy2
	case "vless", "trojan", "hysteria2", "hy2":
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

func kickAction(c *cli.Context) error {
	if _, err := supervisor.KickControl(supervisor.SockPath); err != nil {
		return fmt.Errorf("连接 bx 失败(bx 是否在运行?): %w", err)
	}
	fmt.Println("✅ 已触发强制重连(重建当前隧道,不碰 TUN/路由)。几秒后 bx status 查看效果。")
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
	args := c.Args().Slice()
	if len(args) == 0 {
		return fmt.Errorf("用法: bx blink <link> [link2 ...](brook:// 或 vless://;多个=容灾 bundle)")
	}
	for _, a := range args {
		if !tunnel.IsClientLink(a) {
			return fmt.Errorf("不支持的链接(仅 brook/vless/hysteria2/trojan/ss/vmess): %s", a)
		}
	}
	// 多个 link → 一条容灾 bundle bx://;单个 → legacy 单格式。
	fmt.Println(blink.EncodeMulti(args))
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
	if err := serverConfigComplete(cfg); err != nil {
		return err
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
	if err := serverConfigComplete(cfg); err != nil {
		return cfg, err
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

// serverFirewallHintFor 按协议给防火墙放行提示:reality=TCP、hysteria2=UDP、brook=其 listen 端口。
// 端口取 cfg.Port(默认 443);务必 ufw + 云安全组都放行。
func serverFirewallHintFor(cfg serverConfig) string {
	port := cfg.Port
	if port <= 0 {
		port = 443
	}
	switch cfg.Type {
	case "reality":
		return fmt.Sprintf("如果 VPS 启用了防火墙,请放行 TCP %d(ufw + 云安全组都要); ufw: sudo ufw allow %d/tcp", port, port)
	case "hysteria2":
		return fmt.Sprintf("如果 VPS 启用了防火墙,请放行 UDP %d(hysteria2 走 QUIC/UDP;ufw + 云安全组都要); ufw: sudo ufw allow %d/udp", port, port)
	default:
		return serverFirewallHint(cfg.Listen)
	}
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
