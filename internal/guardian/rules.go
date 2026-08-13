package guardian

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/getbx/bx/internal/setup"
)

// rulesResponse 是 GET /v1/rules 的应答。
type rulesResponse struct {
	Direct []string `json:"direct,omitempty"`
	Proxy  []string `json:"proxy,omitempty"`
	// RequiresRestart 恒为 true,而且**刻意不是 omitempty**:
	// bx 不热重载配置,改完必须 `bx down && bx up`。菜单不说这句话,用户会以为
	// 已经生效,然后在问题依旧时把这一步排除掉 —— 而那正是真正的原因。
	// 键缺席会被读成「这版 Guardian 不知道要不要重启」,与「不需要重启」是两回事。
	RequiresRestart bool `json:"requires_restart"`
}

// rulesRequest 是 POST /v1/rules 的请求。
type rulesRequest struct {
	Action  string `json:"action"` // add | remove
	Kind    string `json:"kind"`   // direct | proxy
	Pattern string `json:"pattern"`
}

// rulesHandler 服务 /v1/rules。
//
// **授权与 /v1/up、/v1/down 同一道门(authorizeOwnerPeer)。** 判据不是「改规则
// 比开关保护更敏感」—— 恰恰相反,能关掉保护的人已经能做更坏的事。取一致是要点:
// 菜单要能改规则,而菜单以 owner 身份跑。
//
// **它不重启任何东西。** 改完要 `bx down && bx up` 才生效,而那是一次断网 ——
// 必须是用户单独的、显式的一下,不能顺手替他做了。
func rulesHandler(configPath string, ownerUID uint32) http.HandlerFunc {
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
			serveRuleList(w, configPath)
		case http.MethodPost:
			applyRuleChange(w, r, configPath)
		default:
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

func serveRuleList(w http.ResponseWriter, configPath string) {
	rules, err := setup.ListRules(configPath)
	if err != nil {
		// 完整原因只进 Guardian 日志;响应体只带失败类别 —— 原始错误串里可能
		// 有路径、链接或凭据(与本仓库其它端点同一条纪律)。
		log.Printf("guardian_rules_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "rules_read_failed"})
		return
	}
	writeGuardianJSON(w, http.StatusOK, rulesResponse{
		Direct:          rules.Direct,
		Proxy:           rules.Proxy,
		RequiresRestart: true,
	})
}

func applyRuleChange(w http.ResponseWriter, r *http.Request, configPath string) {
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
		err = setup.AddRule(configPath, req.Kind, req.Pattern)
	case "remove":
		err = setup.RemoveRule(configPath, req.Kind, req.Pattern)
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
	serveRuleList(w, configPath)
}
