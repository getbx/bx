package guardian

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/supervisor"
)

// 本文件服务 /v1/servers:菜单栏那一侧的「有哪几台、现在在哪台、换一台」。
//
// **切换的编排不在这里。** 它在 supervisor.SwitchServer(武装 → 验证 → 确认),
// 与 `bx server use` 用的是同一份。这个仓库反复栽在「同一件事两份实现,一份改了
// 另一份没改,而两边测试都绿」上,所以这里只做 HTTP 与配置写盘。

type ServerEntry struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`
	UDPHost string `json:"udp_host,omitempty"`
	Current bool   `json:"current"`
	// Probe 只在这次请求做过探测时出现。**键缺席 = 没测过**,不是「测了没通」。
	Probe *ProbeReport `json:"probe,omitempty"`
	// PeakBPS 是观测到的峰值吞吐。**实际在跑的**那台来自 Core 的实时观测
	// (见 ServerListResponse.Running,那未必是配置里选的那一台),其余来自历史。
	// 0/缺席 = 从来没观测到过,**不是「跑不动」**。
	PeakBPS int64 `json:"peak_bps,omitempty"`
	// PeakAgeSeconds 是那次观测有多久了。**它和 PeakBPS 必须成对出现** ——
	// 一个不带年龄的历史数字读起来像现状,而所有者同意存历史的前提正是
	// 「界面上要标出来这是以前的」。0 = 不到一秒之前,也就是真的就在此刻 ——
	// **它由观测时刻算出来,绝不许写死**(见 attachThroughput:热切换不会把
	// Core 那块进程级速率表清零,写死 0 会让上一台的成绩冒充这一台的现状)。
	PeakAgeSeconds int64 `json:"peak_age_seconds,omitempty"`
}

// ProbeReport 是一台的探测结论。
//
// **Reachable 与 RTTMS 分开,而且 RTT 带 omitempty。** 把「没通」表达成 0 毫秒
// 会让界面显示一个漂亮的零 —— 零值读起来像一切正常,这个仓库反复禁止过。
type ProbeReport struct {
	// Measured 为 false 时这一轮没测成,Reachable 无意义 —— 别去读它。
	// **不带 omitempty**:键缺席读作「这一版 Guardian 没说」,而不是「没测成」,
	// 是客户端区分新旧 Guardian 的唯一信号,与 Status.Capabilities 同一条纪律。
	Measured  bool  `json:"measured"`
	Reachable bool  `json:"reachable"`
	RTTMS     int64 `json:"rtt_ms,omitempty"`
	// Error 是给人看的失败原因,**中文** —— 它的消费方是 `bx server list`。
	//
	// **菜单不许显示它。** 那个界面通篇英文,而 CJK 守卫只扫菜单自己的源码、
	// 扫不到从这里来的字符串;真机上「服务器关着」这条最常见的路径会在全英文
	// 菜单里显示一句中文。客户端改为只读下面那个码、自己出英文,Swift 侧
	// 干脆连这个键都不解 —— 让「显示它」在构造上不可能。
	Error string `json:"error,omitempty"`
	// ErrorCode 是同一件事的机器可读形式(supervisor.ProbeErr*),菜单据此出话。
	// 与 Error 成对:两者都缺席 = 这次没有失败可报。
	ErrorCode string `json:"error_code,omitempty"`
}

type ServerListResponse struct {
	Servers []ServerEntry `json:"servers"`
	Current string        `json:"current"`
	// Running 是 Core **此刻真正在用**的那一台,与 Current(配置里的选择)
	// **并列,绝不合并**。
	//
	// 两者不同**正是这里最有价值的一条诊断**:热切换是先写配置再切(反过来
	// 会留下「现在在 B、下次启动回 A」这种没人看得出来的不一致),所以切换
	// 失败的那一刻配置已经是 B 了。合成一个字段之后,「配置说 B、流量还从 A
	// 出去」就再也表达不出来 —— 与 bx status --json 里 desired / observed /
	// divergence 同一条纪律。
	//
	// **带 omitempty:问不出来时必须缺席。** 空串会被读成「没有在跑」,而
	// 真相是「没问出来」。Core 不可达、或者它报的主机在清单里对不上任何一台
	// (那就说不出名字来),都留空 —— 说不出来好过说错。
	Running    string `json:"running,omitempty"`
	ConfigPath string `json:"config_path"`
	// SingleServer 为真表示配置里**根本没有** `servers:` 这个键 —— 它是一份
	// 单服务器配置(`server:` / `transports:`),而不是一份空的清单。
	//
	// **两者在窗口里必须说不同的话。** `bx setup` 从不写 `servers:` 清单
	// (它写的是 `server:` 或 `transports:`),所以**每一个正常装好 bx 的用户**
	// 打开服务器窗口时清单都是空的 —— 而 bx 此刻正跑着一台服务器。对他说
	// 「还没有服务器」是一句当场就能被证伪的假话,`bx server list` 早就说对了
	// (「配置里没有服务器清单(还是单服务器配置)」),只有窗口没有这个区分。
	//
	// 判据是 `setup.ListServers` 返回的切片**是不是 nil**:键缺席时 readServers
	// 返回 nil,而 `servers: []` 返回一个长度为 0 的非 nil 切片。两者在 JSON 里
	// 都是 `[]`,所以这个区分必须由服务端说出来,客户端推不出来。
	//
	// **不带 omitempty**(与本文件其余新字段同一条纪律):键缺席读作「这一版
	// Guardian 没说」,客户端那时退回既有措辞,而不是编一句关于配置形状的话。
	SingleServer bool `json:"single_server"`
	// CurrentServer 是**当前那台**的主机与端口,**只在 `Servers` 里没有它的
	// 条目时出现** —— 也就是单服务器配置(`bx setup` 写出来的那种,根本没有
	// `servers:` 这个键,`Current` 因此也是空的)。
	//
	// 少了它,菜单在**最常见的那种配置上说不出服务器地址**:Core 起不来时那句
	// 可行动的话退化成「bx cannot reach your server」,没有地址、没有 `nc -z`。
	// CLI 那一半没有这个洞(它以 root 跑,直接 config.Parse 读 cfg.Server),
	// 而所有者自己的机器有 `servers:`,所以真机验收也照不出这一条。
	//
	// **为什么不是往 Servers 里塞一条**:那会让 serverListEmptyReason 从
	// 「这是单服务器配置,不是一份清单」退回 nil —— 那句话正是为这种配置刻意
	// 写的,而窗口还会摆出一行既切不过去也删不掉的服务器。
	//
	// **发布面一寸没扩**:它带的是 ServerEntry 早就在发的那几个字段,多服务器
	// 配置下每一条应答都带着它们;链接一个字节都不出门(与
	// TestServerListNeverShipsTheLinkItself 同一条)。名字空着是诚实的 ——
	// 单服务器配置里那台真的没有名字。
	CurrentServer *ServerEntry `json:"current_server,omitempty"`
	// CurrentServerRunning 答「Core 此刻跑的是不是 CurrentServer 那一台」,**只在
	// CurrentServer 存在时才可能出现**。
	//
	// 有清单时这件事由 Running(名字)说;而单服务器配置里那台**没有名字**,按名字
	// 比对永远对不上 —— 窗口一旦开始画这一台,它会在最常见的那种配置上天天说
	// 「bx could not confirm which server is running」。判据与 runningServerName
	// 同一份(hostMatches,按主机)。三态:true = 对上;false = Core 报的是别的主机
	// (配置改过而 Core 没重启 —— 那时说「在跑的就是它」是一句谎);**nil = Core
	// 没答话,键缺席,不编**。
	CurrentServerRunning *bool `json:"current_server_running,omitempty"`
	// Added 只在 add 应答里出现:最终写进清单的名字(用户给的,或按链接推导的)。
	// 界面靠它知道接下来该切换到哪一台 —— 自己再推一遍推导规则就是第二份判据。
	Added string `json:"added,omitempty"`
}

type serversRequest struct {
	// Action 空 = 换到 Name 那一台(这个端点最初唯一的动作,保持兼容)。
	// "add" = 把一台加进清单,**不动 current**。
	// "remove" = 从清单里删掉 Name 那一台(**当前那台一律拒绝**)。
	// "replace" = 就地换掉 Name 那一台的链接,**不动 current**(凭据轮换、
	// VPS 换了地址;这两件事今天只有手改 /etc/bx/config.yaml 或 `bx setup --force`)。
	Action string `json:"action,omitempty"`
	Name   string `json:"name"`
	Link   string `json:"link,omitempty"`
	UDP    string `json:"udp,omitempty"`
}

type switchResponse struct {
	Name string `json:"name"`
	Host string `json:"host"`
	// Applied 区分「已经在跑的那个实例也换过去了」与「只写进了配置」。
	//
	// **这两件事必须分开报。** 合成一个 ok 会让菜单在热切失败时说「已切换」,
	// 而流量还从原来那台出去 —— 正是 `bx server use` 第一版那句谎的形状。
	Applied bool `json:"applied"`
	// Outcome 是热切失败时的**结局码**,四种各一个(见 switchOutcomeCode)。
	//
	// **applied=false 远不止一种意思**,而此前四种共用一个常量:菜单只能说一句
	// 「没切过去,关了再开」—— 对「已生效但确认失败」那是假话(它切过去了,
	// 死手可能把它还原,用户得立刻处理),对「回滚也失败了」则轻描淡写了一次
	// 正在发生的断网。
	//
	// **带 omitempty**:成功时本就没有结局可说;而 applied=false 却没有这个键,
	// 读作「这一版 Guardian 不说结局」,不是「不知道是哪种」—— 与
	// Status.Capabilities 同一条区分新旧的纪律。
	Outcome string `json:"outcome,omitempty"`
}

// switchOutcomeCode 把 supervisor.SwitchServer 的四种结局映射成一个机器可读的码。
//
// **原始错误串一个字都不出门**(与 /v1/rules 的 409 同一条门规):那句话里带着
// 服务器名与 Core 报上来的细节,完整原因只写进 Guardian 日志,这个码是它唯一的
// 对外出口。
func switchOutcomeCode(err error) string {
	switch {
	case errors.Is(err, supervisor.ErrSwitchArmFailed):
		return "arm_failed"
	case errors.Is(err, supervisor.ErrSwitchRolledBack):
		return "rolled_back"
	case errors.Is(err, supervisor.ErrSwitchRollbackFailed):
		return "rollback_failed"
	case errors.Is(err, supervisor.ErrSwitchCommitFailed):
		return "commit_failed"
	}
	// 认不出的错误**不许套用四种里的任何一种**。说错了比不说更糟:一句
	// 「已回滚」会让用户以为流量还好好地走在原来那台上。
	return "servers_hot_switch_failed"
}

// serverSwitcher 是「让正在跑的实例换过去」这一步。注入进来是为了让顺序可测:
// 生产实现要连 Core 的控制 socket,测试里造不出来。
type serverSwitcher func(name, link, udp string) error

// serverProber 量一次到某台服务器的直连往返时间。注入是为了可测:生产实现要连
// Core 的控制 socket。
type serverProber func(host string, port int) (supervisor.ProbeResult, error)

// liveServerProbe 接到真 Core 上。
func liveServerProbe(host string, port int) (supervisor.ProbeResult, error) {
	return supervisor.ProbeControl(supervisor.SockPath, host, port)
}

// coreLiveStatus 是 Core 此刻的状态里这个端点要用的两件事。
//
// **两件必须一起取、且分开表达。** 峰值来自一块进程级速率表,它属于
// ServerHost 那一台;只取峰值不取主机,就只能把它挂到配置里选的那一台头上,
// 而热切换失败时那不是同一台。
type coreLiveStatus struct {
	// ServerHost 是 Core 报的「此刻主传输指向的主机」。
	ServerHost string
	// PeakBPS 是观测到的峰值吞吐,0 = 这段时间没人用它传东西(**不是「跑不动」**)。
	PeakBPS int64
	// PeakAt 是那个峰值**是什么时候量到的**,必须跟着 PeakBPS 一起带出来。
	//
	// **少了它,一次成功的切换照样撒谎。** Core 那块速率表是进程级的,热切换
	// 不会把它的峰值清零 —— A→B 切成功之后它报的还是 A 那个数,而把年龄写死成
	// 0 就等于说「刚刚在 B 上量到的」。这个时刻本来就在同一份 stats.Report 里,
	// 落盘那一半(recordThroughputOnce)一直在忠实地转发它,只有应答这一路把它
	// 扔掉了。
	PeakAt time.Time
}

// coreStatusReader 问一次 Core。
//
// **ok=false 是「没问出来」,与「问出来了但没有峰值」是两件事** —— 前者
// 连「在跑哪一台」都答不上来,后者答得上来。压成一个布尔就再也分不开。
type coreStatusReader func() (coreLiveStatus, bool)

// liveCoreStatus 接到真 Core 上。
func liveCoreStatus() (coreLiveStatus, bool) {
	rep, err := supervisor.FetchStatusReport(supervisor.SockPath)
	if err != nil {
		return coreLiveStatus{}, false
	}
	return coreLiveStatus{ServerHost: rep.Server, PeakBPS: rep.PeakBPS, PeakAt: rep.PeakAt}, true
}

// runningServerName 把 Core 报的主机翻成清单里的名字。
//
// **翻成名字而不是原样发主机**:Current 是名字,而这两个字段的全部价值就在于
// 能不能比对 —— 一个装主机、一个装名字,那个比对就得由每个消费方自己再造一遍。
//
// 对不上任何一台时返回空串:说不出名字就别说。退回配置里选的那一台恰恰是这条
// 改动要消灭的那句谎。
//
// **对上了两条同样是「说不出」。** Core 只报得出一个主机(RuntimeState 里没有
// 名字),而同一主机上挂两条记录是真会发生的:`bx server install --with-hysteria2`
// 出的那两条链接、凭据轮换时先加后删的那段过渡。此前这里取**第一条匹配**,于是
// 填实的 `●` 与「bx is actually using X right now.」落在错的那一台上、实时峰值
// 给了错的一行,而 recordThroughputOnce 会把那个数**按错的名字永久落盘** ——
// 一个自信的错答案。有歧义时宁可什么都不说:那时应答里 running 缺席,菜单按
// 「问不出来」渲染,一个字的谎都没有。
func runningServerName(entries []ServerEntry, host string) string {
	idx := hostMatches(entries, host)
	if len(idx) != 1 {
		return ""
	}
	return entries[idx[0]].Name
}

// hostMatches 是「Core 报的主机对上了哪几条」的**唯一**判据,runningServerName
// 与单服务器配置的 CurrentServerRunning 都经它 —— 两处各写一份比对,就会有一天
// 一处认大小写、一处不认。主机为空时一条都不算对上。
func hostMatches(entries []ServerEntry, host string) []int {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	var idx []int
	for i := range entries {
		if strings.EqualFold(strings.TrimSpace(entries[i].Host), host) {
			idx = append(idx, i)
		}
	}
	return idx
}

// liveServerSwitch 接到真 Core 上。
func liveServerSwitch(name, link, udp string) error {
	return supervisor.SwitchServer(supervisor.LiveSwitchDeps(), name, link, udp)
}

// serversHandler 服务 /v1/servers。
//
// **授权与 /v1/up、/v1/rules 同一道门。** 换服务器会改变出口 IP,是与开关保护
// 同一量级的动作;而菜单以 owner 身份跑,取一致才用得上。
func serversHandler(configPath string, ownerUID uint32, switchTo serverSwitcher, probe serverProber, coreStatus coreStatusReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "servers require owner or root peer"})
			return
		}
		if strings.TrimSpace(configPath) == "" {
			// 「没接线」不是「没有服务器」—— 后者会让菜单显示一个空清单,
			// 用户据此以为自己没配过服务器(与 /v1/rules 同一条纪律)。
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "servers unavailable: no config path"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			serveServerList(w, configPath, coreStatus)
		case http.MethodPost:
			applyServerSwitch(w, r, configPath, switchTo, probe, coreStatus)
		default:
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func serveServerList(w http.ResponseWriter, configPath string, coreStatus coreStatusReader) {
	respondWithServerList(w, configPath, coreStatus, "")
}

// serversSnapshot 组装这个端点每一条应答都该有的东西:清单、配置里选的那台、
// **Core 此刻真正在跑的那台**、以及挂好的吞吐。
//
// **每一条应答都必须经它,不只是 GET 那一条。** Running 上一轮只加在 GET 那条
// 路上,而 `bx server list --test` 走的是 probe 那条 —— 于是一台 Guardian 与
// Core 都完全健康的机器,每次都被告知「实际在跑的是哪一台这次没问到」。
// **修法不许是「让消费方记住上一次的值」**:那是客户端状态,会陈旧,而它陈旧
// 的那一刻恰好就是热切换刚失败、这个字段最有价值的一刻。服务端每一次说实话。
func serversSnapshot(configPath string, coreStatus coreStatusReader) (ServerListResponse, error) {
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		return ServerListResponse{}, err
	}
	entries := serverEntries(list, current)
	// 历史读不出来不影响清单:它是诊断数据,而清单是功能。
	past, herr := loadThroughputState(DefaultThroughputHistoryPath)
	if herr != nil {
		log.Printf("guardian_throughput_history_unreadable err=%v", herr)
	}
	// **先问「在跑哪一台」,再决定峰值挂给谁。** 反过来(挂给配置里选的那台)
	// 正是热切换失败时那句谎的机制。
	var running string
	var live coreLiveStatus
	answered := false
	if coreStatus != nil {
		if st, ok := coreStatus(); ok {
			running = runningServerName(entries, st.ServerHost)
			live = st
			answered = true
		}
	}
	attachThroughput(entries, running, live, past.Servers, time.Now())
	single := singleServerEntry(list, configPath)
	var singleRunning *bool
	if single != nil && answered {
		on := len(hostMatches([]ServerEntry{*single}, live.ServerHost)) == 1
		singleRunning = &on
	}
	return ServerListResponse{
		Servers:              entries,
		CurrentServer:        single,
		CurrentServerRunning: singleRunning,
		Current:              current,
		Running:              running,
		// nil ⇒ 配置里根本没有 `servers:` 这个键;长度为 0 的非 nil 切片 ⇒
		// 有这个键、里面确实是空的。这个区分只有这里做得到(见 SingleServer)。
		SingleServer: list == nil,
		ConfigPath:   configPath,
	}, nil
}

// respondWithServerList 是「改完之后回一份改动后的完整清单」那一步。
//
// 回完整清单而不是一个 ok:界面据此重画,不必自己推演改动后的状态 ——
// 推演出来的状态与盘上真实的状态漂开,正是这个仓库反复栽的形状。
//
// 回的 current 是**发出去的那一份里的那个值**,给改动类动作留痕用:三个动作都
// 承诺「不动 current」,而一条只记了自己做过什么、没记出口在不在原处的日志,
// 事后答不出这个承诺有没有被守住。
func respondWithServerList(w http.ResponseWriter, configPath string, coreStatus coreStatusReader, added string) (current string, ok bool) {
	resp, err := serversSnapshot(configPath, coreStatus)
	if err != nil {
		// 完整原因只进 Guardian 日志:配置里有服务器链接,而链接就是凭据。
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return "", false
	}
	resp.Added = added
	writeGuardianJSON(w, http.StatusOK, resp)
	return resp.Current, true
}

// logServerChange 把一次改动记成一行。**current 一起记**:三个动作都承诺不动
// 出口,而只有把它记下来,事后才答得出那个承诺有没有被守住。
func logServerChange(event, name, current string, ok bool) {
	if !ok {
		// 改动做成了,但改完那份清单没读回来 —— 别编一个 current 出来。
		log.Printf("%s name=%q current=unknown", event, name)
		return
	}
	log.Printf("%s name=%q current=%q", event, name, current)
}

// findServerNamed 从清单里挑出叫这个名字的那一台,没有就返回 nil。
//
// **比对判据只有一份**(`setup.SameServerName`),不是「与 setup 那一侧同款」——
// 同款是要靠人记住的,而这三处此前真的各写了一份:这里、replaceServerLink 里
// 一份内联、setup.RemoveServer 里一份。任一处漂了,「查过了、没有这台」与
// 「写得进去」就分家:菜单说已经删掉了,而配置里那一行还在。
func findServerNamed(list []config.Server, name string) *config.Server {
	for i := range list {
		if setup.SameServerName(list[i].Name, name) {
			return &list[i]
		}
	}
	return nil
}

// serverNamed 是 findServerNamed 的薄壳:只问「在不在」。
func serverNamed(list []config.Server, name string) bool {
	return findServerNamed(list, name) != nil
}

// singleServerEntry 在单服务器配置下把「当前那台」补成一条条目。
//
// **判据与 CLI 那一半同源**:走 config.Parse 取 cfg.Server —— 它已经把
// servers/current、server、transports 三种写法归一了,自己再推一遍 current
// 该指谁就是第二份判据,而它会与真正在用的那条漂开。
//
// 只在 `list == nil`(键整个缺席)时出现:有 `servers:` 清单时那台已经在
// Servers 里,再发一遍就是同一件事的两份拷贝。读不出来 ⇒ nil ⇒ 消费方照旧
// 说「没问出来」,绝不编一个占位地址。
func singleServerEntry(list []config.Server, configPath string) *ServerEntry {
	if list != nil {
		return nil
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return nil
	}
	host, ok := setup.LinkHost(cfg.Server)
	if !ok || host == "" {
		return nil
	}
	return &ServerEntry{Host: host, Port: setup.LinkPort(cfg.Server), Current: true}
}

// serverEntries 只发出**主机名**,绝不发链接本身。
//
// 链接是凭据(里面有 uuid / 密码)。菜单要显示的是「流量从哪出去」,而那是主机;
// 把整条链接送到一个 uid 501 的进程里,只是为了渲染一行字,不值得。
func serverEntries(list []config.Server, current string) []ServerEntry {
	entries := make([]ServerEntry, 0, len(list))
	for _, s := range list {
		// 必须用会解 bx:// 壳的那份判据:配置里存的是换壳链接,
		// 直接问 tunnel.ServerHost 会得到整串 base64(真机实测)。
		host, ok := setup.LinkHost(s.Link)
		if !ok {
			host = ""
		}
		udp := ""
		if strings.TrimSpace(s.UDP) != "" {
			if uh, ok := setup.LinkHost(s.UDP); ok {
				udp = uh
			}
		}
		entries = append(entries, ServerEntry{
			Name: s.Name, Host: host, Port: setup.LinkPort(s.Link), UDPHost: udp,
			Current: setup.SameServerName(s.Name, current),
		})
	}
	return entries
}

// switchInFlight 串行化换服务器。
//
// **一次切换不是原子的**:它是「写配置 → 武装 → 等隧道健康 → 确认」四步、
// 最长二十几秒。两次交错的后果不只是乱序,而是**验证验错了对象**:
// A 武装 B、B 武装 C 之后,A 那句「隧道健康吗」问到的是 C 的隧道,于是 A 确认了
// C,再对用户说「已切到 B」—— 正是这一整轮在消灭的那种谎。
//
// **用独立的锁,不用 up/down 那个 mutation channel**:后者会让一次切换把
// `bx down` 挡在门外二十几秒,而「停止路径不许依赖/等待别的事」是这个项目用
// 一次 71 分钟的事故换来的规矩。
//
// **抢不到就立刻拒绝,不排队**:排队会让用户连点几下之后,出口在接下来一分钟里
// 自己跳好几次。
var switchInFlight sync.Mutex

func applyServerSwitch(w http.ResponseWriter, r *http.Request, configPath string, switchTo serverSwitcher, probe serverProber, coreStatus coreStatusReader) {
	if !switchInFlight.TryLock() {
		log.Printf("guardian_server_switch_rejected reason=busy")
		writeGuardianJSON(w, http.StatusConflict, map[string]string{"code": "servers_switch_busy"})
		return
	}
	defer switchInFlight.Unlock()

	uid, _ := peerUIDFrom(r.Context())
	// **拼错的键与拼错的值走的是同一扇门,而这扇门通向换出口。**
	//
	// 不开 DisallowUnknownFields 的话,`{"actoin":"remove","name":"osaka"}` 里
	// `Action` 解出空串,落进下面那条「空 action = 换过去」的兼容分支 —— 于是
	// 一个键名敲错的删除请求会**真的把出口切到 osaka**、改配置、回 200,与
	// 下面 default 分支刚拦掉的那条伤害逐字相同(只是从隔壁那扇门进来)。
	// 菜单那边把 "action" 写成 "actoin" 是一次改不出编译错误的手滑。
	//
	// **兼容契约不动**:空 action 仍然是「换到 Name 那一台」,老客户端照旧。
	// 收紧的只是「我不认识这个键」——那从来不是任何客户端有意发出的东西。
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	var req serversRequest
	if err := decoder.Decode(&req); err != nil {
		// 留痕:这条路上的失手会以「出口国变了」的形式被用户看见,而那时
		// 唯一能指认是哪个键写错了的地方就是这一行。
		log.Printf("guardian_server_request_rejected reason=bad_json uid=%d err=%v", uid, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_bad_request"})
		return
	}
	switch action := strings.ToLower(strings.TrimSpace(req.Action)); action {
	case "add":
		addServerEntry(w, req, configPath, coreStatus, uid)
		return
	case "remove":
		removeServerEntry(w, req, configPath, coreStatus, uid)
		return
	case "replace":
		replaceServerLink(w, req, configPath, coreStatus, uid)
		return
	case "probe":
		probeServers(w, configPath, probe, coreStatus, uid)
		return
	case "":
		// 空 Action = 换到 Name 那一台。这是这个端点最初唯一的动作,保持兼容。
	default:
		// **认不出的动作一律拒绝,绝不落进上面那条兼容分支。**
		//
		// 兼容分支切的是 `req.Name`,**不是** Action —— 而 remove / replace 的
		// 请求按构造带着一个**合法**的名字:正是用户想删掉或想换链接的那一台。
		// 于是一个拼错的 `{"action":"delete","name":"osaka"}` 不会「找不到名为
		// delete 的服务器」,它会**真的把出口切到 osaka**、改配置、回 200。
		// 菜单那边只要把 "remove" 写成 "delete",点一下 Delete 就换了出口 IP
		// 与国家 —— multi-server 设计里明写「只有用户可以切」。
		//
		// 这条分支在有兄弟动词之前不可达(没有别的词可拼错),是它们让它变得
		// 可达的,所以门也由它们来补。
		log.Printf("guardian_server_action_rejected action=%q uid=%d", action, uid)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_unknown_action"})
		return
	}
	// 谁把出口换到了哪台,留痕。这是必须可审计的一类改动。
	log.Printf("guardian_server_switch_requested name=%q uid=%d", req.Name, uid)

	// **先写配置,再热切。** 反过来的话,热切成功而写盘失败会留下
	// 「现在在 B、下次启动回 A」—— 一个没人看得出来的不一致。
	if err := setup.SetCurrentServer(configPath, strings.TrimSpace(req.Name)); err != nil {
		log.Printf("guardian_server_switch_write_failed name=%q err=%v", req.Name, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_unknown_name"})
		return
	}
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	var target config.Server
	for _, s := range list {
		if setup.SameServerName(s.Name, current) {
			target = s
		}
	}
	host, _ := setup.LinkHost(target.Link)

	// **解壳再交给 Core**:它只认内层链接,喂换壳的进去它解析不出主机、
	// 装不了 bypass,于是(正确地)拒绝切换,而用户看到的是一句关于 base64 的错误。
	if err := switchTo(target.Name, setup.DecodeLink(target.Link), setup.DecodeLink(target.UDP)); err != nil {
		log.Printf("guardian_server_switch_hot_failed name=%q err=%v", target.Name, err)
		// **200 而不是 500**:配置确实写成功了,这次请求不是白做的。
		// 但 applied=false 让菜单说得出「要重启才能用上」那半句。
		writeGuardianJSON(w, http.StatusOK, switchResponse{
			Name: target.Name, Host: host, Applied: false,
			Outcome: switchOutcomeCode(err),
		})
		return
	}
	log.Printf("guardian_server_switch_applied name=%q host=%s uid=%d", target.Name, host, uid)
	writeGuardianJSON(w, http.StatusOK, switchResponse{Name: target.Name, Host: host, Applied: true})
}

// addServerEntry 把一台加进清单。
//
// **它不改变现在在用的是哪一台,也不热切任何东西。** 刚部署好一台新 VPS 不构成
// 「把我的出口换过去」的请求;换出口要用户在清单里显式点一下(见 applyServerSwitch)。
//
// 「不改变在用哪一台」不等于「一个字节都不碰 current:」:清单本来没有 current 时
// (`bx setup` 写的 legacy `server:` 迁成清单之后正是这样,而 CLAUDE.md 称它
// 「最常见那种配置」),setup.settleCurrent 会把**此刻实际在用的那一台**
// (config.resolveServers 的回落 = servers[0])写明白。**2026-09-13 之前它写的是
// 刚加进来的那一台** —— 没有热切,所以当场看不出来,要到下一次 `bx up` 才发作。
//
// 链接不写进日志 —— 它就是凭据。
func addServerEntry(w http.ResponseWriter, req serversRequest, configPath string, coreStatus coreStatusReader, uid uint32) {
	name := strings.TrimSpace(req.Name)
	link := strings.TrimSpace(req.Link)
	log.Printf("guardian_server_add_requested name=%q uid=%d has_udp=%t", name, uid, strings.TrimSpace(req.UDP) != "")
	if name == "" {
		// 名字可省略:按链接推导。**用 setup.LinkHost,不用 config.DeriveServerName**——
		// 两条理由都要:① 用户手里的几乎全是 bx:// 换壳,DeriveServerName→
		// tunnel.ServerHost 不解壳,会把整串 base64 当成"名字";LinkHost 是
		// serverEntries 用的同一份判据,认得壳。② LinkHost 只回 (host, ok),
		// 没有错误文本——DeriveServerName 那条链路(url.Parse 失败时)会把**原始
		// 链接逐字**塞进 error string,而链接就是凭据,决不能进日志
		// (本函数上面那句"链接不写进日志"的注释说的就是这个)。
		host, ok := setup.LinkHost(link)
		if !ok {
			// 不带 err、不带 link:解不出主机本身已经是完整的诊断信息。
			log.Printf("guardian_server_add_failed reason=derive_name")
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
			return
		}
		if err := config.ValidateServerName(host); err != nil {
			// **同样不带 %v**:校验错误会把 host 本身回显进去,而 host 来自
			// 链接、可能仍带着凭据的碎片(比如把 user:pass@ 一起解出来的情形)。
			log.Printf("guardian_server_add_failed reason=derive_name_invalid")
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
			return
		}
		name = host
	} else if _, ok := setup.LinkHost(link); !ok {
		// **给了名字也照样校验。** 校验此前只发生在「名字省略、要按链接推导」
		// 那一支上,而那是个副作用:名字一给,LinkHost 就不再被调,任何字符串
		// 都写得进配置 —— 种下的是一台永远切不过去的服务器,而清单里它长得
		// 和别的一样(只有 host 那一格空着)。
		//
		// 这道门守的是「主机解不出来」,**不是**「这条链接一定能连上」——
		// 后者只有真拨一次才知道,而这里一个包都不许发。
		// 不带 err、不带 link:链接就是凭据。
		log.Printf("guardian_server_add_failed reason=bad_link")
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
		return
	}
	// **UDP 那条链接过同一道门。** 上面那段论证(同一个 helper、一行代码、
	// 一严一松是自相矛盾的)对同一个请求里的第二条链接逐字成立。
	//
	// **空的 UDP 是「这台不单独走 UDP」,不是畸形输入**,所以只在非空时校验。
	// 后果比主链接轻(专用 UDP 传输起不来会回落主传输,`bx status` 的
	// UDPNotice 会说出来),但两种形式都收下一条解不出主机的链接、都静默写盘,
	// 而用户以为自己配好了 QUIC 加速。码沿用 servers_add_failed:菜单对它那句
	// 话正是「Check the link」,另立一个码只会退回通用漏斗那句更差的废话。
	if udp := strings.TrimSpace(req.UDP); udp != "" {
		if _, ok := setup.LinkHost(udp); !ok {
			// 不带 err、不带 link:UDP 链接同样是凭据。
			log.Printf("guardian_server_add_failed reason=bad_udp_link")
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
			return
		}
	}
	// **先查重,再写。** setup.AddServer 对已存在的名字是「改写那一台的链接」——
	// 对用户那是「我加了一台,结果把原来那台换掉了」,而界面上看不出任何异常。
	existing, _, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	for _, s := range existing {
		if setup.SameServerName(s.Name, name) {
			log.Printf("guardian_server_add_rejected reason=name_exists name=%q", name)
			writeGuardianJSON(w, http.StatusConflict, map[string]string{"code": "servers_name_exists"})
			return
		}
	}
	if _, err := setup.AddServer(configPath, name, link, strings.TrimSpace(req.UDP)); err != nil {
		log.Printf("guardian_server_add_failed name=%q err=%v", name, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
		return
	}
	nowCurrent, ok := respondWithServerList(w, configPath, coreStatus, name)
	logServerChange("guardian_server_added", name, nowCurrent, ok)
}

// removeServerEntry 从清单里删掉一台。
//
// **删掉当前正在用的那台一律拒绝。** 那会让 current 指向一个不存在的名字,
// 下一次启动直接起不来 —— 而用户只是想清理一条不用的记录。判据取配置里的
// current(**删除是配置层的操作,与「实际在跑哪一台」无关**:热切换失败时
// 两者不同,而那时该拦的仍然是配置里那台 —— 删掉它配置就坏了)。
//
// 拒绝这条路上**一个字节都不许写盘**:用户看到一句拒绝就以为什么都没发生,
// 而一次「被拒绝」却仍然改了配置的删除,比拒绝失败更糟。
func removeServerEntry(w http.ResponseWriter, req serversRequest, configPath string, coreStatus coreStatusReader, uid uint32) {
	name := strings.TrimSpace(req.Name)
	log.Printf("guardian_server_remove_requested name=%q uid=%d", name, uid)
	if name == "" {
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_bad_request"})
		return
	}
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	if setup.SameServerName(current, name) {
		// **自己的码,不与「删失败了」共用一个。** 底下的 setup.RemoveServer
		// 也拦这一条(纵深防御),但它只给得出一句中文错误,而错误串不出门 ——
		// 菜单要说得出「先换到别的那台再删」就得有这个码。
		log.Printf("guardian_server_remove_rejected reason=current name=%q", name)
		writeGuardianJSON(w, http.StatusConflict, map[string]string{"code": "servers_remove_current"})
		return
	}
	// **「这台已经没了」与「盘没写成」必须分得开。** 两个菜单窗口开着、同一台
	// 删两次是真会发生的,而前者其实什么都不用做;折成一个码之后菜单只能对两者
	// 说同一句话(replace 那半早有 servers_unknown_name,这半此前没有)。
	if !serverNamed(list, name) {
		log.Printf("guardian_server_remove_rejected reason=unknown_name name=%q", name)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_unknown_name"})
		return
	}
	if err := setup.RemoveServer(configPath, name); err != nil {
		log.Printf("guardian_server_remove_failed name=%q err=%v", name, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_remove_failed"})
		return
	}
	nowCurrent, ok := respondWithServerList(w, configPath, coreStatus, "")
	logServerChange("guardian_server_removed", name, nowCurrent, ok)
}

// replaceServerLink 就地换掉同名那台的链接:凭据轮换,或者 VPS 重建换了地址。
//
// **它不动 current,也不热切任何东西。** 换链接是修一条记录,不是「把我的流量
// 换到那里去」—— 与 addServerEntry 同一条判断。
//
// **刻意不走 setup.UpsertServer,尽管那个函数当初就是为这件事写的、至今零生产
// 调用方**(spec §7.2 原本就是这么写的,这里是有意偏离):它会把 current 设成
// 被改的那一台(TestUpsertStillSwitchesBecauseThatIsItsJob 钉着这个行为:它
// 服务的是 `bx setup`「用这一台」),于是换一条**没在用**那台的链接会顺手把
// 出口换过去,而界面上只说了「已替换」。
//
// **也不走 setup.AddServer** —— 它只差半步:current 空着时它会填上(填的是
// 清单里第一台,所以自 2026-09-13 起**不再挪动出口**,见 setup.settleCurrent)。
// 但一份没有 current: 的清单**照样在跑**(config.resolveServers 回落 servers[0]),
// 手改出来的配置正是这个样子 —— 恰好就是这个功能的受众;替他把 current 钉死之后,
// RemoveServer 从此会拒绝删掉那一台,而他只是换了一条链接。
// 走的是 setup.ReplaceServerLink:它任何情况下都不动 current。
func replaceServerLink(w http.ResponseWriter, req serversRequest, configPath string, coreStatus coreStatusReader, uid uint32) {
	name := strings.TrimSpace(req.Name)
	link := strings.TrimSpace(req.Link)
	udp := strings.TrimSpace(req.UDP)
	// 链接不写进日志 —— 它就是凭据。
	log.Printf("guardian_server_replace_requested name=%q uid=%d has_udp=%t", name, uid, udp != "")
	if name == "" || link == "" {
		log.Printf("guardian_server_replace_failed reason=bad_request name=%q", name)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_bad_request"})
		return
	}
	existing, _, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	// **先确认它真在清单里,再写。** 底层原语自己也拦这一条(纵深防御),但
	// 错误串按门规不出门 —— 菜单要说得出「这台已经没了」就得有这个码。
	// 这里同时要拿到它**原来那条 UDP**,见下。
	target := findServerNamed(existing, name)
	if target == nil {
		log.Printf("guardian_server_replace_rejected reason=unknown_name name=%q", name)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_unknown_name"})
		return
	}
	if _, ok := setup.LinkHost(link); !ok {
		// **换链接同样要校验,而这一条的代价落在当前那台上。** 底下的
		// setup.ReplaceServerLink 什么都不校验:任何字符串都写得进去。换的是
		// 当前那台时,配置就此指着一条下一次重连解析不出来的链接,而界面刚刚
		// 承诺「bx picks up the new one when it reconnects」—— 一句当场就被
		// 证伪的话,还把机器留在起不来的状态里。
		//
		// **码沿用 servers_replace_failed**:菜单对它那句话正是「Check that you
		// pasted a complete bx link」—— 另立一个码只会得到同一句话。原始链接
		// 一个字都不出门、也不进日志。
		log.Printf("guardian_server_replace_rejected reason=bad_link name=%q", target.Name)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_replace_failed"})
		return
	}
	if udp != "" {
		if _, ok := setup.LinkHost(udp); !ok {
			// **同一道门,同一条理由**(见 addServerEntry 里那段)。这里只校验
			// **请求带来的**那条:下面那个回落取的是盘上已有的值,而盘上可能
			// 正躺着一条这次修复之前写进去的畸形链接 —— 拿它当拒绝理由会让
			// 用户连主链接都换不了,而换主链接恰恰是他此刻要做的事。
			log.Printf("guardian_server_replace_rejected reason=bad_udp_link name=%q", target.Name)
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_replace_failed"})
			return
		}
	}
	if udp == "" {
		// **没让它改的东西不许被顺手抹掉。** 空 UDP 在底下那个原语里是「删掉
		// udp: 这一行」,而 UDP 传输一旦消失就**静默**回落到主传输 —— 没有任何
		// 一处会报错,而用户以为自己只换了一条链接。
		udp = strings.TrimSpace(target.UDP)
	}
	if err := setup.ReplaceServerLink(configPath, target.Name, link, udp); err != nil {
		// %v 里可能带着 name(校验错误会回显它),但绝不会带 link —— 那是凭据。
		log.Printf("guardian_server_replace_failed name=%q err=%v", target.Name, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_replace_failed"})
		return
	}
	nowCurrent, ok := respondWithServerList(w, configPath, coreStatus, "")
	logServerChange("guardian_server_replaced", target.Name, nowCurrent, ok)
}

// probeServers 逐台量一次「从这台机器直连过去多远」。
//
// **不并发。** 同时向几台服务器发握手会在网络上留下一个很整齐的模式,而这几台
// 恰好是同一个人的资产 —— 一次点击暴露的关联比逐台发更强。清单通常只有两三台,
// 串行的代价是几百毫秒。
//
// **一台失败不影响其余**:每台各自带自己的结论,与 internal/observe 那条
// 「任一项观测失败即记为 Unknown 并附原因,绝不中断其余项」同源。
func probeServers(w http.ResponseWriter, configPath string, probe serverProber, coreStatus coreStatusReader, uid uint32) {
	if probe == nil {
		writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "probe unavailable"})
		return
	}
	// **与 GET 那条路同一份快照。** 探测应答此前自己拼一份 ServerListResponse,
	// 于是 Running 只加在了 GET 上,而 `bx server list --test` 走的正是这一条 ——
	// 一台完全健康的机器每次都被告知「实际在跑的是哪一台这次没问到」。
	resp, err := serversSnapshot(configPath, coreStatus)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	// 探测走在隧道**外面**,是一次真实的出站 —— 留痕,和别的改动类动作一样。
	log.Printf("guardian_server_probe_requested count=%d uid=%d", len(resp.Servers), uid)

	entries := resp.Servers
	for i := range entries {
		host, port := entries[i].Host, entries[i].Port
		if host == "" {
			entries[i].Probe = &ProbeReport{
				Measured: false,
				// **人话经 supervisor.ProbeErrorText,不在这儿手写一句。**
				// 它此前是英文的,理由是「那句话会出现在全英文菜单里」——
				// 而菜单如今根本不读这个字段(它读码、自己出英文)。于是这句话
				// 只剩一个消费方:中文的 `bx server list`。手写还有第二个代价:
				// 「码 ↔ 中文」那张表里的这一条在生产里**永远不可达**,而守卫
				// 声称覆盖了它。
				Error:     supervisor.ProbeErrorText(supervisor.ProbeErrLinkUnparsed),
				ErrorCode: supervisor.ProbeErrLinkUnparsed,
			}
			continue
		}
		result, err := probe(host, port)
		if err != nil {
			// Core 不可达 / 这一版不支持 —— 那是「没问出来」,**不是「不可达」**。
			// 判成不可达会把一台好服务器标成红的。
			log.Printf("guardian_server_probe_failed host=%s err=%v", host, err)
			entries[i].Probe = &ProbeReport{
				Measured:  false,
				Error:     supervisor.ProbeErrorText(supervisor.ProbeErrCoreUnreachable),
				ErrorCode: supervisor.ProbeErrCoreUnreachable,
			}
			continue
		}
		entries[i].Probe = &ProbeReport{
			Measured: true, Reachable: result.Reachable, RTTMS: result.RTTMS,
			// **码与文字一起转发。** 只转文字就是把一句中文送进英文菜单;
			// 只转码就让 `bx server list` 没话可说 —— 两个消费方要的东西不同。
			Error: result.Error, ErrorCode: result.ErrorCode,
		}
	}
	writeGuardianJSON(w, http.StatusOK, resp)
}

// attachThroughput 给每台挂上吞吐:**实际在跑的**那台用实时观测,其余用历史。
//
// **running 是 Core 报的那一台,不是配置里选的那一台。** Core 的峰值来自一块
// 进程级速率表,热切换**不会**把它清零;挂到配置里那台头上、年龄再强行归零,
// 读起来正是「刚刚在这台上量到的」—— 而那个数是另一台的。切换失败时这两者
// 恰好不同,而那正是用户最需要一个准确数字的时刻。
//
// **历史必须带年龄。** 所有者同意存历史(「以前的值没事」)的前提正是界面上
// 要标出来这是以前的 —— 一个不带年龄的历史数字读起来像现状。
//
// **「实时」那一份也要报它真实的年龄,不许写死 0。** Core 那块速率表是进程级的,
// 热切换**不会**把它的峰值清零 —— A→B 切**成功**之后它报的仍是 A 那个数;年龄
// 写死成 0 之后(PeakAgeSeconds 带 omitempty,0 连键都不上线)界面读到的就是
// 「刚刚在 B 上量到的」。带上真实年龄之后,一个不带年龄的数字只可能真的是刚量到的。
//
// **年龄修好了,张冠李戴那一半还在,别以为这里已经修干净了**:切换**成功**
// 之后,B 那一行显示的仍然是 A 的那个数,只是如今如实标着它真实的年龄(界面
// 因此至少不会把它读成现状)。根治要在 Core 那边热切时把速率表清零,那是另一层
// 的改动,**刻意搁置**。
//
// **实时的那份压过历史**:两者都在时,在跑的那台显示的是速率表此刻报的值。
// running 为空(问不出来 / 认不出那台主机)时**谁都不挂实时** —— 那个数确实
// 存在,只是不知道该记给谁,而记错比不记糟得多;`live.PeakAt` 缺席时同样不挂 ——
// 说不出年龄的数字不许上线。
func attachThroughput(entries []ServerEntry, running string, live coreLiveStatus, history map[string]throughputEntry, now time.Time) {
	for i := range entries {
		past, ok := history[entries[i].Name]
		if !ok || past.PeakBPS <= 0 || past.ObservedAt.IsZero() {
			continue
		}
		age := now.Sub(past.ObservedAt)
		if age < 0 {
			// 时钟被改过。**宁可不报年龄也不报一个负的** —— 负的年龄会让
			// 界面说出「-3 小时前」,而用户从此不信这一栏里的任何数字。
			continue
		}
		entries[i].PeakBPS = past.PeakBPS
		entries[i].PeakAgeSeconds = int64(age / time.Second)
	}
	running = strings.TrimSpace(running)
	if running == "" || live.PeakBPS <= 0 || live.PeakAt.IsZero() {
		return
	}
	liveAge := now.Sub(live.PeakAt)
	if liveAge < 0 {
		// 与历史那一半同一条:时钟被改过时宁可不报,也不报一个负的年龄。
		return
	}
	for i := range entries {
		if setup.SameServerName(entries[i].Name, running) {
			entries[i].PeakBPS = live.PeakBPS
			entries[i].PeakAgeSeconds = int64(liveAge / time.Second)
			return
		}
	}
}
