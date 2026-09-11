package guardian

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/getbx/bx/internal/policy"
	"github.com/getbx/bx/internal/preset"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/supervisor"
)

// rulesResponse 是 GET /v1/rules 的应答。
type rulesResponse struct {
	Direct []string `json:"direct,omitempty"`
	Proxy  []string `json:"proxy,omitempty"`
	// Groups 是按 internal/preset 那份**唯一清单**分好的组。界面显示组而不是
	// 一条条域名:十行域名对普通用户没有意义,而「Steam 相关的走不通」有。
	Groups []ruleGroup `json:"groups,omitempty"`
	// Custom 是**不属于任何组**的规则(用户手写的)。单独列出来是因为组开关
	// 绝不能碰它们 —— 菜单没有资格替用户删掉他自己写的东西。
	Custom []string `json:"custom,omitempty"`
	// ConfigPath 供界面提供「在 Finder 中显示」。发布出去而不是让菜单自己猜,
	// 与 stats.Report.ConfigPath 同一条纪律。
	ConfigPath string `json:"config_path,omitempty"`
	// Review 是 Guardian 自己做的规则体检(rulereview.go)。**刻意不是
	// omitempty 之外的形状**:nil = 「这一版没做/读不到配置」,而一份**空**
	// 报告是「查过了、没有问题」—— 两者压成同一个东西正是这个功能最贵的教训。
	Review *rulereview.Report `json:"review,omitempty"`
	// RequiresRestart 说的是「这次改动(POST)/ 经这一版改的规则(GET)要不要重连
	// 才生效」,而且**刻意不是 omitempty**。
	//
	// 2026-09-08 之前它恒为 true —— 那是常量,不是事实:`bx direct add` 早就经
	// Core 的 /v0/reload 热重载,只是 Guardian 这条路从没调过它。现在 POST 成功
	// 写盘后也走同一条重载路,重载成功 ⇒ false;Core 没应答 / 没接重载 ⇒ true
	// (规则已落盘、下次重连生效,如实说要重连)。菜单不说这句话,用户会以为
	// 已经生效,然后在问题依旧时把这一步排除掉 —— 而那正是真正的原因。
	// 键缺席会被读成「这版 Guardian 不知道要不要重启」,与「不需要重启」是两回事。
	RequiresRestart bool `json:"requires_restart"`
}

// rulesRequest 是 POST /v1/rules 的请求。
type rulesRequest struct {
	Action  string `json:"action"` // add | remove | enable_group | disable_group
	Kind    string `json:"kind"`   // direct | proxy
	Pattern string `json:"pattern"`
	Group   string `json:"group"`
	// Force 放行 direct 规则的风险门(见 policy.DirectRuleHazard)。**逃生口,
	// 不是主路**:菜单把它放在次要动作上,右键那条一键路径压根不提供危险候选。
	Force bool `json:"force,omitempty"`
}

// ruleGroup 是界面上的一行。
type ruleGroup struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
	// State 是 on / off / partial。**三态,不是布尔。** 真实配置里一组常常只装了
	// 一半(用户手工删过几条,或者 preset 后来加了新域名);把半装的组画成「开」,
	// 用户会以为那几条在生效,画成「关」更糟。
	State     string `json:"state"`
	Installed int    `json:"installed"`
	Total     int    `json:"total"`
	// Domains 是这一组的完整域名清单。**发下来而不是让客户端自己存一份** ——
	// 界面要把 /v1/status 报的失败归因对到组上,而两份清单一旦漂开,
	// 界面就会把失败标到错误的组上,或者干脆标不上而看起来一切正常。
	Domains []string `json:"domains,omitempty"`
}

// rulesHandler 服务 /v1/rules。
//
// **授权与 /v1/up、/v1/down 同一道门(authorizeOwnerPeer)。** 判据不是「改规则
// 比开关保护更敏感」—— 恰恰相反,能关掉保护的人已经能做更坏的事。取一致是要点:
// 菜单要能改规则,而菜单以 owner 身份跑。
//
// **它不重启任何东西。** 重启是一次断网,必须是用户单独的、显式的一下。它做的
// 是**热重载**:写盘成功后经 reload 叫 Core 重读配置、原子换入新 router(与
// `bx direct add` 同一条 /v0/reload 路,不断隧道、不碰 TUN/路由)。reload 为 nil
// 表示没接线,应答退回「要重连」。
func rulesHandler(configPath string, ownerUID uint32, reload func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "rules require owner or root peer"})
			return
		}
		if strings.TrimSpace(configPath) == "" {
			// 「没接线」不是「没有规则」—— 后者会让菜单显示一个空列表,
			// 用户据此以为自己没配过任何规则。
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "rules unavailable: no config path"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			serveRuleList(w, configPath, reload == nil)
		case http.MethodPost:
			applyRuleChange(w, r, configPath, reload)
		default:
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func serveRuleList(w http.ResponseWriter, configPath string, requiresRestart bool) {
	rules, err := setup.ListRules(configPath)
	if err != nil {
		// 完整原因只进 Guardian 日志;响应体只带失败类别 —— 原始错误串里可能
		// 有路径、链接或凭据(与本仓库其它端点同一条纪律)。
		log.Printf("guardian_rules_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "rules_read_failed"})
		return
	}
	grouped, custom := preset.Classify(rules.Direct)
	groups := make([]ruleGroup, 0, len(preset.All()))
	for _, p := range preset.All() {
		// **分开数「在用的」与「退役的」。** 一组的 Direct 全装了、却还留着退役
		// 域名时,显示成 on 等于告诉用户「这一组就是现在这样」,而实际上里面还有
		// 我们主动废弃的规则在把流量送错方向。
		live, stale := 0, 0
		liveSet := map[string]bool{}
		for _, d := range p.Direct {
			liveSet[strings.ToLower(strings.TrimSpace(d))] = true
		}
		for _, rule := range grouped[p.Name] {
			if liveSet[strings.ToLower(strings.TrimSpace(rule))] {
				live++
				continue
			}
			stale++
		}
		groups = append(groups, ruleGroup{
			Name: p.Name, Title: p.Title, Summary: p.Summary,
			State:     groupState(live, len(p.Direct), stale),
			Installed: live, Total: len(p.Direct),
			// 把**全部**认领的域名发下来(含退役的):界面要把失败归因对到组上,
			// 而一条退役规则正在成片失败恰恰是最该被看见的。
			Domains: append([]string(nil), p.AllDomains()...),
		})
	}
	writeGuardianJSON(w, http.StatusOK, rulesResponse{
		Direct:          rules.Direct,
		Proxy:           rules.Proxy,
		Groups:          groups,
		Custom:          custom,
		ConfigPath:      configPath,
		Review:          reviewRulesAt(configPath, nil),
		RequiresRestart: requiresRestart,
	})
}

func applyRuleChange(w http.ResponseWriter, r *http.Request, configPath string, reload func() error) {
	var req rulesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "rules_bad_request"})
		return
	}
	uid, _ := peerUIDFrom(r.Context())
	// 谁改了什么,留痕。规则决定流量走哪条路,这是必须可审计的一类改动。
	log.Printf("guardian_rules_change_requested action=%s kind=%s pattern=%q uid=%d",
		req.Action, req.Kind, req.Pattern, uid)

	var err error
	switch req.Action {
	case "add":
		if req.Kind == "direct" && !req.Force {
			if hazard, reason, _ := policy.DirectRuleHazard(req.Pattern); hazard {
				// pattern 不进响应体(与 servers_name_exists 同一条);进日志是
				// 可以的 —— 它是用户自己刚输入的域名,不是凭据。
				log.Printf("guardian_rule_add_refused reason=risky_direct pattern=%q why=%q", req.Pattern, reason)
				writeGuardianJSON(w, http.StatusConflict, map[string]string{"code": "rules_risky_direct"})
				return
			}
		}
		err = setup.AddRule(configPath, req.Kind, req.Pattern)
	case "remove":
		err = setup.RemoveRule(configPath, req.Kind, req.Pattern)
	case "enable_group", "disable_group":
		p, ok := preset.Lookup(req.Group)
		if !ok {
			// 静默什么都不做会让界面显示成功而配置没变 —— 用户据此重连一次网络,
			// 然后发现毫无变化。
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "rules_unknown_group"})
			return
		}
		if req.Action == "enable_group" {
			// **只装在用的。** 装回退役域名等于撤销掉那次演进。
			err = setup.ApplyGroup(configPath, setup.RuleKindDirect, p.Direct, true)
		} else {
			// 关掉时连退役的一起清 —— 否则它们永远留在配置里,而界面对
			// 「自定义」规则只显示不给开关,用户根本删不掉。
			err = setup.ApplyGroup(configPath, setup.RuleKindDirect, p.AllDomains(), false)
		}
	default:
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "rules_unknown_action"})
		return
	}
	if err != nil {
		log.Printf("guardian_rules_change_failed action=%s kind=%s err=%v", req.Action, req.Kind, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "rules_change_failed"})
		return
	}
	log.Printf("guardian_rules_change_ok action=%s kind=%s pattern=%q", req.Action, req.Kind, req.Pattern)
	// 规则已落盘。**重载失败不回滚、也不把整次改动报成失败**:盘上那条规则是真的,
	// 下次重连就生效;只是应答要如实说「要重连」,别让用户以为已经生效。
	requiresRestart := true
	if reload != nil {
		if err := reload(); err != nil {
			log.Printf("guardian_rules_reload_failed err=%v", err)
		} else {
			requiresRestart = false
			log.Printf("guardian_rules_reloaded action=%s kind=%s pattern=%q", req.Action, req.Kind, req.Pattern)
		}
	}
	serveRuleList(w, configPath, requiresRestart)
}

// reloadCoreRules 是生产那份「叫 Core 热重载规则」:与 `bx direct add` 打的是同一个
// 控制 socket、同一个端点。Guardian 以 root 跑,拨得到 core.sock。
func reloadCoreRules() error { return reloadCoreRulesAt(supervisor.SockPath) }

func reloadCoreRulesAt(sockPath string) error {
	_, err := supervisor.ReloadControl(sockPath)
	return err
}

// groupState 把「装了几条 / 一共几条」压成界面那一行的三态。
// groupState 把「在用的装了几条 / 一共几条 / 还留着几条退役的」压成界面那一行的三态。
//
// **留着退役域名时一律 partial**,哪怕在用的全装齐了:显示成 on 等于断言
// 「这一组就是现在的样子」,而事实上里面还有我们主动废弃的规则在生效。
// partial 会促使用户关一次再开,那正好清掉它们。
func groupState(live, total, stale int) string {
	switch {
	case stale > 0:
		return "partial"
	case total == 0 || live == 0:
		return "off"
	case live >= total:
		return "on"
	default:
		return "partial"
	}
}
