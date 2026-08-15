package guardian

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/supervisor"
)

// 本文件服务 /v1/servers:菜单栏那一侧的「有哪几台、现在在哪台、换一台」。
//
// **切换的编排不在这里。** 它在 supervisor.SwitchServer(武装 → 验证 → 确认),
// 与 `bx server use` 用的是同一份。这个仓库反复栽在「同一件事两份实现,一份改了
// 另一份没改,而两边测试都绿」上,所以这里只做 HTTP 与配置写盘。

type serverEntry struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`
	UDPHost string `json:"udp_host,omitempty"`
	Current bool   `json:"current"`
	// Probe 只在这次请求做过探测时出现。**键缺席 = 没测过**,不是「测了没通」。
	Probe *probeReport `json:"probe,omitempty"`
	// PeakBPS 是观测到的峰值吞吐,**只有当前那台会有**(吞吐是被动观测,
	// 没在用的服务器没有产生过流量)。0/缺席 = 没观测到,**不是「跑不动」**。
	PeakBPS int64 `json:"peak_bps,omitempty"`
}

// probeReport 是一台的探测结论。
//
// **Reachable 与 RTTMS 分开,而且 RTT 带 omitempty。** 把「没通」表达成 0 毫秒
// 会让界面显示一个漂亮的零 —— 零值读起来像一切正常,这个仓库反复禁止过。
type probeReport struct {
	Reachable bool   `json:"reachable"`
	RTTMS     int64  `json:"rtt_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type serversResponse struct {
	Servers    []serverEntry `json:"servers"`
	Current    string        `json:"current"`
	ConfigPath string        `json:"config_path"`
}

type serversRequest struct {
	// Action 空 = 换到 Name 那一台(这个端点最初唯一的动作,保持兼容)。
	// "add" = 把一台加进清单,**不动 current**。
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
	Applied bool   `json:"applied"`
	Detail  string `json:"detail,omitempty"`
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

// throughputReader 报「**当前**那台观测到的峰值吞吐」。0 = 没观测到。
type throughputReader func() (bps int64, ok bool)

// liveThroughput 从 Core 的状态里取。**只有当前那台有这个数**,因为吞吐是
// 被动观测 —— 没在用的服务器没有产生过流量,凭空给它一个数就是编。
func liveThroughput() (int64, bool) {
	rep, err := supervisor.FetchStatusReport(supervisor.SockPath)
	if err != nil || rep.PeakBPS <= 0 {
		return 0, false
	}
	return rep.PeakBPS, true
}

// liveServerSwitch 接到真 Core 上。
func liveServerSwitch(name, link, udp string) error {
	return supervisor.SwitchServer(supervisor.LiveSwitchDeps(), name, link, udp)
}

// serversHandler 服务 /v1/servers。
//
// **授权与 /v1/up、/v1/rules 同一道门。** 换服务器会改变出口 IP,是与开关保护
// 同一量级的动作;而菜单以 owner 身份跑,取一致才用得上。
func serversHandler(configPath string, ownerUID uint32, switchTo serverSwitcher, probe serverProber, throughput throughputReader) http.HandlerFunc {
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
			serveServerList(w, configPath, throughput)
		case http.MethodPost:
			applyServerSwitch(w, r, configPath, switchTo, probe)
		default:
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func serveServerList(w http.ResponseWriter, configPath string, throughput throughputReader) {
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		// 完整原因只进 Guardian 日志:配置里有服务器链接,而链接就是凭据。
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	entries := serverEntries(list, current)
	attachThroughput(entries, throughput)
	writeGuardianJSON(w, http.StatusOK, serversResponse{
		Servers:    entries,
		Current:    current,
		ConfigPath: configPath,
	})
}

// serverEntries 只发出**主机名**,绝不发链接本身。
//
// 链接是凭据(里面有 uuid / 密码)。菜单要显示的是「流量从哪出去」,而那是主机;
// 把整条链接送到一个 uid 501 的进程里,只是为了渲染一行字,不值得。
func serverEntries(list []config.Server, current string) []serverEntry {
	entries := make([]serverEntry, 0, len(list))
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
		entries = append(entries, serverEntry{
			Name: s.Name, Host: host, Port: setup.LinkPort(s.Link), UDPHost: udp,
			Current: strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(current)),
		})
	}
	return entries
}

func applyServerSwitch(w http.ResponseWriter, r *http.Request, configPath string, switchTo serverSwitcher, probe serverProber) {
	var req serversRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_bad_request"})
		return
	}
	uid, _ := peerUIDFrom(r.Context())
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "add":
		addServerEntry(w, req, configPath, uid)
		return
	case "probe":
		probeServers(w, configPath, probe, uid)
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
		if strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(current)) {
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
			Detail: "servers_hot_switch_failed",
		})
		return
	}
	log.Printf("guardian_server_switch_applied name=%q host=%s uid=%d", target.Name, host, uid)
	writeGuardianJSON(w, http.StatusOK, switchResponse{Name: target.Name, Host: host, Applied: true})
}

// addServerEntry 把一台加进清单。
//
// **它不动 current,也不热切任何东西。** 刚部署好一台新 VPS 不构成「把我的出口
// 换过去」的请求;换出口要用户在清单里显式点一下(见 applyServerSwitch)。
// 链接不写进日志 —— 它就是凭据。
func addServerEntry(w http.ResponseWriter, req serversRequest, configPath string, uid uint32) {
	name := strings.TrimSpace(req.Name)
	log.Printf("guardian_server_add_requested name=%q uid=%d has_udp=%t", name, uid, strings.TrimSpace(req.UDP) != "")
	if _, err := setup.AddServer(configPath, name, strings.TrimSpace(req.Link), strings.TrimSpace(req.UDP)); err != nil {
		log.Printf("guardian_server_add_failed name=%q err=%v", name, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
		return
	}
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	log.Printf("guardian_server_added name=%q current=%q", name, current)
	// 回完整清单而不是一个 ok:界面据此重画,不必自己推演改动后的状态 ——
	// 推演出来的状态与盘上真实的状态漂开,正是这个仓库反复栽的形状。
	writeGuardianJSON(w, http.StatusOK, serversResponse{
		Servers: serverEntries(list, current), Current: current, ConfigPath: configPath,
	})
}

// probeServers 逐台量一次「从这台机器直连过去多远」。
//
// **不并发。** 同时向几台服务器发握手会在网络上留下一个很整齐的模式,而这几台
// 恰好是同一个人的资产 —— 一次点击暴露的关联比逐台发更强。清单通常只有两三台,
// 串行的代价是几百毫秒。
//
// **一台失败不影响其余**:每台各自带自己的结论,与 internal/observe 那条
// 「任一项观测失败即记为 Unknown 并附原因,绝不中断其余项」同源。
func probeServers(w http.ResponseWriter, configPath string, probe serverProber, uid uint32) {
	if probe == nil {
		writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "probe unavailable"})
		return
	}
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	// 探测走在隧道**外面**,是一次真实的出站 —— 留痕,和别的改动类动作一样。
	log.Printf("guardian_server_probe_requested count=%d uid=%d", len(list), uid)

	entries := serverEntries(list, current)
	for i := range entries {
		host, port := entries[i].Host, entries[i].Port
		if host == "" {
			entries[i].Probe = &probeReport{Error: "链接解析不出主机"}
			continue
		}
		result, err := probe(host, port)
		if err != nil {
			// Core 不可达 / 这一版不支持 —— 那是「没问出来」,**不是「不可达」**。
			// 判成不可达会把一台好服务器标成红的。
			log.Printf("guardian_server_probe_failed host=%s err=%v", host, err)
			entries[i].Probe = &probeReport{Error: "没能测(bx 没在跑?)"}
			continue
		}
		entries[i].Probe = &probeReport{
			Reachable: result.Reachable, RTTMS: result.RTTMS, Error: result.Error,
		}
	}
	writeGuardianJSON(w, http.StatusOK, serversResponse{
		Servers: entries, Current: current, ConfigPath: configPath,
	})
}

// attachThroughput 把观测到的峰值吞吐挂到**当前**那台上。
//
// **只挂当前那台,而且只在真观测到的时候挂。** 吞吐是被动观测:没在用的服务器
// 没有产生过流量,给它一个数就是编;而 0 会被读成「跑不动」,与「这段时间没人
// 用它传东西」是两回事。
func attachThroughput(entries []serverEntry, throughput throughputReader) {
	if throughput == nil {
		return
	}
	bps, ok := throughput()
	if !ok || bps <= 0 {
		return
	}
	for i := range entries {
		if entries[i].Current {
			entries[i].PeakBPS = bps
			return
		}
	}
}
