package guardian

import (
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/getbx/bx/internal/install"
)

// CapabilityLogs:这一版 Guardian 会经 /v1/logs 发布自己与 Core 的日志尾部。
//
// 菜单只在声明了它时才画「Show Details」/ 日志页 —— 旧版 Guardian 对 /v1/logs
// 回 404,而 404 在菜单上表达不出来(**绝不「试着拨一下看看」**)。
const CapabilityLogs = "logs"

// LogSource 是一份要发布的日志:名字给界面,路径给 tailLines。
type LogSource struct {
	Name string
	Path string
}

// LogTail 是一份日志的尾部。**Lines 无 omitempty**:空数组是「这份日志是空的」,
// 键缺席会被菜单读成「这份没给」;Unavailable 非空表示没读到(路径也照给,让人
// 知道去哪找)。
type LogTail struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Lines       []string `json:"lines"`
	Unavailable string   `json:"unavailable,omitempty"`
}

type LogsResponse struct {
	Logs []LogTail `json:"logs"`
}

const (
	logsDefaultLines = 200
	logsMaxLines     = 2000
)

// guardianLogSources 把 install 那份路径清单配上名字。**路径不在这里另抄一份**
// (TestGuardianLogsServeTheInstalledPaths 钉着)。非 darwin 为 nil ⇒ 端点 501。
func guardianLogSources() []LogSource {
	var out []LogSource
	for _, path := range install.GuardianLogPaths() {
		out = append(out, LogSource{Name: logSourceName(path), Path: path})
	}
	return out
}

func logSourceName(path string) string {
	switch path {
	case install.GuardianStdoutLogPath:
		return "guardian"
	case install.GuardianStderrLogPath:
		return "guardian-errors"
	case install.CoreLogPath():
		return "core"
	}
	return filepath.Base(path)
}

// logsHandler 服务 GET /v1/logs?lines=N。
//
// **发布面记录**:此前这些日志只有 root 读得到(SecureGuardianLogs 收成 0600),
// 现在 owner 也读得到 —— 门与 /v1/down 同一道:owner 本来就能关掉保护,能做
// 更坏的事;日志里的服务器 IP / bypass 网段与 `bx status --json` 已发布的同量级。
// 不做脱敏、不做过滤。
func logsHandler(sources []LogSource, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "logs require owner or root peer"})
			return
		}
		if sources == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "logs unavailable on this platform"})
			return
		}
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		lines := logsDefaultLines
		if raw := r.URL.Query().Get("lines"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > logsMaxLines {
				writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "logs_bad_request"})
				return
			}
			lines = n
		}
		resp := LogsResponse{Logs: make([]LogTail, 0, len(sources))}
		for _, src := range sources {
			tail := LogTail{Name: src.Name, Path: src.Path, Lines: []string{}}
			got, err := tailLines(src.Path, lines)
			if err != nil {
				// 完整原因进日志;响应里只说这份读不到 —— 与其它端点同一条纪律。
				log.Printf("guardian_logs_read_failed name=%s path=%s err=%v", src.Name, src.Path, err)
				tail.Unavailable = "could not read this log"
			} else {
				tail.Lines = got
			}
			resp.Logs = append(resp.Logs, tail)
		}
		writeGuardianJSON(w, http.StatusOK, resp)
	}
}
