package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/gateway"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/mcp"
	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/procredact"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/srvgen"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/tray"
	"github.com/getbx/bx/internal/tunnel"
	"github.com/getbx/bx/internal/version"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// defaultConfigPath(客户端配置默认路径)是 OS-aware 的,见 paths_{windows,other}.go。
// server 相关路径仅 Linux 用,留在此处。
const (
	defaultServerConfigPath = "/etc/bx/server.yaml"
	defaultShareDir         = "/etc/bx/shares"
)

// 健康探测目标:必须是隧道出口能稳定连上的东西。github.com 本身常被黑洞/限速(尤其从代理出口),
// 用它当探针会把"github 慢"误判成"隧道挂了"导致无谓重连。1.1.1.1:443 是裸 IP(免 DNS)、全球稳定。
const (
	defaultProbeTarget      = "1.1.1.1:443"
	darwinDNSListen         = "127.0.0.1:53"
	defaultLogArchiveDir    = ".bx-log-archives"
	defaultAutoArchiveLimit = 12
)

// New 返回配置好子命令的 bx App。
func New() *cli.App {
	return &cli.App{
		Name:    "bx",
		Usage:   "透明全局代理",
		Version: version.String(),
		Action:  rootAction,
		Commands: []*cli.Command{
			guardianCommand(),
			{Name: "setup", Usage: "首次配置:写配置+装服务+连通检测(不启动)", ArgsUsage: "bx://...", Flags: setupFlags(), Action: setupAction},
			{Name: "probe", Usage: "检测 bx:// 链接连通性(不写配置/不改路由)", ArgsUsage: "bx://...", Flags: probeFlags(), Action: probeAction},
			{Name: "server", Usage: "管理 bx server", Subcommands: serverCommands()},
			{Name: "invite", Usage: "生成给普通用户的 bx 邀请", ArgsUsage: "[name]", Flags: inviteFlags(), Action: inviteAction},
			{Name: "user", Usage: "管理 bx 用户", Subcommands: userCommands()},
			{Name: "preset", Usage: "应用内置应用可用性规则", Subcommands: presetCommands()},
			{Name: "doctor", Usage: "诊断客户端配置和运行状态", Flags: doctorFlags(), Action: doctorAction},
			{Name: "inspect", Usage: "输出 agent 可读诊断包", Flags: inspectFlags(), Action: inspectAction},
			{Name: "leak-check", Usage: "非交互的机器可读检查(不开页面;供 MCP 与脚本):本机泄漏面 + 其它 VPN 共存 + 可选主动出口探测。人用请敲 leakcheck(没有连字符),它会开页面并把两半事实对起来", Flags: leakCheckFlags(), Action: leakCheckAction},
			{Name: "leakcheck", Usage: "泄漏检测【推荐】:开浏览器页面,把浏览器那半(WebRTC/公网出口)与本机那半(路由/DNS)对起来 —— 只有两半合起来才判得了泄漏。保护关着、别的 VPN 在跑时照样能用", Flags: leakcheckFlags(), Action: leakcheckAction},
			{Name: "observe", Usage: "观察一小段运行期状态变化(只读)", Flags: observeFlags(), Action: observeAction},
			{Name: "capabilities", Usage: "输出机器可读能力清单", Action: capabilitiesAction},
			{Name: "up", Usage: "启动并设为开机自启", Action: upAction},
			{Name: "down", Usage: "停止并取消开机自启", Action: downAction},
			{Name: "reconnect", Usage: "故障排查:手动触发安全恢复(正常网络变化会自动恢复)", Flags: reconnectFlags(), Action: reconnectAction},
			{Name: "restart", Usage: "安全重连保护(reconnect 的兼容别名)", Hidden: true, Action: restartAction},
			{Name: "update", Usage: "更新 bx 到最新 release(SHA256 校验 + 原子替换,不打断保护)", Flags: updateFlags(), Action: updateAction},
			{Name: "direct", Usage: "管理直连白名单(global 下只有白名单域名直连,其余走隧道)", Subcommands: directCommands()},
			{Name: "proxy", Usage: "管理强制走隧道的域名", Subcommands: proxyCommands()},
			{Name: "dns", Usage: "管理 macOS 系统 DNS 接管", Subcommands: dnsCommands()},
			{Name: "realtime", Usage: "查看实时 UDP 策略", Subcommands: realtimeCommands()},
			{Name: "run", Usage: "前台运行(调试/服务内部用)", Hidden: true, Flags: runFlags(), Action: runAction},
			{Name: "tray", Usage: "启动系统托盘(Windows;点图标连/断/设置/看状态)", Action: trayAction},
			{Name: "autostart", Usage: "开机自启开关(Windows;on|off|status)", ArgsUsage: "on|off|status", Action: autostartAction},
			{Name: "debug-tun", Usage: "仅创建 TUN 适配器(不起隧道/不碰路由),真机验证 wintun+wgbridge", Hidden: true, Flags: debugTunFlags(), Action: debugTunAction},
			{Name: "serve", Usage: "运行 bx server", Hidden: true, Flags: serveFlags(), Action: serveAction},
			{Name: "mcp", Usage: "启动 agent 控制面 MCP server(stdio)", Hidden: false, Flags: mcpFlags(), Action: mcpAction, Subcommands: []*cli.Command{
				{Name: "install", Usage: "打印把 bx 接入你的 agent 的 MCP 配对指令(只打印,不自跑)", Action: mcpInstallAction},
			}},
			{Name: "status", Usage: "查看状态面板", Flags: statusFlags(), Action: statusAction},
			{Name: "logs", Usage: "查看客户端日志", Flags: logsFlags(), Action: logsAction},
			{Name: "link", Usage: "生成 bx:// 链接", ArgsUsage: "<internal-link>", Hidden: true, Action: linkAction},
			{Name: "blink", Usage: "把内部传输链接换壳成 bx://", ArgsUsage: "<link> [link2 ...]", Hidden: true, Action: linkAction},
			{Name: "darwin-plan", Usage: "打印 macOS 路由 dry-run 计划(不改网络)", Hidden: true, Flags: darwinPlanFlags(), Action: darwinPlanAction},
			{Name: "router-plan", Usage: "打印 router 模式 dry-run 计划(ip + nft,不改网络)", Hidden: true, Flags: routerPlanFlags(), Action: routerPlanAction},
			{
				Name:   "app-install",
				Usage:  "从 Bx.app 安装统一 runtime/CLI bridge/Guardian(macOS,root)",
				Hidden: true,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "app-source", Usage: "源 Bx.app 路径(默认从自身位置推导)"},
					&cli.StringFlag{Name: "config", Value: defaultConfigPath, Usage: "Guardian 配置路径"},
					&cli.BoolFlag{Name: "yes", Usage: "跳过升级确认(非交互调用必须显式加;已装过 bx 时会停止并重启保护)"},
				},
				Action: appInstallAction,
			},
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

type usersReport struct {
	OK              bool       `json:"ok"`
	SecretsRedacted bool       `json:"secrets_redacted"`
	Users           []userView `json:"users"`
}

type userReport struct {
	OK              bool     `json:"ok"`
	SecretsRedacted bool     `json:"secrets_redacted"`
	User            userView `json:"user"`
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

type clientStatusReport struct {
	*stats.Report
	CoreAvailable     bool                      `json:"core_available"`
	CoreEvidence      string                    `json:"core_evidence"`
	Desired           string                    `json:"desired,omitempty"`
	ProtectionState   string                    `json:"protection_state"`
	NetworkGeneration string                    `json:"network_generation"`
	Recovery          guardian.RecoverySnapshot `json:"recovery"`
	DNSState          guardian.DNSState         `json:"dns_state"`
	DNSManaged        bool                      `json:"dns_managed"`
	DNSService        string                    `json:"dns_service,omitempty"`
	Phase             string                    `json:"phase,omitempty"`
	CoreVersion       string                    `json:"core_version,omitempty"`
	GuardianVersion   string                    `json:"guardian_version,omitempty"`
	RuntimeVersion    string                    `json:"runtime_version,omitempty"`

	// Observed 与 Divergence 是刚从系统读回的事实,以及它与上面那些「信念」字段的
	// 差异。刻意不用观测覆盖信念——二者的 diff 本身就是最有价值的诊断信号。
	Observed   *observe.ObservedState `json:"observed,omitempty"`
	Divergence []observe.Divergence   `json:"divergence,omitempty"`

	// Reconcile 是 Guardian 那条只观察调谐环**最近一轮**的判断,与上面的一次性
	// 观测**并列**发布,不是替换它。
	//
	// 一次性观测的价值是「你敲命令那一刻的新鲜事实」——bx status 正是出问题时
	// 最先敲的命令;拿一份最多可能陈旧 10 分钟(退避上限)的缓存去顶替它是退化。
	// 而循环那份的价值在于时间:它回答的是「这段时间里系统一直是什么样」,
	// 以及「调谐器一直想做什么」——而后者是阶段③b 授权之前唯一的证据来源。
	//
	// 缺席有两种意思,靠 GuardianCapabilities 分开(见 renderClientStatus)。
	Reconcile *guardian.ReconcileReport `json:"reconcile,omitempty"`

	// GuardianCapabilities 是对面那一版 Guardian 自己声明的能力集合。
	//
	// omitempty 在这里是对的,与 guardian.Status.Capabilities 刻意不加 omitempty
	// 并不矛盾:那一层要分开「声明过、集合是空的」与「压根没声明」,而这一层的
	// 每一个消费者问的都是「集合里有没有某一条」——空集合与从未声明的答案相同
	// (都是「没有」),于是「我们根本没问到 Guardian」如实表现为字段缺席。
	GuardianCapabilities []string `json:"guardian_capabilities,omitempty"`

	// MaintenanceHold 是正在生效的维护挂起。它解释了「desired=on 而保护不在」——
	// 没有它,一台正在升级的机器与一台坏掉的机器在 bx status 上长得一模一样。
	//
	// 缺席有两种意思(「这一版 Guardian 不认识挂起」与「认识、此刻没有」),
	// 靠 GuardianCapabilities 里的 CapabilityMaintenanceHold 分开 —— 与 Reconcile
	// 同一机制。渲染层对两者都**什么都不写**:一行常驻的「没有挂起」正是
	// observerForPlatform 那道门在防的噪声。
	MaintenanceHold *guardian.MaintenanceHoldStatus `json:"maintenance_hold,omitempty"`
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

type logsReport struct {
	OK              bool     `json:"ok"`
	Kind            string   `json:"kind"`
	Version         string   `json:"version"`
	SecretsRedacted bool     `json:"secrets_redacted"`
	ChangesSystem   bool     `json:"changes_system"`
	ChangesNetwork  bool     `json:"changes_network"`
	RequiresRoot    bool     `json:"requires_root"`
	Service         string   `json:"service"`
	Lines           int      `json:"lines"`
	Text            string   `json:"text,omitempty"`
	Error           string   `json:"error,omitempty"`
	Hint            string   `json:"hint,omitempty"`
	Paths           []string `json:"paths,omitempty"`
}

type observeReport struct {
	OK              bool           `json:"ok"`
	Kind            string         `json:"kind"`
	Version         string         `json:"version"`
	SecretsRedacted bool           `json:"secrets_redacted"`
	ChangesSystem   bool           `json:"changes_system"`
	ChangesNetwork  bool           `json:"changes_network"`
	RequiresRoot    bool           `json:"requires_root"`
	Risk            string         `json:"risk"`
	Scenario        string         `json:"scenario,omitempty"`
	DurationMS      int64          `json:"duration_ms"`
	Samples         int            `json:"samples"`
	Start           *stats.Report  `json:"start,omitempty"`
	End             *stats.Report  `json:"end,omitempty"`
	Delta           stats.Snapshot `json:"delta"`
	Checks          []checkReport  `json:"checks"`
	TestSteps       []string       `json:"test_steps,omitempty"`
	Evidence        []string       `json:"evidence,omitempty"`
	Recommendations []string       `json:"recommendations,omitempty"`
	Error           string         `json:"error,omitempty"`
	Hint            string         `json:"hint,omitempty"`
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

type leakCheckReport struct {
	OK              bool                `json:"ok"`
	Kind            string              `json:"kind"`
	Version         string              `json:"version"`
	SecretsRedacted bool                `json:"secrets_redacted"`
	ChangesSystem   bool                `json:"changes_system"`
	ChangesNetwork  bool                `json:"changes_network"`
	RequiresRoot    bool                `json:"requires_root"`
	Risk            string              `json:"risk"`
	Checks          []checkReport       `json:"checks"`
	Network         *networkProbeReport `json:"network,omitempty"`
	WebRTC          webrtcCheckReport   `json:"webrtc"`
	Doctor          doctorReport        `json:"doctor"`
	Evidence        []string            `json:"evidence,omitempty"`
	NextActions     []string            `json:"next_actions,omitempty"`
}

type networkProbeResult struct {
	IPv4    string   `json:"ipv4,omitempty"`
	IPv4Err string   `json:"ipv4_error,omitempty"`
	IPv6    string   `json:"ipv6,omitempty"`
	IPv6Err string   `json:"ipv6_error,omitempty"`
	DNSName string   `json:"dns_name,omitempty"`
	DNSIPs  []string `json:"dns_ips,omitempty"`
	DNSErr  string   `json:"dns_error,omitempty"`
}

type networkProbeReport struct {
	OK              bool               `json:"ok"`
	Kind            string             `json:"kind"`
	Version         string             `json:"version"`
	SecretsRedacted bool               `json:"secrets_redacted"`
	ChangesSystem   bool               `json:"changes_system"`
	ChangesNetwork  bool               `json:"changes_network"`
	RequiresRoot    bool               `json:"requires_root"`
	Risk            string             `json:"risk"`
	Result          networkProbeResult `json:"result"`
	Checks          []checkReport      `json:"checks"`
	Evidence        []string           `json:"evidence,omitempty"`
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
		// 客户端那一半:在已配好的几台之间**由用户自己**选(不是自动容灾)。
		{
			Name:  "list",
			Usage: "列出已配置的服务器,并标出当前在用的那台",
			// --test 会往隧道**外面**发包(每台一次 TCP 握手),所以它是
			// 显式的一下,不是默认行为。
			Flags:  append(ruleBaseFlags(), &cli.BoolFlag{Name: "test", Usage: "顺便测一遍延迟与可达性(会往隧道外面发包)"}),
			Action: serverListAction,
		},
		{Name: "use", Usage: "切换到清单里的另一台(先热切,切不动才需要重启)", ArgsUsage: "<name>", Flags: ruleBaseFlags(), Action: serverUseAction},
		{Name: "rm", Usage: "从清单里删掉一台(不能删当前在用的)", ArgsUsage: "<name>", Flags: ruleBaseFlags(), Action: serverRemoveAction},
		{Name: "deploy", Usage: "从本机把 bx server 装到一台 VPS 上(走系统 ssh,bx 不经手凭据)", ArgsUsage: "<user@host>", Flags: serverDeployFlags(), Action: serverDeployAction},
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

func userCommands() []*cli.Command {
	return []*cli.Command{
		{Name: "list", Usage: "列出 bx 用户", Flags: userListFlags(), Action: userListAction},
		{Name: "show", Usage: "查看一个用户", ArgsUsage: "<name>", Flags: userShowFlags(), Action: userShowAction},
		{Name: "invite", Usage: "生成或复显用户邀请", ArgsUsage: "<name>", Flags: inviteFlags(), Action: inviteAction},
		{Name: "revoke", Usage: "撤销一个用户", ArgsUsage: "<name>", Flags: userRevokeFlags(), Action: userRevokeAction},
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
	}
}

func reconnectFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "check", Usage: "只检查运行中的 bx 是否支持安全重连"},
		&cli.BoolFlag{Name: "json", Usage: "输出最终 recovery snapshot"},
	}
}

func leakCheckFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出 agent 可读 JSON"},
		&cli.BoolFlag{Name: "network", Usage: "发起只读外网出口/DNS 探测"},
		&cli.DurationFlag{Name: "network-timeout", Value: 8 * time.Second, Usage: "等待外网出口/DNS 探测的最长时间"},
		&cli.StringSliceFlag{Name: "expected-ip", Usage: "允许出现的代理/VPS 公网 IP(可重复)"},
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
		&cli.StringFlag{Name: "dns-service", Usage: "macOS 网络服务名(留空自动探测)"},
	}
}

func observeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出 agent 可读 JSON"},
		&cli.DurationFlag{Name: "duration", Value: 30 * time.Second, Usage: "观察窗口"},
		&cli.DurationFlag{Name: "interval", Value: 2 * time.Second, Usage: "采样间隔"},
		&cli.StringFlag{Name: "scenario", Value: "general", Usage: "观察场景: general|video|realtime"},
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
		&cli.BoolFlag{Name: "open-ufw", Usage: "安装后自动执行 ufw allow(reality+hys2 会同时放行 tcp 与 udp)"},
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

func inviteFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultServerConfigPath, Usage: "server 配置路径"},
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "share 配置目录"},
		&cli.StringFlag{Name: "host", Usage: "公网地址或域名(brook server 需要;reality 链接通常已内含)"},
		&cli.BoolFlag{Name: "open-ufw", Usage: "brook share 创建后自动执行 ufw allow <port>/tcp"},
	}
}

func userListFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "用户/share 配置目录"},
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
	}
}

func userShowFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "用户/share 配置目录"},
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
	}
}

func userRevokeFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "dir", Value: defaultShareDir, Usage: "用户/share 配置目录"},
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
		&cli.BoolFlag{Name: "json", Usage: "输出 agent 可读 JSON"},
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
	if c.Bool("open-ufw") {
		rules := serverUFWRules(cfg)
		if err := openUFWRules(rules); err != nil {
			return err
		}
		fmt.Printf("✅ 已放行 ufw 规则: %s\n", strings.Join(rules, ", "))
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
	for _, u := range publicIPProbeURLs {
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
	main, udp := encodedClientLinks(cfg)
	if udp != "" {
		fmt.Println("🔀 reality(TCP/隐蔽)+ hysteria2(UDP/加速)就绪。客户端一条命令配齐(按类分流,既安全又有速度):")
		fmt.Printf("  %s\n", setupCommand(main, udp))
		return
	}
	fmt.Println(main)
}

func encodedClientLinks(cfg serverConfig) (main string, udp string) {
	if cfg.Link != "" {
		main = blink.Encode(cfg.Link)
	}
	if cfg.UDPLink != "" {
		udp = blink.Encode(cfg.UDPLink)
	}
	return main, udp
}

func setupCommand(main, udp string) string {
	if udp != "" {
		return setupCommandLine(main, udp)
	}
	return fmt.Sprintf("sudo bx setup '%s'", main)
}

func inviteText(name, main, udp string) string {
	title := "bx invite"
	if name != "" {
		title += ": " + name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", title)
	fmt.Fprintln(&b, "给用户:")
	fmt.Fprintln(&b, "  1. 安装 bx")
	fmt.Fprintln(&b, "  2. 打开 bx 菜单栏 App,粘贴下面的 bx:// 链接")
	fmt.Fprintln(&b, "  3. 点击 Start Protection")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "bx:// 链接:")
	fmt.Fprintf(&b, "  %s\n", main)
	if udp != "" {
		fmt.Fprintf(&b, "  UDP: %s\n", udp)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "命令行备用:")
	fmt.Fprintf(&b, "  %s\n", setupCommand(main, udp))
	fmt.Fprintln(&b, "  sudo bx up")
	return b.String()
}

func inviteAction(c *cli.Context) error {
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		cfg, err := readServerConfig(c.String("config"))
		if err != nil {
			return err
		}
		if cfg.Link == "" {
			host := c.String("host")
			if host == "" {
				return fmt.Errorf("brook server 生成邀请需要 --host <VPS_IP或域名>")
			}
			link, err := bxServerLink(host, cfg)
			if err != nil {
				return err
			}
			cfg.Link = mustDecodeBXLink(link)
		}
		main, udp := encodedClientLinks(cfg)
		if main == "" {
			return fmt.Errorf("server 配置没有可分享链接")
		}
		fmt.Print(inviteText("", main, udp))
		return nil
	}

	name, err := cleanShareName(name)
	if err != nil {
		return err
	}
	cfg, err := inviteShareConfig(name, c.String("config"), c.String("dir"), c.String("host"))
	if err != nil {
		return err
	}
	main, udp := encodedClientLinks(cfg)
	if main == "" {
		return fmt.Errorf("share %s 没有可分享链接", name)
	}
	if c.Bool("open-ufw") && cfg.Listen != "" {
		if err := openUFW(cfg.Listen); err != nil {
			return err
		}
	}
	fmt.Print(inviteText(name, main, udp))
	return nil
}

func inviteShareConfig(name, configPath, dir, host string) (serverConfig, error) {
	if cfg, err := readServerConfig(shareConfigPath(dir, name)); err == nil {
		if cfg.Link == "" {
			if host == "" {
				return serverConfig{}, fmt.Errorf("brook share %s 显示邀请需要 --host <VPS_IP或域名>", name)
			}
			link, err := bxServerLink(host, cfg)
			if err != nil {
				return serverConfig{}, err
			}
			cfg.Link = mustDecodeBXLink(link)
		}
		return cfg, nil
	}
	mainCfg, err := readServerConfig(configPath)
	if err != nil {
		return serverConfig{}, err
	}
	switch mainCfg.Type {
	case "reality":
		return realityShare(name, dir, mainCfg)
	case "hysteria2":
		return serverConfig{}, fmt.Errorf("hysteria2 主 server 暂不支持多用户邀请;请使用默认 reality+hysteria2 server")
	default:
		if host == "" {
			return serverConfig{}, fmt.Errorf("brook server 创建邀请需要 --host <VPS_IP或域名>")
		}
		link, _, err := createShare(name, host, dir, "", "")
		if err != nil {
			return serverConfig{}, err
		}
		raw, err := blink.Decode(link)
		if err != nil {
			return serverConfig{}, err
		}
		cfg, err := readServerConfig(shareConfigPath(dir, name))
		if err != nil {
			return serverConfig{}, err
		}
		cfg.Link = raw
		return cfg, nil
	}
}

func mustDecodeBXLink(link string) string {
	raw, err := blink.Decode(link)
	if err != nil {
		return link
	}
	return raw
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
	rec := serverConfig{
		Type: "reality", SNI: mainCfg.SNI, Port: mainCfg.Port,
		Link: swapVlessUUID(mainCfg.Link, newUUID), UDPLink: mainCfg.UDPLink,
	}
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

func userListAction(c *cli.Context) error {
	shares, err := readShares(c.String("dir"))
	if err != nil {
		return err
	}
	users := userViews(shares)
	if c.Bool("json") {
		return writeJSON(os.Stdout, usersReport{OK: true, SecretsRedacted: true, Users: users})
	}
	if len(users) == 0 {
		fmt.Println("No users.")
		return nil
	}
	fmt.Println("NAME\tTYPE\tSTATUS\tPLAN")
	for _, u := range users {
		fmt.Printf("%s\t%s\t%s\t%s\n", u.Name, u.Type, u.Status, u.Plan)
	}
	return nil
}

func userShowAction(c *cli.Context) error {
	name, err := cleanShareName(c.Args().First())
	if err != nil {
		return err
	}
	share, err := readShare(c.String("dir"), name)
	if err != nil {
		return err
	}
	user := userViewFromShare(share)
	if c.Bool("json") {
		return writeJSON(os.Stdout, userReport{OK: true, SecretsRedacted: true, User: user})
	}
	fmt.Printf("bx user: %s\n", user.Name)
	fmt.Printf("  Type    %s\n", user.Type)
	fmt.Printf("  Status  %s\n", user.Status)
	fmt.Printf("  Plan    %s\n", user.Plan)
	if user.Listen != "" {
		fmt.Printf("  Listen  %s\n", user.Listen)
	}
	fmt.Printf("  Invite  sudo bx user invite %s\n", user.Name)
	return nil
}

func userRevokeAction(c *cli.Context) error {
	name, err := cleanShareName(c.Args().First())
	if err != nil {
		return err
	}
	if err := revokeShare(name, c.String("dir")); err != nil {
		return err
	}
	fmt.Printf("✅ user %s 已撤销。\n", name)
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
	var ufwRules []string
	if install.ServerUnitInstalled() {
		fmt.Println("bx server 已安装,直接启动(要换协议/重生成密钥:sudo bx server install --force)。")
		// 已装分支不经过 serverInstallAction,--open-ufw 要在这里单独生效(读盘现有配置推导规则)。
		if c.Bool("open-ufw") {
			cfg, err := readServerConfig(c.String("config"))
			if err != nil {
				return fmt.Errorf("读取 server 配置以应用 --open-ufw: %w", err)
			}
			ufwRules = serverUFWRules(cfg)
			if err := openUFWRules(ufwRules); err != nil {
				return err
			}
		}
	} else if err := serverInstallAction(c); err != nil {
		return err
	}
	if err := install.EnableServer(); err != nil {
		return err
	}
	if len(ufwRules) > 0 {
		fmt.Printf("✅ 已放行 ufw 规则: %s\n", strings.Join(ufwRules, ", "))
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
	if c.Bool("json") {
		if c.Bool("follow") {
			return fmt.Errorf("--json 不能和 --follow 同时使用")
		}
		raw, err := install.TailLogs(install.ServerServiceName, c.Int("lines"))
		return writeJSON(os.Stdout, logsReportFromTail("server", c.Int("lines"), raw, err))
	}
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
	path, err := provision.EnsureBrook("/var/lib/bx", "", embedded.Brook(), embedded.BrookVersion(), "", "")
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
	if runtime.GOOS == "darwin" {
		for _, line := range darwinServiceDoctorLines(install.GuardianInstalled(), install.GuardianActive()) {
			doctorLine(line.Status, line.Key, line.Value)
		}
	} else {
		doctorLine(boolStatus(install.UnitInstalled()), "service installed", install.ServiceName)
		activeState := serviceState("is-active", install.ServiceName)
		doctorLine(serviceStatusFromState("is-active", activeState), "service active", activeState)
		if activeState != "active" {
			doctorLine("hint", "logs", "bx logs")
		}
		enabledState := serviceState("is-enabled", install.ServiceName)
		doctorLine(serviceStatusFromState("is-enabled", enabledState), "service enabled", enabledState)
	}
	if err := checkStatusSocket(); err != nil {
		doctorLine("warn", "status socket", err.Error())
		doctorLine("hint", "logs", "bx logs")
	} else {
		doctorLine("ok", "status socket", "reachable")
	}
	if runtime.GOOS == "darwin" {
		guardianStatus, guardianErr := readGuardianStatus()
		if guardianErr != nil {
			guardianStatus = guardianStatusFallback(stats.Report{}, runtime.GOOS)
		}
		check := recoveryDoctorCheck(guardianStatus.Recovery)
		doctorLine(check.Status, check.Name, check.Detail)
		if check.Hint != "" {
			doctorLine("hint", check.Name, check.Hint)
		}
	}
	for _, check := range collectPlatformChecks(c.Context) {
		doctorLine(check.Status, check.Name, check.Detail)
		if check.Hint != "" {
			doctorLine("hint", check.Name, check.Hint)
		}
	}
	return nil
}

func inspectAction(c *cli.Context) error {
	return writeJSON(os.Stdout, collectClientInspect(c.String("config"), c.String("target"), c.Duration("timeout"), c.Bool("skip-probe")))
}

func leakCheckAction(c *cli.Context) error {
	rep := collectLeakCheck(c.Context, c.String("config"), c.String("dns-service"), c.Bool("network"), c.Duration("network-timeout"), c.StringSlice("expected-ip"))
	if c.Bool("json") {
		return writeJSON(os.Stdout, rep)
	}
	fmt.Println("bx leak-check")
	fmt.Printf("  风险    %s\n", rep.Risk)
	for _, check := range rep.Checks {
		doctorLine(check.Status, check.Name, check.Detail)
	}
	for _, action := range rep.NextActions {
		fmt.Printf("  下一步  %s\n", action)
	}
	return nil
}

func observeAction(c *cli.Context) error {
	rep := collectObserve(c.Context, c.Duration("duration"), c.Duration("interval"), c.String("scenario"))
	if c.Bool("json") {
		return writeJSON(os.Stdout, rep)
	}
	fmt.Println("bx observe")
	fmt.Printf("  风险    %s\n", rep.Risk)
	for _, check := range rep.Checks {
		doctorLine(check.Status, check.Name, check.Detail)
	}
	for _, rec := range rep.Recommendations {
		fmt.Printf("  建议    %s\n", rec)
	}
	if rep.Error != "" {
		fmt.Printf("  错误    %s\n", rep.Error)
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
				Command:        "bx leak-check --json",
				Category:       "diagnostics",
				Summary:        "Aggregate network-path leak risk from doctor, DNS/UDP state, WebRTC, IPv6, and QUIC posture.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--network", "--network-timeout <duration>", "--expected-ip <ip>", "--config <path>", "--dns-service <name>"},
				Examples:       []string{"bx leak-check --json", "bx leak-check --network --json --expected-ip <proxy-ip>"},
				SafeNotes:      []string{"Read-only for system settings.", "Default mode does not send outbound probes.", "--network sends outbound IPv4/IPv6/DNS requests to classify the current exit path.", "Scope is network-path leakage only; browser fingerprinting is intentionally out of scope."},
			},
			{
				Command:        "bx observe --json",
				Category:       "diagnostics",
				Summary:        "Sample bx runtime counters over a short window to explain app/video failures.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--duration <duration>", "--interval <duration>"},
				Examples:       []string{"bx observe --json", "bx observe --json --duration 30s"},
				SafeNotes:      []string{"Read-only.", "Uses the local status socket only.", "Does not send outbound probes, open browsers, or change routing/DNS."},
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
				Command:        "sudo bx reconnect",
				Category:       "control",
				Summary:        "Reconnect transport without releasing protection.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Examples:       []string{"sudo bx reconnect"},
				SafeNotes:      []string{"Requires a running bx service.", "Builds and verifies a replacement transport before switching.", "Does not release TUN, routes, or managed DNS; a failed replacement leaves the current protected path in place."},
			},
			{
				Command:        "sudo bx update",
				Category:       "update",
				Summary:        "Download, verify, and atomically replace the bx binary without interrupting protection.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Arguments:      []string{"--check", "--force"},
				Examples:       []string{"sudo bx update", "bx update --check"},
				SafeNotes:      []string{"Downloads a SHA256-checked release and atomically replaces the CLI binary.", "Does not restart the running protection service or release TUN, routes, or DNS.", "The replacement binary is used the next time protection starts.", "On a protected macOS unified install, this runs as a fail-closed Guardian update transaction: network access may pause briefly, it never falls back to a direct (unprotected) connection, and a failed health check automatically rolls back to the previous version while protection stays on.", "Agents should call this command directly; do not simulate an update by combining down and up."},
			},
			{
				Command:        "sudo bx direct add <domain>",
				Category:       "routing",
				Summary:        "Add a domain to the direct allowlist and hot-reload routing rules when bx is running.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<domain>...", "--config <path>", "--force"},
				Examples:       []string{"sudo bx direct add taobao.com", "sudo bx direct ls", "sudo bx direct rm taobao.com"},
				SafeNotes:      []string{"Writes client config only.", "direct/proxy entries are mutually exclusive; adding direct removes matching proxy entries.", "Public cloud/open-subdomain domains are skipped unless --force is provided."},
			},
			{
				Command:        "sudo bx proxy add <domain>",
				Category:       "routing",
				Summary:        "Force tunnel routing for a domain and hot-reload routing rules when bx is running.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<domain>...", "--config <path>"},
				Examples:       []string{"sudo bx proxy add openai.com", "sudo bx proxy ls", "sudo bx proxy rm openai.com"},
				SafeNotes:      []string{"Writes client config only.", "Adds force tunnel rules; matching direct entries are removed to avoid route conflicts.", "Does not change TUN, routes, or DNS directly."},
			},
			{
				Command:        "sudo bx preset apply <name>",
				Category:       "routing",
				Summary:        "Apply a curated app/CDN usability preset as explicit direct rules and hot-reload when bx is running.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<name>", "--config <path>"},
				Examples:       []string{"bx preset ls", "bx preset show gaming", "sudo bx preset apply gaming"},
				SafeNotes:      []string{"Explicit opt-in; no preset is active by default.", "Writes client config direct rules only and removes matching proxy rules.", "Does not directly change TUN, routes, or DNS.", "Uses the local reload control when bx is running; otherwise it takes effect on the next sudo bx up."},
			},
			{
				Command:        "bx logs",
				Category:       "diagnostics",
				Summary:        "Show recent client service logs.",
				Stable:         true,
				RequiresRoot:   false,
				ChangesSystem:  false,
				ChangesNetwork: false,
				Outputs:        []string{"text", "json"},
				Arguments:      []string{"--json", "--lines <n>", "--follow", "--archive", "--dir <path>"},
				Examples:       []string{"bx logs --json", "bx logs", "bx logs -n 200", "bx logs --archive"},
				SafeNotes:      []string{"Read-only.", "--json returns structured text/error/hint fields for agents.", "May require sudo depending on system log permissions.", "Use --archive to preserve raw logs and diagnostic snapshots.", "Automatic diagnostics are kept under the platform log directory for bx up/down/doctor."},
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
				Arguments:      []string{"--config <path>"},
				Examples:       []string{"sudo bx realtime on"},
				SafeNotes:      []string{"Writes client config without restarting the active protection service.", "The changed UDP policy is used the next time protection starts.", "Relays non-DNS UDP through bx instead of using the local real network path."},
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
				Arguments:      []string{"--config <path>"},
				Examples:       []string{"sudo bx realtime off"},
				SafeNotes:      []string{"Writes client config without restarting the active protection service.", "The changed UDP policy is used the next time protection starts.", "Block mode blocks non-DNS UDP."},
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
				Arguments:      []string{"--host <host>", "--listen <addr>", "--password <password>", "--force", "--open-ufw"},
				Examples:       []string{"sudo bx server install --host <host>"},
				SafeNotes:      []string{"May change firewall only when --open-ufw is passed."},
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
				Command:        "sudo bx invite [name]",
				Category:       "server",
				Summary:        "Print a user-friendly bx invite with install steps and client link.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"[name]", "--host <host>", "--config <path>", "--dir <dir>", "--open-ufw"},
				Examples:       []string{"sudo bx invite", "sudo bx invite alice", "sudo bx invite alice --host <host>"},
				SafeNotes:      []string{"With a name, creates or reuses a per-user share.", "May change firewall only when --open-ufw is passed.", "Use this as the preferred human-facing sharing command."},
			},
			{
				Command:        "sudo bx user list --json",
				Category:       "server",
				Summary:        "List bx users in the management layer.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  false,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"json"},
				Arguments:      []string{"--json", "--dir <dir>"},
				Examples:       []string{"sudo bx user list --json", "sudo bx user show alice --json"},
				SafeNotes:      []string{"Read-only.", "Secrets are redacted.", "Users are backed by existing share records in this phase."},
			},
			{
				Command:        "sudo bx user invite <name>",
				Category:       "server",
				Summary:        "Create or reprint a user's invite.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				ReadsSecrets:   true,
				Outputs:        []string{"text"},
				Arguments:      []string{"<name>", "--host <host>", "--config <path>", "--dir <dir>", "--open-ufw"},
				Examples:       []string{"sudo bx user invite alice"},
				SafeNotes:      []string{"Alias of the invite flow under the user management namespace.", "May change firewall only when --open-ufw is passed."},
			},
			{
				Command:        "sudo bx user revoke <name>",
				Category:       "server",
				Summary:        "Revoke one bx user.",
				Stable:         true,
				RequiresRoot:   true,
				ChangesSystem:  true,
				ChangesNetwork: false,
				Outputs:        []string{"text"},
				Arguments:      []string{"<name>", "--dir <dir>"},
				Examples:       []string{"sudo bx user revoke alice"},
				SafeNotes:      []string{"Uses the same revocation path as server share revoke."},
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
	return collectClientDoctorWith(configPath, target, timeout, skipProbe, true)
}

// collectClientDoctorWith 是 collectClientDoctor 的完整实现,多一个 includePlatformChecks 开关:
// doctorAction 的独立 JSON 路径要平台检查(true,行为不变);collectLeakCheck 内嵌本函数时传 false,
// 因为 leak-check 自己在顶层已经跑一遍 collectPlatformChecks——避免同一批 pgrep/netstat/scutil
// 探测跑两遍、同一条检查在 leak.doctor.checks[] 和 leak.checks[] 里重复出现。
func collectClientDoctorWith(configPath, target string, timeout time.Duration, skipProbe, includePlatformChecks bool) doctorReport {
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
	// macOS 上服务三行必须问 **Guardian**,不是 Core / 不是 systemd。
	//
	// 统一布局下 Core 不是 launchd 服务(由 Guardian 起停),所以
	// `install.UnitInstalled()`(查 Core 的两个 plist)与 `install.ServiceName`
	// (systemd 的 "bx.service")在一台**装好且正在保护**的 mac 上必然三条 FAIL
	// —— 真机 2026-08-06,教训写在 darwinGuardianServiceName 旁边。人读版 doctor
	// (doctorAction)早就照做了,**这条 --json 路径此前漏了**:后果不只是三行难看,
	// 还有 `rep.OK = !rep.hasFail()` 让一台健康的 mac 恒报 `ok:false`,以及
	// doctorNextActions 把 "sudo bx setup <client-link>" 列进 next_actions ——
	// 建议用户去重跑一个已经跑过的 setup。
	for _, check := range serviceDoctorChecks(runtime.GOOS, guardianServiceChecks, systemdServiceChecks) {
		rep.addReport(check)
	}
	if err := checkStatusSocket(); err != nil {
		rep.addCheck("status_socket", "warn", err.Error(), "bx logs")
	} else {
		rep.addCheck("status_socket", "ok", "reachable", "")
	}
	status, detail, hint := udpPolicyDoctor(udpMode)
	rep.addCheck("udp_policy", status, detail, hint)
	if runtime.GOOS == "darwin" {
		guardianStatus, err := readGuardianStatus()
		if err != nil {
			guardianStatus = guardianStatusFallback(stats.Report{}, runtime.GOOS)
		}
		rep.Checks = append(rep.Checks, guardianDNSDoctorCheck(guardianStatus))
		rep.Checks = append(rep.Checks, recoveryDoctorCheck(guardianStatus.Recovery))
	}
	if includePlatformChecks {
		for _, check := range collectPlatformChecks(context.Background()) {
			rep.addReport(check)
		}
	}
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

// collectLeakCheck 是**非交互**的那一半:不开浏览器、不等用户点击,供 MCP 与脚本用。
//
// 浏览器那条路已经整个搬去 `bx leakcheck`(internal/leakserve)。此前这里另有一份
// 内嵌 HTML 页 + 第二个 loopback 服务器,与那边**同一件事两份实现** —— 而一道对
// 浏览器开放的本机端口有两份实现,就有两份要守的安全面,却只有一份会被想起来。
func collectLeakCheck(ctx context.Context, configPath, dnsService string, network bool, networkTimeout time.Duration, expectedIPs []string) leakCheckReport {
	// includePlatformChecks=false:下面已经在顶层跑过一次 collectPlatformChecks(ctx),
	// 内嵌 doctor 不重复跑,避免同一批探测执行两次、同一条检查出现在 doctor.checks[] 和顶层 checks[] 里。
	doctor := collectClientDoctorWith(configPath, defaultProbeTarget, 0, true, false)
	webrtc := collectWebRTCCheck(configPath, dnsService)
	var networkReport *networkProbeReport
	if network {
		probeCtx, cancel := context.WithTimeout(ctx, networkTimeout)
		defer cancel()
		result := collectNetworkProbe(probeCtx)
		networkReport = assessNetworkProbe(result, expectedIPs)
	}
	rep := assembleLeakCheckReport(doctor, webrtc, networkReport)
	for _, check := range collectPlatformChecks(ctx) {
		rep.addCheck(check)
	}
	applyPlatformRisk(&rep)
	return rep
}

func assembleLeakCheckReport(doctor doctorReport, webrtc webrtcCheckReport, network *networkProbeReport) leakCheckReport {
	rep := leakCheckReport{
		Kind:            "leak",
		Version:         version.String(),
		SecretsRedacted: true,
		Risk:            "low",
		Doctor:          doctor,
		WebRTC:          webrtc,
		Network:         network,
	}
	if webrtc.Risk != "" {
		rep.Risk = maxRisk(rep.Risk, webrtc.Risk)
	}
	if network != nil && network.Risk != "" {
		rep.Risk = maxRisk(rep.Risk, network.Risk)
	}
	if doctor.hasFail() {
		rep.Risk = maxRisk(rep.Risk, "high")
	}
	rep.addCheck(aggregateDoctorServiceCheck(doctor))
	rep.addCheck(aggregateNamedCheck("dns", webrtc.Checks, "dns"))
	rep.addCheck(aggregateNamedCheck("udp", webrtc.Checks, "udp_path"))
	rep.addCheck(aggregateWebRTCCheck(webrtc))
	if network != nil {
		rep.addCheck(aggregateNamedCheck("egress_ipv4", network.Checks, "egress_ipv4"))
		rep.addCheck(aggregateNamedCheck("egress_ipv6", network.Checks, "egress_ipv6"))
		rep.addCheck(aggregateNamedCheck("dns_resolution", network.Checks, "dns_resolution"))
	} else {
		rep.addCheck(checkReport{
			Name:   "egress_ipv4",
			Status: "info",
			Detail: "not probed; pass --network --expected-ip <proxy-ip> to verify current exit path",
		})
	}
	rep.addCheck(checkReport{
		Name:   "ipv6",
		Status: "info",
		Detail: "local route/config risk only; verify on an IPv6-capable network with curl -6 or browser leak tests",
	})
	rep.addCheck(checkReport{
		Name:   "quic",
		Status: "info",
		Detail: "QUIC/HTTP3 follows UDP policy; use udp=proxy or block to avoid direct local path",
	})
	rep.NextActions = appendUnique(rep.NextActions, doctorNextActions(doctor)...)
	rep.NextActions = appendUnique(rep.NextActions, webrtc.NextActions...)
	rep.NextActions = appendUnique(rep.NextActions, "bx leakcheck")
	if network != nil {
		rep.NextActions = appendUnique(rep.NextActions, network.NextActions...)
	} else {
		rep.NextActions = appendUnique(rep.NextActions, "bx leak-check --network --json --expected-ip <proxy-ip>")
	}
	rep.Evidence = append(rep.Evidence, webrtc.Evidence...)
	if network != nil {
		rep.Evidence = append(rep.Evidence, network.Evidence...)
	}
	rep.OK = rep.Risk == "low"
	return rep
}

func applyPlatformRisk(rep *leakCheckReport) {
	for _, check := range rep.Checks {
		if check.Status == "warn" || check.Status == "fail" {
			switch check.Name {
			case "tailscale", "zerotier", "warp", "wireguard", "openvpn", "local_proxy", "packet_tunnel":
				rep.Risk = maxRisk(rep.Risk, "medium")
			}
		}
	}
	rep.OK = rep.Risk == "low"
}

func collectNetworkProbe(ctx context.Context) networkProbeResult {
	result := networkProbeResult{DNSName: "www.google.com"}
	updates := make(chan func(*networkProbeResult), 3)
	go func() {
		ip, err := fetchPublicIP(ctx, "tcp4", publicIPProbeV4URL)
		updates <- func(r *networkProbeResult) {
			if err != nil {
				r.IPv4Err = err.Error()
			} else {
				r.IPv4 = ip
			}
		}
	}()
	go func() {
		ip, err := fetchPublicIP(ctx, "tcp6", publicIPProbeV6URL)
		updates <- func(r *networkProbeResult) {
			if err != nil {
				r.IPv6Err = err.Error()
			} else {
				r.IPv6 = ip
			}
		}
	}()
	go func() {
		ips, err := net.DefaultResolver.LookupHost(ctx, result.DNSName)
		updates <- func(r *networkProbeResult) {
			if err != nil {
				r.DNSErr = err.Error()
			} else {
				r.DNSIPs = uniqueStrings(ips)
			}
		}
	}()
	for i := 0; i < 3; i++ {
		update := <-updates
		update(&result)
	}
	return result
}

func fetchPublicIP(ctx context.Context, network, target string) (string, error) {
	dialer := &net.Dialer{}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, address)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("response is not an IP address: %q", ip)
	}
	return ip, nil
}

func assessNetworkProbe(result networkProbeResult, expectedIPs []string) *networkProbeReport {
	report := &networkProbeReport{
		Kind:            "network",
		Version:         version.String(),
		SecretsRedacted: true,
		Risk:            "low",
		Result:          result,
	}
	expected := stringSet(expectedIPs)
	if result.IPv4 != "" {
		switch {
		case len(expected) == 0:
			report.addCheck("egress_ipv4", "warn", result.IPv4+" observed, but no expected proxy/VPS IP was provided", "pass --expected-ip <proxy-ip>")
			report.Risk = maxRisk(report.Risk, "medium")
			report.NextActions = appendUnique(report.NextActions, "bx leak-check --network --json --expected-ip <proxy-ip>")
		case expected[result.IPv4]:
			report.addCheck("egress_ipv4", "ok", result.IPv4+" matches expected proxy/VPS IP", "")
			report.Evidence = append(report.Evidence, "egress_ipv4: "+result.IPv4)
		default:
			report.addCheck("egress_ipv4", "fail", result.IPv4+" is not in expected proxy/VPS IPs", "check bx status, upstream proxy, and active VPN/network extensions")
			report.Risk = maxRisk(report.Risk, "high")
			report.NextActions = appendUnique(report.NextActions, "bx status --json", "bx logs")
		}
	} else if result.IPv4Err != "" {
		report.addCheck("egress_ipv4", "warn", result.IPv4Err, "retry with network access, or inspect bx logs")
		report.Risk = maxRisk(report.Risk, "medium")
	} else {
		report.addCheck("egress_ipv4", "info", "not observed", "")
	}

	if result.IPv6 != "" {
		ip := net.ParseIP(result.IPv6)
		if ip != nil && ip.To4() == nil {
			report.addCheck("egress_ipv6", "fail", result.IPv6+" public IPv6 egress observed", "disable IPv6 bypass or verify bx IPv6 capture")
			report.Risk = maxRisk(report.Risk, "high")
			report.NextActions = appendUnique(report.NextActions, "bx logs")
			report.Evidence = append(report.Evidence, "egress_ipv6: "+result.IPv6)
		} else {
			report.addCheck("egress_ipv6", "info", result.IPv6+" is an IPv4 address returned by the IPv6 probe; no public IPv6 address observed", "")
		}
	} else if result.IPv6Err != "" {
		report.addCheck("egress_ipv6", "ok", "no IPv6 egress observed: "+result.IPv6Err, "")
	} else {
		report.addCheck("egress_ipv6", "ok", "no IPv6 egress observed", "")
	}

	if len(result.DNSIPs) > 0 {
		if containsFakeIP(result.DNSIPs) {
			report.addCheck("dns_resolution", "ok", "fake-IP DNS response observed: "+strings.Join(result.DNSIPs, ", "), "")
			report.Evidence = append(report.Evidence, "dns_resolution: fake-IP")
		} else {
			report.addCheck("dns_resolution", "info", "resolver returned "+strings.Join(result.DNSIPs, ", "), "")
		}
	} else if result.DNSErr != "" {
		report.addCheck("dns_resolution", "warn", result.DNSErr, "bx dns status")
		report.Risk = maxRisk(report.Risk, "medium")
	} else {
		report.addCheck("dns_resolution", "info", "not observed", "")
	}
	report.OK = report.Risk == "low"
	return report
}

func (r *networkProbeReport) addCheck(name, status, detail, hint string) {
	r.Checks = append(r.Checks, checkReport{Name: name, Status: status, Detail: detail, Hint: hint})
}

func containsFakeIP(ips []string) bool {
	_, fakeNet, _ := net.ParseCIDR("198.18.0.0/15")
	for _, value := range ips {
		ip := net.ParseIP(value)
		if ip != nil && fakeNet.Contains(ip) {
			return true
		}
	}
	return false
}

func (r *leakCheckReport) addCheck(check checkReport) {
	if check.Name == "" {
		return
	}
	r.Checks = append(r.Checks, check)
}

func aggregateDoctorServiceCheck(doctor doctorReport) checkReport {
	for _, name := range []string{"service_active", "status_socket", "config_readable"} {
		if check := findReportCheck(doctor.Checks, name); check.Name != "" && check.Status != "ok" && check.Status != "info" {
			return checkReport{Name: "service", Status: check.Status, Detail: check.Detail, Hint: check.Hint}
		}
	}
	if check := findReportCheck(doctor.Checks, "service_active"); check.Name != "" {
		return checkReport{Name: "service", Status: check.Status, Detail: check.Detail, Hint: check.Hint}
	}
	return checkReport{Name: "service", Status: "info", Detail: "not checked"}
}

func aggregateNamedCheck(name string, checks []checkReport, source string) checkReport {
	check := findReportCheck(checks, source)
	if check.Name == "" {
		return checkReport{Name: name, Status: "info", Detail: "not checked"}
	}
	return checkReport{Name: name, Status: check.Status, Detail: check.Detail, Hint: check.Hint}
}

func aggregateWebRTCCheck(webrtc webrtcCheckReport) checkReport {
	for _, name := range []string{"browser_unexpected_public_ip", "browser_local_ip", "browser_public_ip"} {
		check := findReportCheck(webrtc.Checks, name)
		if check.Name == "" {
			continue
		}
		status := check.Status
		if webrtc.Risk == "high" && status != "fail" {
			status = "fail"
		}
		detail := check.Detail
		if webrtc.LeakProof != "" {
			detail = strings.TrimSpace(webrtc.LeakProof + " " + detail)
		}
		return checkReport{Name: "webrtc", Status: status, Detail: detail, Hint: check.Hint}
	}
	if check := findReportCheck(webrtc.Checks, "browser_candidates"); check.Name != "" {
		detail := strings.TrimSpace(webrtc.LeakProof + " " + check.Detail)
		return checkReport{Name: "webrtc", Status: check.Status, Detail: detail, Hint: check.Hint}
	}
	return checkReport{Name: "webrtc", Status: "info", Detail: "not checked"}
}

func findReportCheck(checks []checkReport, name string) checkReport {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return checkReport{}
}

func collectObserve(ctx context.Context, duration, interval time.Duration, scenario string) observeReport {
	if duration <= 0 {
		duration = 30 * time.Second
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if interval > duration {
		interval = duration
	}
	var samples []stats.Report
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if rep, err := readStatusReport(); err == nil {
		samples = append(samples, rep)
	} else {
		return observeErrorReport(duration, scenario, err)
	}
	for {
		select {
		case <-ctx.Done():
			rep := assessObserveWindow(samples, duration, scenario)
			rep.Error = ctx.Err().Error()
			rep.Hint = "retry bx observe with a shorter duration"
			rep.OK = false
			rep.Risk = maxRisk(rep.Risk, "medium")
			return rep
		case <-deadline.C:
			if rep, err := readStatusReport(); err == nil {
				samples = append(samples, rep)
			}
			return assessObserveWindow(samples, duration, scenario)
		case <-ticker.C:
			if rep, err := readStatusReport(); err == nil {
				samples = append(samples, rep)
			}
		}
	}
}

func observeErrorReport(duration time.Duration, scenario string, err error) observeReport {
	rep := observeReport{
		Kind:            "observe",
		Version:         version.String(),
		SecretsRedacted: true,
		Risk:            "high",
		Scenario:        normalizeObserveScenario(scenario),
		DurationMS:      duration.Milliseconds(),
		Error:           err.Error(),
		Hint:            "sudo bx up; bx logs --json",
	}
	rep.TestSteps = observeTestSteps(rep.Scenario, duration)
	rep.addCheck("status_socket", "fail", err.Error(), "sudo bx up")
	rep.Recommendations = append(rep.Recommendations, "Start bx, then reproduce the app issue while bx observe is running.")
	return rep
}

func assessObserveWindow(samples []stats.Report, duration time.Duration, scenarioValue ...string) observeReport {
	scenario := normalizeObserveScenario("")
	if len(scenarioValue) > 0 {
		scenario = normalizeObserveScenario(scenarioValue[0])
	}
	rep := observeReport{
		Kind:            "observe",
		Version:         version.String(),
		SecretsRedacted: true,
		Risk:            "low",
		Scenario:        scenario,
		DurationMS:      duration.Milliseconds(),
		Samples:         len(samples),
	}
	rep.TestSteps = observeTestSteps(scenario, duration)
	if len(samples) == 0 {
		rep.Risk = "high"
		rep.addCheck("samples", "fail", "no status samples", "sudo bx up")
		rep.Recommendations = append(rep.Recommendations, "Start bx and run observe while reproducing the problem.")
		return rep
	}
	start := samples[0]
	end := samples[len(samples)-1]
	rep.Start = &start
	rep.End = &end
	rep.Delta = snapshotDelta(start.Snapshot, end.Snapshot)
	if !end.TunnelHealthy {
		rep.Risk = maxRisk(rep.Risk, "high")
		rep.addCheck("tunnel", "fail", "tunnel unhealthy", "bx logs --json")
		rep.Recommendations = append(rep.Recommendations, "Inspect bx logs before changing routing or app rules.")
	} else {
		rep.addCheck("tunnel", "ok", fmt.Sprintf("healthy latency %dms", end.LatencyMS), "")
	}
	newConns := rep.Delta.Proxy + rep.Delta.Direct + rep.Delta.Blocked
	if newConns == 0 && rep.Delta.BytesUp == 0 && rep.Delta.BytesDown == 0 {
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.addCheck("activity", "warn", "no new connections or traffic observed", "reproduce the app issue while observe is running")
		rep.Recommendations = append(rep.Recommendations, "Start the video/app action first, then run bx observe for 30 seconds during the failure.")
	} else {
		rep.addCheck("activity", "ok", fmt.Sprintf("connections +%d, traffic up +%s down +%s", newConns, observeBytes(rep.Delta.BytesUp), observeBytes(rep.Delta.BytesDown)), "")
		rep.Evidence = append(rep.Evidence, fmt.Sprintf("delta proxy=%d direct=%d blocked=%d udp_blocked=%d", rep.Delta.Proxy, rep.Delta.Direct, rep.Delta.Blocked, rep.Delta.UDPBlocked))
	}
	if rep.Delta.UDPBlocked > 0 {
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.addCheck("udp_blocks", "warn", fmt.Sprintf("%d UDP packets/connections blocked", rep.Delta.UDPBlocked), "bx realtime status; bx logs --json")
		rep.Recommendations = append(rep.Recommendations, "If video or meeting media is affected, check UDP relay health and bx logs before changing split rules.")
	} else {
		rep.addCheck("udp_blocks", "ok", "0 new UDP blocks", "")
	}
	if rep.Delta.Blocked > 0 {
		rep.Risk = maxRisk(rep.Risk, "medium")
		rep.addCheck("blocked", "warn", fmt.Sprintf("%d new blocked connections", rep.Delta.Blocked), "bx logs --json")
		rep.Recommendations = append(rep.Recommendations, "Blocked connections during reproduction usually mean fail-closed protection or an unsupported path; inspect logs.")
	} else {
		rep.addCheck("blocked", "ok", "0 new blocked connections", "")
	}
	totalRouted := rep.Delta.Proxy + rep.Delta.Direct
	if totalRouted > 0 {
		proxyRatio := float64(rep.Delta.Proxy) / float64(totalRouted)
		if proxyRatio > 0.9 && rep.Delta.Direct == 0 {
			rep.addCheck("split_shape", "info", "all observed routed connections went through proxy", "")
			rep.Recommendations = append(rep.Recommendations, scenarioProxyRecommendation(scenario))
		} else {
			rep.addCheck("split_shape", "ok", fmt.Sprintf("proxy %d direct %d", rep.Delta.Proxy, rep.Delta.Direct), "")
		}
	} else {
		rep.addCheck("split_shape", "info", "no routed proxy/direct decisions observed", "")
	}
	rep.OK = rep.Risk == "low"
	return rep
}

func normalizeObserveScenario(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "video", "realtime":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "general"
	}
}

func observeTestSteps(scenario string, duration time.Duration) []string {
	seconds := int(duration.Seconds())
	if seconds <= 0 {
		seconds = 30
	}
	switch scenario {
	case "video":
		return []string{
			"Start bx observe before reproducing the video issue.",
			fmt.Sprintf("Within the next %d seconds, open the affected app and try to play the problematic video.", seconds),
			"Keep the app on the failing screen until observe finishes.",
			"Review activity, udp_blocks, blocked, and split_shape checks.",
		}
	case "realtime":
		return []string{
			"Start bx observe before joining or testing the call.",
			fmt.Sprintf("Within the next %d seconds, speak or send realtime media so UDP/QUIC paths are exercised.", seconds),
			"Keep the call active until observe finishes.",
			"Review udp_blocks, blocked, and traffic deltas.",
		}
	default:
		return []string{
			"Start bx observe, then reproduce the problem during the observation window.",
			"Keep the affected app active until observe finishes.",
			"Review activity, blocked, udp_blocks, and split_shape checks.",
		}
	}
}

func scenarioProxyRecommendation(scenario string) string {
	switch scenario {
	case "video":
		return "For China app/CDN video, all-proxy traffic often means the CDN may be exiting overseas; verify whether those domains should be direct in router/client rules."
	case "realtime":
		return "For realtime apps, all-proxy traffic is expected only when UDP relay is healthy; if media stutters, inspect UDP transport health and logs."
	default:
		return "For China-only apps or CDN video, verify whether their domains should be direct in router/client rules."
	}
}

func (r *observeReport) addCheck(name, status, detail, hint string) {
	r.Checks = append(r.Checks, checkReport{Name: name, Status: status, Detail: detail, Hint: hint})
}

func snapshotDelta(start, end stats.Snapshot) stats.Snapshot {
	return stats.Snapshot{
		Active:     end.Active,
		Proxy:      nonNegativeDelta(start.Proxy, end.Proxy),
		Direct:     nonNegativeDelta(start.Direct, end.Direct),
		Blocked:    nonNegativeDelta(start.Blocked, end.Blocked),
		UDPBlocked: nonNegativeDelta(start.UDPBlocked, end.UDPBlocked),
		BytesUp:    nonNegativeDelta(start.BytesUp, end.BytesUp),
		BytesDown:  nonNegativeDelta(start.BytesDown, end.BytesDown),
	}
}

func nonNegativeDelta(start, end int64) int64 {
	if end < start {
		return 0
	}
	return end - start
}

func observeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
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
	rep.NextActions = appendUnique(rep.NextActions, "bx leakcheck")
	rep.OK = rep.Risk == "low"
	return rep
}

func (r *webrtcCheckReport) addCheck(name, status, detail, hint string) {
	r.Checks = append(r.Checks, checkReport{Name: name, Status: status, Detail: detail, Hint: hint})
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

// setupDisposition 决定 bx setup 该怎么落配置。
type setupDisposition int

const (
	// setupWriteFresh:还没有配置,写一份全新的。
	setupWriteFresh setupDisposition = iota
	// setupUpdateTransports:已有配置,**只换传输链接**,其余一个字不动。
	setupUpdateTransports
	// setupOverwrite:--force,整份重写(会丢掉用户手写的策略等设置)。
	setupOverwrite
)

// decideSetupDisposition 的核心约束:没有 --force 就绝不整份重写已有配置。
//
// 旧行为是「配置已存在就拒绝」,而拒绝发生在 15 秒连通探测**之后**,于是用户先看到
// 一个绿勾再看到一行错误,读起来像「成功了,附带说明」;随后 bx up 用旧配置起来
// 显示 Protected,用户完全有理由相信自己换过服务器了(真机事故 2026-08-06)。
// 现在这条路直接可用:换传输保留其余,不再需要 --force,也就没有那次误导。
func decideSetupDisposition(configExists, force bool) setupDisposition {
	switch {
	case !configExists:
		return setupWriteFresh
	case force:
		return setupOverwrite
	default:
		return setupUpdateTransports
	}
}

// reportTransportChange 把「从什么换成了什么」并排打出来。
//
// 只说「已更新」不够:用户此前正是在看不出配置有没有真的改的情况下,以为自己换了
// 服务器而实际没换。链接自带凭据,故一律经 redactLink 打码。
func reportTransportChange(before setup.TransportsBefore, links []string, udpTransport string) {
	for _, line := range transportChangeLines(before, links, udpTransport) {
		fmt.Println(line)
	}
}

// transportChangeLines 是「换传输之后告诉用户什么」的**全部判断**。
//
// 抽成纯函数是因为这里有一条不显眼但会静默废掉 UDP 的判定(见 StaleUDPTransport),
// 而 reportTransportChange 直接 Println,测试进不去。
func transportChangeLines(before setup.TransportsBefore, links []string, udpTransport string) []string {
	var out []string
	oldMain := before.Server
	if oldMain == "" && len(before.Transports) > 0 {
		oldMain = before.Transports[0]
	}
	newMain := links[0]
	if oldMain == newMain {
		out = append(out, fmt.Sprintf("• 主传输不变:%s", redactLink(newMain)))
	} else {
		out = append(out, fmt.Sprintf("• 主传输:%s → %s", redactLink(oldMain), redactLink(newMain)))
	}
	if len(links) > 1 {
		out = append(out, fmt.Sprintf("• 容灾传输共 %d 条", len(links)))
	}
	switch {
	case udpTransport == "":
		if before.UDPTransport != "" {
			// **这里是那个静默陷阱。** 「保持不变」听起来无害,而如果这条 UDP
			// 传输本来跟旧服务器同机,它现在指着一台不属于你的机器:
			// UDP 健康检查失败 → fail-closed 全阻断 → 网页能开、语音和 QUIC 全废,
			// 而 bx status 显示 Protected(主隧道确实健康)。
			if host, stale := setup.StaleUDPTransport(before, newMain); stale {
				out = append(out,
					fmt.Sprintf("⚠ UDP 传输仍指向旧服务器 %s —— 它跟着上一台机器走的,现在那台已经不是你的出口了", host),
					"  后果:UDP(微信语音、QUIC、NTP)会被 fail-closed 全部阻断,而 bx status 仍显示 Protected",
					"  处置:带上新服务器的 UDP 链接重跑 `bx setup --udp <hysteria2://…> <bx://…>`(**flag 必须在链接之前**),",
					"        或者从 /etc/bx/config.yaml 里删掉 udp.transport 这一行(UDP 会改走主传输)")
			} else {
				out = append(out, fmt.Sprintf("• UDP 传输保持不变:%s", redactLink(before.UDPTransport)))
			}
		}
	case before.UDPTransport == udpTransport:
		out = append(out, fmt.Sprintf("• UDP 传输不变:%s", redactLink(udpTransport)))
	default:
		out = append(out, fmt.Sprintf("• UDP 传输:%s → %s", redactLink(before.UDPTransport), redactLink(udpTransport)))
	}
	out = append(out, "• 配置里的其余设置(分流策略、模式、列表等)未改动")
	return out
}

// checkSetupArgs 拒绝多余的位置参数。
//
// urfave/cli 沿用 Go flag 包语义:**遇到第一个位置参数就停止解析 flag**。于是
//
//	sudo bx setup '<链接>' --udp '<链接>'
//
// 里的 --udp 会变成两个普通位置参数,被**静默忽略**——命令看起来完全成功,而
// udp.transport 一个字没改。真机 2026-08-06/07 在 Mac 与 ws-via-vps 上都是这么
// 敲的,结果 UDP 传输一直停在旧服务器(那台的旧 IP 早已不可达)。
//
// 静默吞掉用户明确写出来的意图是最坏的一类失败:没有任何线索指向真正的原因。
// bx setup 只接受一个链接,多出来的一律报错,并点名那个被吞掉的 flag。
func checkSetupArgs(args []string) error {
	if len(args) <= 1 {
		return nil
	}
	for _, arg := range args[1:] {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		return fmt.Errorf("%s 写在了链接后面,会被静默忽略(flag 必须在链接之前)。\n正确写法:sudo bx setup %s '<值>' '<链接>'", arg, arg)
	}
	return fmt.Errorf("bx setup 只接受一个链接,多给了 %d 个参数;若要传 flag,必须写在链接之前", len(args)-1)
}

func setupAction(c *cli.Context) error {
	arg := c.Args().First()
	if arg == "" {
		return fmt.Errorf("用法: sudo bx setup <客户端链接>")
	}
	if err := checkSetupArgs(c.Args().Slice()); err != nil {
		return err
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
	if lat, perr := setup.ProbeServer(config.DefaultDataDir, link, c.String("probe"), 15*time.Second); perr != nil {
		if c.Bool("strict") {
			return fmt.Errorf("连通检测失败: %w", perr)
		}
		fmt.Printf("⚠️  连通检测未通过(仍写配置,稍后可排查): %v\n", perr)
	} else {
		fmt.Printf("✅ 服务器连通,延迟 %dms\n", lat)
	}
	_, statErr := os.Stat(cfgPath)
	switch decideSetupDisposition(statErr == nil, c.Bool("force")) {
	case setupUpdateTransports:
		before, err := setup.UpdateTransports(cfgPath, configLinks, udpTransport)
		if err != nil {
			return err
		}
		reportTransportChange(before, configLinks, udpTransport)
	default:
		if err := setup.WriteConfig(cfgPath, configLinks, udpTransport, c.Bool("force")); err != nil {
			return err
		}
	}
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	if unifiedLayoutActive() {
		if err := install.WriteGuardianUnit(install.GuardianExecutable(), abs); err != nil {
			return fmt.Errorf("写入 Guardian 服务失败: %w", err)
		}
		fmt.Println("✓ 统一布局:保留 CLI bridge,Guardian 指向 runtime")
		if err := postSetupAutostart(); err != nil {
			return fmt.Errorf("设默认开机自启: %w", err)
		}
		fmt.Printf("✅ 配置已写好 %s,Guardian 已指向统一 runtime。下一步:sudo bx up\n", cfgPath)
		return nil
	}
	if unifiedLayoutDegraded() {
		return errors.New(unifiedRepairHint)
	}
	bin, err := install.SelfInstall()
	if err != nil {
		return fmt.Errorf("安装 bx 到 PATH: %w", err)
	}
	if err := install.WriteUnit(buildExecStart(bin, abs)); err != nil {
		return err
	}
	if err := postSetupAutostart(); err != nil {
		return fmt.Errorf("设默认开机自启: %w", err)
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
		&cli.BoolFlag{Name: "no-hijack", Usage: "分步验证:起隧道+TUN+引擎但不劫持路由/不设 DNS/不装 WFP(系统网络零改动,真机 bring-up 用)"},
	}
}

func runAction(c *cli.Context) error {
	cfg, err := loadConfig(c.String("config"))
	if err != nil {
		return err
	}
	opts := optsFromFlags(c)
	// Windows:若由 SCM 作为服务拉起,须走 svc.Run 上报状态;Stop 时 cancel ctx 触发 Run 的
	// defer 全量还原。控制台调试(bx run 手敲)则 isWindowsService()=false,照常前台跑。
	if isWindowsService() {
		return runAsWindowsService(func(ctx context.Context) error {
			return supervisor.Run(ctx, cfg, opts)
		})
	}
	return supervisor.Run(c.Context, cfg, opts)
}

// trayAction 启动系统托盘(仅 Windows 有实现;其它平台返回清晰错误,见 tray_other.go)。
func trayAction(c *cli.Context) error { return tray.Run() }

func debugTunFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "tun", Value: "bx0", Usage: "TUN 设备名"},
		&cli.StringFlag{Name: "tun-addr", Value: "198.51.100.1/30", Usage: "TUN 接口地址(仅记录;debug-tun 不配地址)"},
		&cli.UintFlag{Name: "mtu", Value: 1500},
		&cli.DurationFlag{Name: "test-timeout", Usage: "死手:到点自动退出并移除 TUN(可选;debug-tun 不改路由,风险低)"},
	}
}

// debugTunAction 只建 TUN 适配器 hold 到 Ctrl+C(不起隧道/不碰路由),真机隔离验证 wintun+wgbridge。
func debugTunAction(c *cli.Context) error {
	ctx, cancel := context.WithCancel(c.Context)
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() { <-sig; cancel() }()
	if d := c.Duration("test-timeout"); d > 0 {
		t := time.AfterFunc(d, cancel)
		defer t.Stop()
	}
	return supervisor.DebugTUN(ctx, c.String("tun"), c.String("tun-addr"), uint32(c.Uint("mtu")))
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
		NoHijack:        c.Bool("no-hijack"),
		// resolveConfigPath 与 bx direct/proxy 写配置走同一解析(含 /etc 缺失时的 ~/.config 兜底),
		// 否则热重载会去重读一个和 CLI 写入不同的文件,rule 永远热生效不了。
		ConfigPath: resolveConfigPath(c.String("config")),
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
	if runtime.GOOS == "darwin" {
		return macOSUpAction(c)
	}
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
	stepDone("服务", upStepLabel())
	if rep, err := readStatusReport(); err == nil {
		printUpSummary(rep)
		return nil
	}
	fmt.Println(upDoneMessage())
	return nil
}

func downAction(c *cli.Context) (err error) {
	defer autoArchiveAfterClientCommand("down", &err, true)
	if runtime.GOOS == "darwin" {
		return macOSDownAction(c)
	}
	if err := install.Disable(); err != nil {
		return err
	}
	fmt.Println(downDoneMessage())
	return nil
}

type guardianRecoveryClient interface {
	RequestRecovery(context.Context, guardian.RecoveryRequest) (guardian.RecoverySnapshot, error)
	CurrentRecovery(context.Context) (guardian.RecoverySnapshot, error)
}

type reconnectDependencies struct {
	client          guardianRecoveryClient
	output          io.Writer
	wait            func(context.Context, time.Duration) error
	legacyReconnect func(context.Context) error
}

type legacyReconnectOutput struct {
	State   string `json:"state"`
	Stage   string `json:"stage"`
	Reason  string `json:"reason"`
	Attempt int    `json:"attempt"`
}

func defaultReconnectDependencies() reconnectDependencies {
	return reconnectDependencies{
		client: guardian.NewClient(guardian.SocketPath),
		output: os.Stdout,
		wait:   waitForReconnectPoll,
		legacyReconnect: func(ctx context.Context) error {
			if !install.UnitInstalled() {
				return fmt.Errorf("尚未配置。先运行: sudo bx setup <client-link>")
			}
			if _, err := supervisor.ReconnectControlContext(ctx, statusSocketPath()); err != nil {
				return err
			}
			return nil
		},
	}
}

// reconnectAction asks Guardian to serialize the complete protected path recovery.
// A legacy direct-Core reconnect remains only for installations with no reachable Guardian.
func reconnectAction(c *cli.Context) (err error) {
	deps := defaultReconnectDependencies()
	if c.Bool("check") {
		_, err := deps.client.CurrentRecovery(c.Context)
		if err == nil {
			return nil
		}
		var unavailable *guardian.UnavailableError
		if !errors.As(err, &unavailable) {
			return err
		}
		ok, err := supervisor.SupportsSafeReconnect(statusSocketPath())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("运行中的 bx 不支持安全重连")
		}
		return nil
	}
	defer autoArchiveAfterClientCommand("reconnect", &err, true)
	return reconnectWithDependencies(c.Context, c.Bool("json"), deps)
}

func reconnectWithDependencies(ctx context.Context, jsonOutput bool, deps reconnectDependencies) error {
	if deps.client == nil {
		return fmt.Errorf("Guardian recovery client unavailable")
	}
	if deps.output == nil {
		deps.output = io.Discard
	}
	if deps.wait == nil {
		deps.wait = waitForReconnectPoll
	}

	snapshot, err := deps.client.RequestRecovery(ctx, guardian.RecoveryRequest{Reason: "manual"})
	if err != nil {
		var unavailable *guardian.UnavailableError
		if errors.As(err, &unavailable) && deps.legacyReconnect != nil {
			if !jsonOutput {
				fmt.Fprintln(deps.output, "• Protection  Reconnecting")
			}
			if err := deps.legacyReconnect(ctx); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(deps.output, legacyReconnectOutput{
					State: "succeeded", Stage: "legacy_core", Reason: "manual", Attempt: 1,
				})
			}
			fmt.Fprintln(deps.output, "✓ Protection  Reconnected")
			return nil
		}
		return err
	}
	if !jsonOutput {
		fmt.Fprintln(deps.output, "• Protection  Reconnecting")
	}

	delay := 250 * time.Millisecond
	for {
		switch snapshot.State {
		case "succeeded":
			if jsonOutput {
				return writeJSON(deps.output, snapshot)
			}
			fmt.Fprintln(deps.output, "✓ Protection  Reconnected")
			return nil
		case "failed":
			if jsonOutput {
				if err := writeJSON(deps.output, snapshot); err != nil {
					return err
				}
			}
			code := snapshot.ErrorCode
			if code == "" {
				code = "recovery_failed"
			}
			return fmt.Errorf("recovery failed: %s", code)
		case "ignored":
			if jsonOutput {
				if err := writeJSON(deps.output, snapshot); err != nil {
					return err
				}
			}
			return fmt.Errorf("recovery was not started: %s", snapshot.Stage)
		}

		if err := deps.wait(ctx, delay); err != nil {
			return fmt.Errorf("recovery %s is still running: %w", snapshot.ID, err)
		}
		delay = 500 * time.Millisecond
		current, err := deps.client.CurrentRecovery(ctx)
		if err != nil {
			return fmt.Errorf("recovery %s is still running: %w", snapshot.ID, err)
		}
		if current.ID == snapshot.ID {
			snapshot = current
		}
	}
}

func waitForReconnectPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// restartAction 保留旧命令,但语义与 reconnect 完全一致。
func restartAction(c *cli.Context) error {
	return reconnectAction(c)
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
	rep, err := readClientStatusReport()
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
	fmt.Print(renderClientStatus(rep))
	return nil
}

// observation 向系统现问事实。它是注入的,好让 status 的组装逻辑免 root 可测:
// 真实实现要跑 route/networksetup 并连 Core 的控制 socket。
type observation func(context.Context) observe.ObservedState

// observeTimeout 给整轮观测封顶。观测要跑若干外部命令,而 bx status 是用户
// (和 agent)在出问题时最先敲的命令——它宁可少答一项,也绝不能挂住。
const observeTimeout = 5 * time.Second

func readClientStatusReport() (clientStatusReport, error) {
	return readClientStatusReportWithObserver(
		readStatusReport, readGuardianStatus, runtime.GOOS, observerForPlatform(runtime.GOOS),
	)
}

// observerForPlatform 只在观测原语真实存在的平台上附观测。
//
// supervisor.LookupRoute 目前只有 darwin 实现,DNS 接管探测同理。在其余平台
// 观测只会产出一份全 Unknown 的结果 + 5 条恒定的「该项无法观测」divergence,
// 而它换不来任何新事实——tunnel_healthy 本就来自同一个控制 socket,已经在扁平
// 字段里了。那种噪声会把 divergence 训练成用户和 agent 学会忽略的东西,
// 正好毁掉它唯一的价值。字段缺席是诚实的「没问」;满屏「无法观测」则是把静态
// 平台限制伪装成每次调用都新发生的差异。
// observerForPlatform 决定哪些平台附上观测层。
//
// **原来只有 darwin,理由是「别处一个观测原语都没有」——那个理由已经不成立了。**
// supervisor.LookupRoute 三平台都有(2026-08-13 归位),于是 capture / barrier 在
// Linux 上问得出来,core_socket 与 tunnel_healthy 本来就走控制 socket。
// 唯一不成立的 dns_managed 由 observe.NotApplicable 显式声明,不会变成每次调用
// 都吐一条的「无法观测」——那正是当初拒绝在别处附观测的理由。
//
// Windows 暂不附:那边的 leakcheck 采集刚补上、且**真机未验**,而观测层一旦附上就
// 进 `bx status --json` 的 divergence,一条没验证过的观测比没有观测更坏。
func observerForPlatform(platform string) observation {
	switch platform {
	case "darwin", "linux":
		return liveObservation
	default:
		return nil
	}
}

// liveObservation 是生产观测:向真实系统现问,不改动任何状态。
func liveObservation(ctx context.Context) observe.ObservedState {
	return observe.Observe(ctx, observe.LiveDeps(statusSocketPath()))
}

func readClientStatusReportWith(
	readCore func() (stats.Report, error),
	readGuardian func() (guardian.Status, error),
	platform string,
) (clientStatusReport, error) {
	return readClientStatusReportWithObserver(readCore, readGuardian, platform, nil)
}

func readClientStatusReportWithObserver(
	readCore func() (stats.Report, error),
	readGuardian func() (guardian.Status, error),
	platform string,
	observer observation,
) (clientStatusReport, error) {
	core, coreErr := readCore()
	status, guardianErr := readGuardian()
	if guardianErr != nil {
		if coreErr != nil {
			return clientStatusReport{}, coreErr
		}
		status = guardianStatusFallback(core, platform)
	} else if coreErr != nil {
		if platform != "darwin" {
			return clientStatusReport{}, coreErr
		}
		switch status.Protection {
		case guardian.ProtectionOff, guardian.ProtectionBlocked, guardian.ProtectionNeedsAttention, guardian.ProtectionRecovering:
		default:
			status.Protection = guardian.ProtectionNeedsAttention
		}
		return attachObservation(assemblePartialClientStatusReport(status), status, observer), nil
	}
	report := assembleClientStatusReportWithCoreForPlatform(&core, "local_status_socket", status, platform)
	return attachObservation(report, status, observer), nil
}

// attachObservation 把观测与差异并列挂到报告上,绝不用观测改写既有的信念字段。
//
// 观测失败不让 bx status 失败:Observe 本身从不返回错误,失败的项以 Unknown
// 加原因出现在观测里,并由 Diverge 转成一条自解释的 divergence。
func attachObservation(report clientStatusReport, status guardian.Status, observer observation) clientStatusReport {
	if observer == nil {
		return report
	}
	ctx, cancel := context.WithTimeout(context.Background(), observeTimeout)
	defer cancel()
	observed := observer(ctx)
	report.Observed = &observed
	// 挂起必须传进去:不传的话,一台正在升级的机器会冒出一条「没有保护在跑」的
	// 假分歧,而 divergence 一旦被训练成噪声,它唯一的价值就没了。
	intent := observe.Intent{Desired: string(status.Desired)}
	if hold := status.MaintenanceHold; hold != nil {
		intent.Hold = &observe.HoldIntent{Reason: hold.Reason, ExpiresAt: hold.ExpiresAt}
	}
	report.Divergence = observe.Diverge(
		intent,
		observed,
		observe.Believed{
			Protection: report.ProtectionState,
			Phase:      report.Phase,
			LastError:  status.LastError,
		},
	)
	return report
}

func guardianStatusFallback(core stats.Report, platform string) guardian.Status {
	if platform == "darwin" {
		return guardian.Status{
			Protection: guardian.ProtectionNeedsAttention,
			Recovery: guardian.RecoverySnapshot{
				State:     "failed",
				Stage:     "unknown",
				ErrorCode: "recovery_unavailable",
			},
		}
	}
	protection := guardian.ProtectionProtected
	if !core.TunnelHealthy {
		protection = guardian.ProtectionBlocked
	}
	return guardian.Status{
		Protection: protection,
		Recovery:   guardian.RecoverySnapshot{State: "idle", Stage: "idle"},
	}
}

func readGuardianStatus() (guardian.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return guardian.NewClient(guardian.SocketPath).Status(ctx)
}

func assembleClientStatusReport(core stats.Report, status guardian.Status) clientStatusReport {
	return assembleClientStatusReportWithCore(&core, "local_status_socket", status)
}

func assemblePartialClientStatusReport(status guardian.Status) clientStatusReport {
	return assembleClientStatusReportWithCore(nil, "unavailable", status)
}

func assembleClientStatusReportWithCore(core *stats.Report, evidence string, status guardian.Status) clientStatusReport {
	return assembleClientStatusReportWithCoreForPlatform(core, evidence, status, runtime.GOOS)
}

func assembleClientStatusReportWithCoreForPlatform(core *stats.Report, evidence string, status guardian.Status, platform string) clientStatusReport {
	protection := normalizedGuardianProtectionState(status, platform)
	switch status.Recovery.State {
	case "accepted", "running":
		if protection != guardian.ProtectionNeedsAttention {
			protection = guardian.ProtectionRecovering
		}
	case "failed":
		if protection != guardian.ProtectionNeedsAttention {
			protection = guardian.ProtectionBlocked
		}
	}
	return clientStatusReport{
		Report:               core,
		CoreAvailable:        core != nil,
		CoreEvidence:         evidence,
		Desired:              string(status.Desired),
		ProtectionState:      protection,
		NetworkGeneration:    status.NetworkGeneration,
		Recovery:             status.Recovery,
		DNSState:             status.DNSState,
		DNSManaged:           status.DNSManaged,
		DNSService:           status.DNSService,
		Phase:                string(status.Phase),
		CoreVersion:          status.CoreVersion,
		GuardianVersion:      status.GuardianVersion,
		RuntimeVersion:       status.RuntimeVersion,
		Reconcile:            status.Reconcile,
		GuardianCapabilities: status.Capabilities,
		MaintenanceHold:      status.MaintenanceHold,
	}
}

func renderClientStatus(report clientStatusReport) string {
	label := clientProtectionLabel(report.ProtectionState)
	if !report.CoreAvailable || report.Report == nil {
		var b strings.Builder
		fmt.Fprintln(&b, "bx protection (partial)")
		fmt.Fprintf(&b, "  Guardian %s\n", label)
		if shouldShowUpdateMessage(report.Phase) {
			fmt.Fprintf(&b, "  更新中(%s):网络可能短暂暂停,完成后自动恢复\n", report.Phase)
		}
		fmt.Fprintln(&b, "  Core     Unavailable")
		fmt.Fprintln(&b, "  Protection Core status/protection cannot be verified")
		writeClientDNS(&b, report.DNSState, report.DNSService)
		if report.NetworkGeneration != "" {
			fmt.Fprintf(&b, "  Network %s\n", report.NetworkGeneration)
		}
		writeClientRecovery(&b, report.Recovery)
		writeClientMaintenanceHold(&b, report)
		writeClientReconcile(&b, report)
		return b.String()
	}

	var b strings.Builder
	fmt.Fprintln(&b, "bx protection")
	fmt.Fprintf(&b, "  Status  %s\n", label)
	if shouldShowUpdateMessage(report.Phase) {
		fmt.Fprintf(&b, "  更新中(%s):网络可能短暂暂停,完成后自动恢复\n", report.Phase)
	}
	if report.NetworkGeneration != "" {
		fmt.Fprintf(&b, "  Network %s\n", report.NetworkGeneration)
	}
	writeClientDNS(&b, report.DNSState, report.DNSService)
	writeClientRecovery(&b, report.Recovery)
	writeClientMaintenanceHold(&b, report)
	writeClientReconcile(&b, report)
	b.WriteString(stats.Render(*report.Report))
	return b.String()
}

// maintenanceHoldStatusPrefix 是那一行的完整行首,含把它对进 Status/Network/DNS
// 那一列的补白(那一列宽 8)。与 reconcileStatusPrefix 同理由:让「这一行在不在」
// 的判据是整段行首,而不是一个短到会撞上别处的字。
const maintenanceHoldStatusPrefix = "  Hold    "

// writeClientMaintenanceHold 解释「用户要保护、此刻却没有」。
//
// **没有挂起就一个字都不写。** 一行常驻的「没有挂起」会把这一项训练成噪声,
// 而它一年里只该出现几分钟 —— 与 observerForPlatform 那道 darwin 门、以及
// writeClientReconcile 第三态逐字相同的理由。这里刻意**不**按能力声明去写
// 「这一版不认识挂起」:那句话对每一台旧机器都恒真,正是同一种噪声。
//
// 只渲染**正在生效**的那一张:Guardian 发布的就只有它(过期的不进 Status),
// 所以这一行出现 = 保护此刻被有意压制,不是坏了。
func writeClientMaintenanceHold(b *strings.Builder, report clientStatusReport) {
	hold := report.MaintenanceHold
	if hold == nil {
		return
	}
	fmt.Fprintf(b, "%s维护挂起(%s),%s —— 保护此刻被有意压制%s\n",
		maintenanceHoldStatusPrefix, maintenanceHoldReasonLabel(hold.Reason),
		maintenanceHoldRemaining(time.Until(hold.ExpiresAt)), maintenanceHoldIntentNote(report.Desired))
}

// maintenanceHoldIntentNote 说明挂起结束之后会发生什么,而那取决于 desired。
//
// desired=on 是常态:挂起一过期,机器就回到「用户要保护」那条线上。
//
// **desired=off 配一张挂起不是矛盾,是过渡升级的样子**:新 CLI 在停机之前武装了
// 挂起,而服务那次停机的**旧** Guardian 不认识挂起、无条件写下了 off
// (restoreIntentAfterHoldUnawareStop 随即把它写回 on,写不成时就停在这里)。
// 这时印一句「desired 仍是 off」等于把最要紧的那半句咽回去:**过期不会恢复
// 保护**(设计取舍五:过期买到的是「不再压制」,不是「自动修好」),而盘上写着
// off,于是下一次开机也不会。用户需要知道的是那条出路。
func maintenanceHoldIntentNote(desired string) string {
	if desired == string(guardian.DesiredOn) {
		return ",desired 仍是 on"
	}
	return ";但盘上的 desired 是 " + desired +
		" —— 挂起过期后保护不会自动恢复(下次开机也不会),需要保护请执行 sudo bx up"
}

// maintenanceHoldReasonLabel 把稳定标识符翻成一句人话,**并保留标识符本身**。
//
// 两者都要:标识符是 Guardian 日志里 grep 得到的那个词(`reason=legacy_upgrade`),
// 只印人话会让用户没法把这一行与日志对上;只印标识符则是把内部词汇丢给用户,
// 「legacy_upgrade」对着屏幕的人什么也不是。未知来由如实只印标识符 —— 编一句
// 人话出来比不编更糟。
func maintenanceHoldReasonLabel(reason string) string {
	labels := map[string]string{
		guardian.HoldReasonUpgrade:       "升级中",
		guardian.HoldReasonLegacyUpgrade: "升级中,由旧版升级记录迁移而来",
	}
	if label := labels[reason]; label != "" {
		return label + "," + reason
	}
	return reason
}

// maintenanceHoldRemaining 渲染剩余时间,**下限钳到零**。
//
// 剩余时间是渲染那一刻现算的,而挂起是 Guardian 在一次 RPC 之前判定「还武装着」
// 的;两者之间必然有间隔,挂起恰好在这个窗口里过期时,time.Until 会是负数,于是
// 这一行会印出「-2s 后失效」并同时断言保护是被有意压制的 —— 一句自相矛盾的话。
func maintenanceHoldRemaining(remaining time.Duration) string {
	if remaining <= 0 {
		return "刚刚失效(保护应随即恢复;若没有,见 sudo tail -50 " + install.GuardianStderrLogPath + ")"
	}
	return remaining.Round(time.Second).String() + " 后失效"
}

func writeClientDNS(b *strings.Builder, state guardian.DNSState, service string) {
	fmt.Fprintf(b, "  DNS     %s\n", guardianDNSLabel(state, service))
}

// reconcileStatusPrefix 是那一行的完整行首,**含把它对进 Status/Network/DNS
// 那一列的补白**(那一列宽 8:`Status  `/`Network `/`DNS     `)。
//
// 标签因此只能用 7 个字符以内的词,`Reconcile` 放不下 —— 挤宽一格会让这一行
// 在整块状态里歪出来。取 `Loop`:这一行说的正是「那条循环最近一轮看到了什么」,
// 而「循环」恰恰是它与旁边那份一次性观测(observed/divergence)唯一的区别。
//
// 常量带着补白,是为了让「这一行在不在」的测试判据是**整段行首**而不是光秃秃
// 一个 `Loop` —— 后者短到会撞上别处的字。
const reconcileStatusPrefix = "  Loop    "

// writeClientReconcile 把 Guardian 那条只观察调谐环的最近一轮判断写成一行。
//
// **三态,每一态的理由都不一样,不能合并:**
//
//   - 有报告 ⇒ 写一行,**哪怕这一轮什么差异都没有**。静默正是这一项要消灭的
//     歧义:一个装了但没跑的循环,与一台健康机器,产出的证据一模一样;唯一
//     分得开二者的是「有没有一份带时刻的报告」,所以那份报告必须说出口。
//   - 没有报告、但对面声明了这条能力 ⇒ 「尚未完成第一轮观测」。这是新版
//     Guardian 刚起来的样子(循环先睡一个基础周期再观测),它与「循环死了」
//     在别处看不出差别,只能由这一行说明。
//   - 没有报告、也没声明能力(旧版 Guardian,或者根本没有这条循环的平台)
//     ⇒ **什么都不写**。理由与 observerForPlatform 那道 darwin 门逐字相同:
//     一行永远存在的「无法观测」会把用户和 agent 训练成忽略这个字段,而那
//     正好毁掉它唯一的价值。字段缺席是诚实的「没问」。
func writeClientReconcile(b *strings.Builder, report clientStatusReport) {
	line, ok := clientReconcileLine(report, time.Now())
	if !ok {
		return
	}
	b.WriteString(reconcileStatusPrefix + line + "\n")
}

func clientReconcileLine(report clientStatusReport, now time.Time) (string, bool) {
	// At 为零的报告不算「跑过一轮」:零时刻正是「从没跑过」的形状,把它渲染成
	// 「最近观测 490000h 前 · 无差异」既荒唐,又恰好是本任务要消灭的那句假话。
	if round := report.Reconcile; round != nil && !round.At.IsZero() {
		return reconcileRoundSummary(*round, now), true
	}
	if !slices.Contains(report.GuardianCapabilities, guardian.CapabilityReconcileReport) {
		return "", false
	}
	return "尚未完成第一轮观测", true
}

func reconcileRoundSummary(round guardian.ReconcileReport, now time.Time) string {
	prefix := fmt.Sprintf("最近观测 %s 前 · ", reconcileElapsed(now, round.At))
	// **停滞优先于内容。** 一份两倍退避上限都没更新过的报告,说明循环已经停了、
	// 或者每一轮都在 panic(炸掉的轮次刻意不写报告)。此时照常渲染那一轮的内容,
	// 等于把一份冻住的快照说成「最近的一轮」——而它的内容多半正是「无差异」,
	// 也就是把一条死掉的循环渲染成一台健康的机器。用户没有理由知道那个上限,
	// 所以必须由这一行说出口。
	if now.Sub(round.At) > guardian.ReconcileStaleAfter {
		return prefix + "报告已停滞 —— 调谐环可能已停止(查 /var/log/bx-guard.err.log)"
	}
	return prefix + reconcileRoundVerdict(round) + reconcileRoundEvidence(round)
}

// reconcileRoundVerdict 说这一轮判断出了什么。
func reconcileRoundVerdict(round guardian.ReconcileReport) string {
	switch {
	case round.Held != "":
		// 被栅栏挡住的一轮**没有做判断**,不是「判断出没有差异」——soak 要数的
		// 正是这种轮次有多少。
		return "被 " + round.Held + " 挡住" + heldFenceHint(round.Held)
	case len(round.Actions) > 0:
		return "本会提议 " + strings.Join(round.Actions, ",")
	case round.UnchangedRounds == 0:
		// 干净 + 连续未变轮数为 0 ⇒ 这一轮的判断与上一轮**不同**。写成「连续 0 轮
		// 未变」在最该说清楚的那一刻反而最难读。
		//
		// **注意它有两种来由,措辞必须同时对这两种成立**:一是某件事刚刚被解决掉;
		// 二是 Guardian 刚起来、这是第一轮(循环的 previous 初值虽然也是「干净」,
		// 但每轮还带着观测质量与 Core 普查,首轮几乎必然与初值不同)。故这里说
		// 「刚转为」而不说「刚被解决」—— 后者在开机后的头 30 秒会是一句假话。
		return "无差异(本轮刚转为无差异)"
	default:
		return fmt.Sprintf("无差异(连续 %d 轮未变)", round.UnchangedRounds)
	}
}

// reconcileRoundEvidence 附上这一轮的**证据质量**:哪些项根本没问出来,以及那次
// 只读进程扫描测到了什么。
//
// **这不是装饰。** 三项探测全失败时判断恰好等于一台健康机器的判断(Unknown 一律
// 「什么都不做」),于是上面那句 verdict 会原样写成「无差异(连续 N 轮未变)」——
// 一台永久失明的机器读起来与一台健康机器一模一样,而 soak 的全部结论就建立在
// 这个区别上。Core 进程数同理:它是本阶段要测的那个误报率,「测到 1 个」与
// 「压根没测成」必须分得开。
func reconcileRoundEvidence(round guardian.ReconcileReport) string {
	evidence := ""
	if len(round.Unobservable) > 0 {
		evidence += fmt.Sprintf(" · %d 项未观测到(%s)",
			len(round.Unobservable), strings.Join(round.Unobservable, ","))
	}
	if round.CoreScan.Measured {
		evidence += fmt.Sprintf(" · 扫到 %d 个 Core 进程", round.CoreScan.Cores)
		return evidence
	}
	reason := round.CoreScan.Reason
	if reason == "" {
		reason = "unknown"
	}
	// 「没测成」绝不能渲染成「扫到 0 个」:那是把「问不出来」写成「问过、没有」。
	return evidence + " · Core 进程未测成(" + reason + ")"
}

// reconcileElapsed 把「多久以前」渲染成人话,并把负数收敛成 0。
//
// Guardian 与 CLI 是两个进程:时钟回拨、或者一份刚生成的报告被读到,都会让 At
// 落在 now 之后,而「-2s 前」只会让人怀疑整份状态。
func reconcileElapsed(now, at time.Time) string {
	elapsed := now.Sub(at)
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Round(time.Second).String()
}

func normalizedGuardianProtectionState(status guardian.Status, platform string) string {
	if platform == "darwin" && status.Protection == guardian.ProtectionProtected &&
		(status.DNSState != guardian.DNSManaged || !status.DNSManaged) {
		return guardian.ProtectionNeedsAttention
	}
	return status.Protection
}

// guardianDNSLabel renders DNS state for a human. It must stay word-for-word in
// sync with dnsPresentation in apps/macos/BxMenu/Sources/BxMenu/StatusPresentation.swift
// (pinned by TestGuardianDNSLabelMatchesMenuWording); machines read the raw
// enum from `bx status --json` instead.
func guardianDNSLabel(state guardian.DNSState, service string) string {
	switch state {
	case guardian.DNSManaged:
		if service != "" {
			return "Handled by bx (" + service + ")"
		}
		return "Handled by bx"
	case guardian.DNSUnmanaged:
		return "Not managed"
	default:
		return "Status unavailable"
	}
}

func shouldShowUpdateMessage(phase string) bool {
	switch phase {
	case "prepared", "barrier_active", "activating", "rolling_back":
		return true
	default:
		return false
	}
}

func clientProtectionLabel(protection string) string {
	label := "Protected"
	switch protection {
	case guardian.ProtectionRecovering:
		label = "Reconnecting"
	case guardian.ProtectionBlocked:
		label = "Blocked"
	case guardian.ProtectionNeedsAttention:
		label = "Repair Required"
	case guardian.ProtectionOff:
		label = "Off"
	case guardian.ProtectionStarting:
		label = "Starting"
	}
	return label
}

func writeClientRecovery(b *strings.Builder, recovery guardian.RecoverySnapshot) {
	if recovery.State != "" && recovery.State != "idle" {
		fmt.Fprintf(b, "  Recovery %s stage=%s attempt=%d", recovery.ID, recovery.Stage, recovery.Attempt)
		if recovery.ErrorCode != "" {
			fmt.Fprintf(b, " error_code=%s", recovery.ErrorCode)
		}
		fmt.Fprintln(b)
	}
}

func recoveryDoctorCheck(snapshot guardian.RecoverySnapshot) checkReport {
	status := "ok"
	hint := ""
	switch snapshot.State {
	case "accepted", "running":
		status = "info"
	case "failed":
		status = "warn"
		hint = "bx logs --json; bx reconnect (troubleshooting only)"
	}
	detail := fmt.Sprintf("state=%s stage=%s attempt=%d", snapshot.State, snapshot.Stage, snapshot.Attempt)
	if snapshot.ErrorCode != "" {
		detail += " error_code=" + snapshot.ErrorCode
	}
	return checkReport{Name: "network_recovery", Status: status, Detail: detail, Hint: hint}
}

func guardianDNSDoctorCheck(status guardian.Status) checkReport {
	state := status.DNSState
	if state == "" {
		state = guardian.DNSUnknown
	}
	detail := fmt.Sprintf("state=%s managed=%t", state, status.DNSManaged)
	if status.DNSService != "" {
		detail += " service=" + status.DNSService
	}
	if state == guardian.DNSManaged && status.DNSManaged {
		return checkReport{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	return checkReport{
		Name:   "guardian_dns",
		Status: "fail",
		Detail: detail,
		Hint:   "sudo bx up; bx logs",
	}
}

func readStatusReport() (stats.Report, error) {
	rep, err := supervisor.FetchStatusReport(statusSocketPath())
	if err != nil {
		return stats.Report{}, fmt.Errorf("连接 bx 失败(bx 是否在运行?): %w", err)
	}
	return rep, nil
}

func printUpSummary(rep stats.Report, statuses ...guardian.Status) {
	fmt.Print(renderUpSummary(rep, statuses...))
}

func renderUpSummary(rep stats.Report, statuses ...guardian.Status) string {
	state := "Protected"
	if !rep.TunnelHealthy {
		state = "Needs Attention"
	} else if hasBlockingWarning(rep.Warnings) {
		state = "Needs Attention"
	} else if len(statuses) > 0 && normalizedGuardianProtectionState(statuses[0], runtime.GOOS) != guardian.ProtectionProtected {
		state = "Needs Attention"
	}
	var b strings.Builder
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "bx is on")
	fmt.Fprintf(&b, "  Status     %s\n", state)
	fmt.Fprintf(&b, "  Tunnel     %dms\n", rep.LatencyMS)
	fmt.Fprintf(&b, "  UDP Relay  %s\n", onOff(rep.UDPMode == "proxy"))
	if len(statuses) > 0 {
		fmt.Fprintf(&b, "  DNS        %s\n", guardianDNSLabel(statuses[0].DNSState, statuses[0].DNSService))
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintf(&b, "  Warning    %s\n", rep.Warnings[0].Detail)
	}
	return b.String()
}

// hasBlockingWarning 只把 error 级告警当作降级总状态的理由。
//
// warn 级是共存 advisory(Tailscale、系统代理、其他 packet tunnel):它已经单独
// 占一行显示,把头条状态一并降级是重复的,而且会让完全正常的保护看起来像出了
// 故障——用户正是据此判断要不要排查。真机 2026-08-06:隧道 322ms 健康、Guardian
// Protected、观测层 divergence 为空,却因为 Tailscale 在跑而每次 up 都显示
// Needs Attention。
func hasBlockingWarning(warnings []stats.Warning) bool {
	for _, warning := range warnings {
		if strings.EqualFold(strings.TrimSpace(warning.Severity), "error") {
			return true
		}
	}
	return false
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

type realtimePostChangePlan struct{ Message string }

func planRealtimePostChange(unitInstalled bool, activeState string) realtimePostChangePlan {
	if !unitInstalled {
		return realtimePostChangePlan{Message: "尚未安装服务。下次运行 sudo bx up 时生效。"}
	}
	if activeState == "active" {
		return realtimePostChangePlan{Message: "当前保护会话保持运行;新的 UDP 策略将在下次 sudo bx up 时生效。"}
	}
	return realtimePostChangePlan{Message: "bx 当前未运行。下次 sudo bx up 时生效。"}
}

func applyRealtimePostChange(_ *cli.Context) error {
	installed := install.UnitInstalled()
	active := "inactive"
	if installed {
		active = serviceState("is-active", install.ServiceName)
	}
	plan := planRealtimePostChange(installed, active)
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
	if c.Bool("json") {
		if c.Bool("follow") {
			return fmt.Errorf("--json 不能和 --follow 同时使用")
		}
		if c.Bool("archive") {
			return fmt.Errorf("--json 不能和 --archive 同时使用")
		}
		raw, err := install.TailLogs(install.ServiceName, c.Int("lines"))
		rep := logsReportFromTail("client", c.Int("lines"), raw, err)
		rep.Paths = install.ClientLogPaths()
		return writeJSON(os.Stdout, rep)
	}
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

func logsReportFromTail(service string, lines int, raw string, err error) logsReport {
	if lines <= 0 {
		lines = 100
	}
	rep := logsReport{
		OK:              err == nil,
		Kind:            "logs",
		Version:         version.String(),
		SecretsRedacted: true,
		Service:         service,
		Lines:           lines,
		Text:            raw,
	}
	if err != nil {
		rep.Error = err.Error()
		rep.Hint = "try sudo bx logs"
	}
	return rep
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
	// includePlatformChecks=false:归档只是把已发生的状态落盘留档,不是又一次体检——
	// text-mode doctor 已经在 doctorAction 里跑过一遍 collectPlatformChecks,`up`/
	// `down`/`reconnect` 退出时也不该白付一次 pgrep/netstat/scutil 的 exec 开销;
	// 代价是 doctor.json 里没了 coexistence 那几条(它们是即时环境探测,不是日志材料,
	// 丢了不影响事后排障)。
	doctor := collectClientDoctorWith(defaultConfigPath, defaultProbeTarget, 0, true, false)
	if err := writeJSONFile(filepath.Join(dir, "doctor.json"), doctor); err != nil {
		return "", err
	}
	recovery := guardian.RecoverySnapshot{State: "idle", Stage: "idle"}
	if status, err := readGuardianStatus(); err == nil {
		recovery = status.Recovery
	} else if runtime.GOOS == "darwin" {
		recovery = guardianStatusFallback(stats.Report{}, runtime.GOOS).Recovery
	}
	if err := persistRecoverySnapshot(filepath.Join(dir, "recovery.json"), recovery); err != nil {
		return "", err
	}
	// **一个文件读不到,绝不让整个诊断包失败。**
	//
	// 这条教训 Guardian 日志那侧早就学过(见下),而客户端这侧还是
	// `return "", err`。ClientLogPaths 现在含 Guardian 的两条(0600 root-only,
	// 那才是当前 Core 在写的),于是非 root 跑 `bx doctor` 时整个归档直接失败 ——
	// 一个在最需要它的时候拒绝工作的诊断工具。
	// **按源路径去重,不按文件名。** ClientLogPaths 与 guardianArchiveLogPaths
	// 在生产上指向同一批绝对路径(两边都含 Guardian 的两条),按文件名去重会把
	// 后一份**指向别处**的同名文件一起吞掉。
	seen := map[string]bool{}
	collectArchiveLog := func(src string) {
		if seen[src] {
			return
		}
		seen[src] = true
		dst := filepath.Join(dir, filepath.Base(src))
		if err := copyIfExists(src, dst); err != nil {
			_ = os.Remove(dst)
			_ = os.WriteFile(dst+".unavailable.txt", []byte(err.Error()+"\n"), 0o600)
			return
		}
		// 同名的前一份可能留下过占位说明(比如 0600 的那份读不到,而另一条
		// 路径指向的同名文件读得到)——成功之后把它清掉,免得诊断包里同时
		// 有内容和一句「读不到」。
		_ = os.Remove(dst + ".unavailable.txt")
	}
	for _, src := range install.ClientLogPaths() {
		collectArchiveLog(src)
	}
	// Guardian 日志(0600 root-only)才有 Guardian 失败的完整原因;Core 日志
	// 对一次 `bx up` 500 可能一个字都没写——事故里翻诊断包只拿到陈旧 Core
	// 日志就是这么来的。但非 root 归档读不到它属正常,只留说明,绝不让整个
	// 诊断包因此失败。
	for _, src := range guardianArchiveLogPaths() {
		collectArchiveLog(src)
	}
	return dir, nil
}

// guardianArchiveLogPaths 是 install.GuardianLogPaths 的可替换入口(测试注入)。
var guardianArchiveLogPaths = install.GuardianLogPaths

func persistRecoverySnapshot(path string, snapshot guardian.RecoverySnapshot) error {
	snapshot.Detail = ""
	switch snapshot.ErrorCode {
	case "", "capture_invalid", "capture_missing", "network_unavailable", "recovery_canceled", "recovery_failed", "recovery_unavailable", "transport_unavailable", "underlay_rebind_failed", "underlay_unavailable", "verification_failed":
	default:
		snapshot.ErrorCode = ""
	}
	if snapshot.State == "failed" && snapshot.ErrorCode == "" {
		snapshot.ErrorCode = "recovery_failed"
	}
	return writeJSONFile(path, snapshot)
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
	if commandErr != nil && *commandErr != nil {
		if err := os.WriteFile(filepath.Join(dir, "command-error.txt"), []byte(persistedCommandError(*commandErr)+"\n"), 0o600); err != nil {
			if announce {
				fmt.Fprintf(os.Stderr, "Diagnostics archive failed: %v\n", err)
			}
			return
		}
	}
	if announce || (commandErr != nil && *commandErr != nil) {
		fmt.Fprintf(os.Stderr, "Diagnostics archived: %s\n", dir)
	}
}

func persistedCommandError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded.Error()
	}
	return "client command failed"
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

func readShare(dir, name string) (shareInfo, error) {
	cfg, err := readServerConfig(shareConfigPath(dir, name))
	if err != nil {
		return shareInfo{}, err
	}
	return shareInfo{Name: name, Config: cfg}, nil
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

// darwinGuardianServiceName 是 macOS 上真正承担 bx 生命周期的 launchd 服务。
//
// 统一布局下 Core 不是 launchd 服务(由 Guardian 起停),所以 doctor 绝不能去查
// install.UnitInstalled() 那两个 Core plist——那必然三条 FAIL,而保护好得很
// (真机 2026-08-06)。install.ServiceName 是 systemd 的 "bx.service",同样不该
// 印在 macOS 上。
const darwinGuardianServiceName = "com.getbx.bx.guard"

// doctorLineSpec 是一条待输出的 doctor 行,抽出来是为了让判定逻辑可测。
type doctorLineSpec struct {
	Status string
	Key    string
	Value  string
}

// darwinServiceDoctorLines 由 Guardian 的安装/活跃状态产出 doctor 的服务三行。
//
// launchd 没有 systemd 那种 enabled 与 active 的分离:Guardian 的 plist 带
// RunAtLoad+KeepAlive,装上即开机自启,故 enabled 直接由 installed 决定。
func darwinServiceDoctorLines(installed, active bool) []doctorLineSpec {
	lines := []doctorLineSpec{{boolStatus(installed), "service installed", darwinGuardianServiceName}}
	activeState := "inactive"
	if active {
		activeState = "active"
	}
	lines = append(lines, doctorLineSpec{serviceStatusFromState("is-active", activeState), "service active", activeState})
	if !active {
		lines = append(lines, doctorLineSpec{"hint", "logs", "bx logs"})
	}
	enabledState := "disabled"
	if installed {
		enabledState = "enabled"
	}
	lines = append(lines, doctorLineSpec{serviceStatusFromState("is-enabled", enabledState), "service enabled", enabledState})
	return lines
}

// serviceDoctorChecks 选 doctor 的服务三条问谁。**纯派发,两个生产者都注入**
// —— 唯一的目的是让「macOS 走 Guardian」这件事本身可以被单测钉住,不必碰真实
// 文件系统。此前这段判断内联在 collectClientDoctorWith 里:把它改回问 Core /
// systemd,整套测试照样绿(变异实测),而两个生产者各自的单测也照样绿 ——
// 被测的是生产者,没人测**接的是哪一个**。
func serviceDoctorChecks(goos string, guardian, systemd func() []checkReport) []checkReport {
	if goos == "darwin" {
		return guardian()
	}
	return systemd()
}

// guardianServiceChecks / systemdServiceChecks 是两个生产者的实际接线(会碰
// 文件系统与 launchctl/systemctl),故与上面的派发分开,派发那半可测。
func guardianServiceChecks() []checkReport {
	return darwinServiceChecks(install.GuardianInstalled(), install.GuardianActive())
}

func systemdServiceChecks() []checkReport {
	activeState := serviceState("is-active", install.ServiceName)
	enabledState := serviceState("is-enabled", install.ServiceName)
	return []checkReport{
		{Name: "service_installed", Status: boolStatus(install.UnitInstalled()), Detail: install.ServiceName, Hint: "sudo bx setup <client-link>"},
		{
			Name:   "service_active",
			Status: serviceStatusFromState("is-active", activeState),
			Detail: activeState,
			Hint:   hintForState(activeState, "sudo bx up", "bx logs"),
		},
		{Name: "service_enabled", Status: serviceStatusFromState("is-enabled", enabledState), Detail: enabledState, Hint: "sudo bx up"},
	}
}

// darwinServiceChecks 是 darwinServiceDoctorLines 的机器可读兄弟:同一份判据
// (Guardian 的安装/活跃状态),产出 doctor --json 的服务三条。
//
// launchd 没有 systemd 那种 enabled 与 active 的分离:Guardian 的 plist 带
// RunAtLoad+KeepAlive,装上即开机自启,故 enabled 直接由 installed 决定。
// 检查**名字**与 linux 那三条保持一致(service_installed/active/enabled),
// 消费方按名字取值,不该因为平台不同而找不到。
func darwinServiceChecks(installed, active bool) []checkReport {
	installHint := ""
	if !installed {
		installHint = "sudo bx setup <client-link>"
	}
	activeState := "inactive"
	if active {
		activeState = "active"
	}
	enabledState := "disabled"
	if installed {
		enabledState = "enabled"
	}
	return []checkReport{
		{Name: "service_installed", Status: boolStatus(installed), Detail: darwinGuardianServiceName, Hint: installHint},
		{
			Name:   "service_active",
			Status: serviceStatusFromState("is-active", activeState),
			Detail: activeState,
			Hint:   hintForState(activeState, "sudo bx up", "bx logs"),
		},
		{Name: "service_enabled", Status: serviceStatusFromState("is-enabled", enabledState), Detail: enabledState, Hint: "sudo bx up"},
	}
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
	conn, err := net.DialTimeout("unix", statusSocketPath(), 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

func statusSocketPath() string {
	if path := strings.TrimSpace(os.Getenv("BX_STATUS_SOCKET")); path != "" {
		return path
	}
	return supervisor.SockPath
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
	return fmt.Errorf("timeout waiting for %s", statusSocketPath())
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

// buildExecStartWith 是 buildExecStartForGOOS 的纯函数内核:darwin 分支该用哪个可执行文件由
// 调用方解析(生产是 install.GuardianExecutable(),测试注入),避免测试依赖本机是否已装统一 runtime。
func buildExecStartWith(goos, bin, configPath, guardianExecutable string) string {
	switch goos {
	case "darwin":
		// darwin:Guardian LaunchDaemon 指向由调用方解析的可执行文件(生产经 install.GuardianExecutable(),
		// 统一 runtime 已装好时优先指向它,否则回落 BinPath;测试注入显式路径)。
		return fmt.Sprintf("%s guardian --config %s --listen-dns %s", guardianExecutable, configPath, darwinDNSListen)
	case "windows":
		// Windows 服务 BinaryPathName:bin/config 路径含空格(Program Files / ProgramData),
		// 必须加引号,交 install.commandLineFields 按引号拆回 exepath+args。
		return fmt.Sprintf(`"%s" run -c "%s"`, bin, configPath)
	default:
		return fmt.Sprintf("%s run -c %s", bin, configPath)
	}
}

func buildExecStartForGOOS(goos, bin, configPath string) string {
	// darwin 分支通过 install.GuardianExecutable() 解析实际该用哪个可执行文件,
	// 其他 GOOS 的 guardianExecutable 参数无关。
	guardianExecutable := ""
	if goos == "darwin" {
		guardianExecutable = install.GuardianExecutable()
	}
	return buildExecStartWith(goos, bin, configPath, guardianExecutable)
}

func uninstallAction(c *cli.Context) error {
	if runtime.GOOS == "darwin" {
		return uninstallDarwinAction(c)
	}
	if err := install.Uninstall(); err != nil {
		return err
	}
	fmt.Println("已卸载 bx 服务")
	return nil
}

// 出口探测用的域名。**一个都不许在 china 直连列表里** —— 在列表里的域名,
// 保护开着时走的是直连,于是探测报出来的是用户真实的 ISP 出口,而不是隧道出口。
//
// 后果不是「少一条信息」,是**方向相反的谎**:用户敲 bx doctor 想确认保护有没有
// 生效,看到自己的真实 IP,得出的结论正好是「bx 在漏」。一个在最需要它的时候
// 撒谎的诊断。
//
// **这个坑踩过两次。** CLAUDE.md 里那条教训正确地点名了 ifconfig.me 与 ip.sb,
// 却推荐用 api.ipify.org —— 而 ipify.org 同样在列表里(china_domain.txt:6045),
// 于是那条写错的推荐照着进了代码三处。所以这里不靠「记得选对」,靠
// TestPublicIPProbeDomainsAreNotChinaDirect 拿**真实的内嵌列表**逐个比对。
const (
	publicIPProbeV4URL = "https://icanhazip.com"
	// v6 用同一个主机名:icanhazip.com 同时有 A 与 AAAA,由 fetchPublicIP 传的
	// network("tcp6")决定实际走哪个族 —— 比换一个 v6 专用域名少一处要维护的常量。
	publicIPProbeV6URL = "https://icanhazip.com"
)

// publicIPProbeURLs 是按顺序尝试的出口探测地址。
var publicIPProbeURLs = []string{publicIPProbeV4URL, "https://ipinfo.io/ip"}

// heldFenceHint 给栅栏名字配一句**下一步**。
//
// 栅栏名是给日志与统计脚本用的稳定标识符,不是给人读的。此前
// `bx status` 只把它原样吐出来 —— 用户看到「被 ownership_uncertain 挡住」,
// 而那串字既不说明发生了什么,也不说明该做什么。
//
// **出路此前只挂在 500 响应上**(guardianCodeHints 是按失败码索引的),也就是说
// 只有主动去 `sudo bx up` 撞一次墙的人才看得到怎么脱身;被动看状态的人拿到的是
// 一个术语。而所有权不确定恰恰是那种**用户不主动撞就不知道自己卡住了**的状态:
// 保护没开、Guardian 在正常应答、status 一切看起来正常,只有这一行在说话。
//
// 措辞刻意不承诺:每次 up 都会重新求证,但系统里真有第二个 Core 时它**应该**
// 继续拒绝。
func heldFenceHint(held string) string {
	switch held {
	case "ownership_uncertain":
		return "(Guardian 拒绝再起一个 Core。`sudo bx up` 每次都会重新求证;" +
			"仍被拒就去看 " + install.GuardianStderrLogPath + " 里的 guardian_core_scan)"
	case "recovery_blocked":
		return "(启动恢复没做完。`sudo bx down` 仍然可用)"
	case "intent_unreadable":
		return "(读不出 /var/lib/bx 里的意图或维护挂起。见 " + install.GuardianStderrLogPath + ")"
	default:
		// path_recovery_in_flight / maintenance_hold 都是**过渡态**,自己会结束,
		// 给「下一步」反而会催人去动手。
		return ""
	}
}

// setupCommandLine 渲染一条**可以直接粘贴**的 bx setup 命令。
//
// **只有一处渲染,因为顺序是会咬人的。** urfave/cli 沿用 Go flag 语义:遇到
// 第一个位置参数就停止解析 flag,于是 `bx setup '<链接>' --udp '<值>'` 里的
// --udp 会被当成位置参数。bx 有一道守卫(checkSetupArgs)专门拒绝这种写法,
// 而 bx 自己曾在三处**生成**它 —— 包括 `bx server install` 与 `bx server deploy`
// 打给用户的下一步(2026-08-14 真机:照着敲直接被拒)。
func setupCommandLine(main, udp string) string {
	parts := []string{"sudo bx setup"}
	if strings.TrimSpace(udp) != "" {
		parts = append(parts, "--udp", "'"+udp+"'")
	}
	parts = append(parts, "'"+main+"'")
	return strings.Join(parts, " ")
}
