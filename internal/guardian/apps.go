package guardian

import (
	"log"
	"net/http"

	"github.com/getbx/bx/internal/supervisor"
)

// appsHandler 服务 GET /v1/apps —— 把 Core 的应用流量归因报告原样转发给菜单。
//
// **形状照抄 rulesHandler**:授权门(authorizeOwnerPeer)→「没接线」回 501 →
// 方法分发 → 转发。判据不是「应用流量比开关保护更敏感」——恰恰相反,能关掉
// 保护的人已经能做更坏的事,取一致才是要点(与 /v1/rules、/v1/up、/v1/down
// 同一道门)。
//
// sockPath 空串是「没接线」的信号,与 rulesHandler 的 configPath 同一条纪律:
// 「没接线」不是「没有应用」——空报告会让菜单显示「一个应用都没有」,而事实是
// 这条链没接上。生产环境里它恒为 supervisor.SockPath(见 NewLocalAPI 的注册),
// 只有测试会传别的值或空串。
//
// **三态原样透传,这一层绝不合并或压平**:supervisor.FetchAppTraffic 已经把
// 「没人订阅」/「订阅了但问不出来」/「订阅了且确实没连接」三种情形分开发布在
// AppTrafficResponse 里(见 internal/supervisor/control.go 的 handleApps),
// Guardian 只是把那份应答整个转发出去,不重新判断、不重新聚合、不先碰
// Report 再判 Error。
func appsHandler(sockPath string, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "apps require owner or root peer"})
			return
		}
		if sockPath == "" {
			// 「没接线」不是「没有应用」—— 与 rulesHandler 同源。
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "apps unavailable: no core socket"})
			return
		}
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		// FetchAppTraffic 自己已经把三态分开装进 AppTrafficResponse,这里只做
		// 一次 GET + 原样转发,绝不拆开重装。
		resp, err := supervisor.FetchAppTraffic(sockPath)
		if err != nil {
			// 完整原因只进 Guardian 日志;响应体只带失败类别 —— 原始错误串里
			// 可能有路径,与本仓库其它端点同一条纪律。
			log.Printf("guardian_apps_fetch_failed err=%v", err)
			writeGuardianJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "apps_fetch_failed"})
			return
		}
		writeGuardianJSON(w, http.StatusOK, resp)
	}
}
