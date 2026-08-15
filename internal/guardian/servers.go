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
	UDPHost string `json:"udp_host,omitempty"`
	Current bool   `json:"current"`
}

type serversResponse struct {
	Servers    []serverEntry `json:"servers"`
	Current    string        `json:"current"`
	ConfigPath string        `json:"config_path"`
}

type serversRequest struct {
	Name string `json:"name"`
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

// liveServerSwitch 接到真 Core 上。
func liveServerSwitch(name, link, udp string) error {
	return supervisor.SwitchServer(supervisor.LiveSwitchDeps(), name, link, udp)
}

// serversHandler 服务 /v1/servers。
//
// **授权与 /v1/up、/v1/rules 同一道门。** 换服务器会改变出口 IP,是与开关保护
// 同一量级的动作;而菜单以 owner 身份跑,取一致才用得上。
func serversHandler(configPath string, ownerUID uint32, switchTo serverSwitcher) http.HandlerFunc {
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
			serveServerList(w, configPath)
		case http.MethodPost:
			applyServerSwitch(w, r, configPath, switchTo)
		default:
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func serveServerList(w http.ResponseWriter, configPath string) {
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		// 完整原因只进 Guardian 日志:配置里有服务器链接,而链接就是凭据。
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	writeGuardianJSON(w, http.StatusOK, serversResponse{
		Servers:    serverEntries(list, current),
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
			Name: s.Name, Host: host, UDPHost: udp,
			Current: strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(current)),
		})
	}
	return entries
}

func applyServerSwitch(w http.ResponseWriter, r *http.Request, configPath string, switchTo serverSwitcher) {
	var req serversRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_bad_request"})
		return
	}
	uid, _ := peerUIDFrom(r.Context())
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
