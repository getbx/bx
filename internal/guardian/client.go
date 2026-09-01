package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/supervisor"
)

type Client struct {
	SocketPath string
	HTTPClient *http.Client
}

type UnavailableError struct {
	Err error
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("Guardian unavailable: %v", e.Err)
}

func (e *UnavailableError) Unwrap() error {
	return e.Err
}

type AmbiguousRecoveryError struct {
	Err error
}

func (e *AmbiguousRecoveryError) Error() string {
	return fmt.Sprintf("Guardian recovery request may have been accepted: %v", e.Err)
}

func (e *AmbiguousRecoveryError) Unwrap() error {
	return e.Err
}

type guardianDialError struct {
	err error
}

func (e *guardianDialError) Error() string {
	return e.err.Error()
}

func (e *guardianDialError) Unwrap() error {
	return e.err
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath}
}

// NewClientWithTimeout 与 NewClient 同,但 HTTPClient 超时为给定值:update 事务
// 服务端上限 guardianMutationTimeout(60s),调用方按需给出余量(如 90s)以覆盖
// 一次完整 barrier/activate/commit 往返而不提前掐断连接。
func NewClientWithTimeout(socketPath string, timeout time.Duration) *Client {
	client := guardianHTTPClient(socketPath)
	client.Timeout = timeout
	return &Client{SocketPath: socketPath, HTTPClient: client}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodGet, "/v1/status", nil)
}

// WatchMaxHold 导出 statuswatch.go 里那个未导出的 watchMaxHold(服务端长轮询
// 挂住的上限),供包外调用方(如 bx status --watch)钉住「客户端超时必须比
// 服务端上限长」这条不等式——否则拿到的永远是自己的超时,服务端那个上限
// 一次都不会生效。
const WatchMaxHold = watchMaxHold

// StatusWatch 是 Status 的长轮询形态:Guardian 在自己的代际号与 generation
// 不同时立刻应答,相同则挂住到它变了或服务端超时(见 WatchMaxHold)。
//
// query 直接拼在 path 上——request 只做 "http://local"+path,本包没有
// url.Values 那一层。
func (c *Client) StatusWatch(ctx context.Context, generation uint64) (Status, error) {
	return c.request(ctx, http.MethodGet, "/v1/status?wait="+strconv.FormatUint(generation, 10), nil)
}

func (c *Client) Up(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodPost, "/v1/up", nil)
}

func (c *Client) Down(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodPost, "/v1/down", nil)
}

// DownForUpgrade 停保护,但把这一跳标记成**维护**而不是「用户不要保护了」。
//
// 两个后果,缺一不可:desired **不被改写**(用户想要保护,只是此刻不能有),
// 以及前一秒才武装的那张维护挂起**不被销掉**(它正是拦住新 Guardian 在二进制
// 换到一半时把 Core 起回来的东西)。普通的 Down 两件都会做。
//
// 调用方只有一个:sudo bx app-install 的停保护步骤。用户明确说 off 的每一条路
// (bx down、菜单 Turn Off)都必须继续用 Down,那才是销挂起的正确时机。
func (c *Client) DownForUpgrade(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodPost, downForUpgradePath, nil)
}

func (c *Client) Migrate(ctx context.Context, request MigrationRequest) (Status, error) {
	normalized, err := ValidateMigrationRequest(request)
	if err != nil {
		return Status{}, err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return Status{}, err
	}
	return c.request(ctx, http.MethodPost, "/v1/migrate", bytes.NewReader(body))
}

func (c *Client) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	normalized, err := ValidateUpdateRequest(request)
	if err != nil {
		return UpdateResult{}, err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return UpdateResult{}, err
	}
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v1/update", bytes.NewReader(body))
	if err != nil {
		return UpdateResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return UpdateResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return UpdateResult{}, guardianHTTPError("/v1/update", response.StatusCode, body)
	}
	var result UpdateResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

func (c *Client) RequestRecovery(ctx context.Context, in RecoveryRequest) (RecoverySnapshot, error) {
	normalized, err := ValidateRecoveryRequest(in)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	return c.recoveryRequest(ctx, http.MethodPost, "/v1/recoveries", bytes.NewReader(body), http.StatusAccepted)
}

func (c *Client) CurrentRecovery(ctx context.Context) (RecoverySnapshot, error) {
	return c.recoveryRequest(ctx, http.MethodGet, "/v1/recoveries/current", nil, http.StatusOK)
}

// RuleList 是 GET /v1/rules 的规则那一半。
//
// **只带 Direct/Proxy,不带 groups/custom**:那两样是给菜单分组显示用的派生物,
// 而这个客户端的消费方(bx doctor 的规则体检)要的是原文两张表。少发一样就少
// 一份会漂移的拷贝。
type RuleList struct {
	Direct []string `json:"direct"`
	Proxy  []string `json:"proxy"`
	// ConfigPath 是 Guardian **实际读的那个文件**。调用方必须拿它与自己要问的
	// 路径比对:否则 `--config /somewhere/else` 会被一份来自
	// /etc/bx/config.yaml 的答案冒名顶替 —— 判据没错、读错了输入,
	// 与 buildRuleReviewInput 头上那段 wrong-reference-object 警告同一形状。
	ConfigPath string `json:"config_path"`
	// Review 是 **Guardian 自己算的**规则体检。它有 root,读得到配置与 Core
	// 实际在用的那张 china 列表,所以给得出完整的四类;客户端自己算只能给三类
	// (无从知道用户有没有指定自己的列表)。
	//
	// **nil 与空报告是两件事**:nil = 这一版没做 / 配置读不到 =「没查」;
	// 空报告 = 查过了、没有问题。压成同一个东西正是这个功能最贵的教训。
	Review *rulereview.Report `json:"review"`
}

// Rules 读 GET /v1/rules。
//
// **它存在的理由是 root 那道墙**:/etc/bx/config.yaml 是 0600 root-only,而
// MCP 的前提是 agent 以业主身份免 sudo 操作 bx —— 非 root 的 bx doctor 因此
// 读不到配置,整块规则体检被静默跳过。这个端点走 authorizeOwnerPeer(业主即可),
// 读的正是同一个文件,只是 Guardian 有 root:**同一份真相,换一条被授权的路。**
func (c *Client) Rules(ctx context.Context) (RuleList, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local/v1/rules", nil)
	if err != nil {
		return RuleList{}, err
	}
	response, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RuleList{}, ctxErr
		}
		var dialErr *guardianDialError
		if errors.As(err, &dialErr) {
			return RuleList{}, &UnavailableError{Err: dialErr.err}
		}
		return RuleList{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return RuleList{}, guardianHTTPError("/v1/rules", response.StatusCode, body)
	}
	var list RuleList
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		return RuleList{}, err
	}
	return list, nil
}

func (c *Client) recoveryRequest(ctx context.Context, method, path string, body io.Reader, expectedStatus int) (RecoverySnapshot, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local"+path, body)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoverySnapshot{}, ctxErr
		}
		var dialErr *guardianDialError
		if errors.As(err, &dialErr) {
			return RecoverySnapshot{}, &UnavailableError{Err: dialErr.err}
		}
		return RecoverySnapshot{}, &AmbiguousRecoveryError{Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		body, _ := io.ReadAll(response.Body)
		return RecoverySnapshot{}, guardianHTTPError(path, response.StatusCode, body)
	}
	var snapshot RecoverySnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		if method == http.MethodPost {
			return RecoverySnapshot{}, &AmbiguousRecoveryError{Err: err}
		}
		return RecoverySnapshot{}, err
	}
	return redactRecoverySnapshot(snapshot), nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (Status, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local"+path, body)
	if err != nil {
		return Status{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return Status{}, guardianHTTPError(path, response.StatusCode, body)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return Status{}, err
	}
	status.Recovery = redactRecoverySnapshot(status.Recovery)
	if status.NetworkGeneration == "" {
		status.NetworkGeneration = status.Recovery.Generation
	}
	return status, nil
}

// guardianFailureBody mirrors failureResponseBody in localapi.go: the JSON
// shape Guardian's four mutation handlers (mutation/update/migration/
// recoveryRequest) write on a 500 response. "code" is present when the
// failure is one of the named short circuits (recovery_incomplete /
// guardian_busy) or when it actually set a fresh LastError this call — see
// failureResponseBody's comment for why a missing code must stay missing
// rather than replay a stale one.
type guardianFailureBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// guardianTroubleshootingHint names the Guardian log explicitly: `bx logs`
// reads the *Core* launchd logs (/var/log/bx.log), while the full reason for
// a Guardian failure — the raw error the response deliberately withholds —
// is only ever written to the Guardian log. During the 2026-08-05 incident
// the diagnostics archive was consulted and yielded stale Core logs, which
// is exactly what pointing at the wrong log produces.
const guardianTroubleshootingHint = "排查:sudo bx doctor;完整原因见 Guardian 日志 sudo tail -50 " +
	install.GuardianStderrLogPath + "(bx logs 看的是 Core 日志,不含 Guardian 失败原因)"

// guardianCodeHints carries the per-code next step for failures the generic
// hint cannot resolve.
//
// core_ownership_uncertain is no longer a latch that only Down() clears
// (2026-08-11). User-initiated Up/Migrate now re-verify on every attempt via
// Manager.recheckOwnershipUncertain: two clean scans separated by a settle
// window release the remembered judgement, anything else keeps refusing. So
// the first thing to tell the user is that retrying is meaningful — and that a
// refusal which survives a retry means a Core really is running, or the scan
// cannot answer at all. Neither can be promised: on a machine that genuinely
// has a second Core, both the retry and down+up are supposed to keep refusing,
// and off darwin scanning is unsupported so the door stays welded shut.
//
// The Guardian response body deliberately withholds the raw error (it may
// carry paths/links/credentials), which is why the wording lives here on the
// CLI side rather than in the error text the daemon produces.
var guardianCodeHints = map[string]string{
	"core_ownership_uncertain": "Guardian 没能证明系统里没有第二个 bx Core 在跑,于是拒绝再起一个" +
		"(两个 Core 会争默认路由,先退出的那个用旧快照还原、掀掉另一个的劫持)。" +
		"每次 sudo bx up 都会重新求证,所以直接重试是有意义的;仍然被拒就说明系统里真有一个 Core、" +
		"或者根本扫不动 —— 先 sudo tail -50 " + install.GuardianStderrLogPath +
		" 看是扫到了哪个进程(guardian_core_still_running_on_release / guardian_core_scan)," +
		"若是另一个终端里的 sudo bx run,退出它再重试。" +
		"sudo bx down 再 sudo bx up 会让 Guardian 忘掉这条判定,但那个 Core 还跑着时它同样会被拒",
	// 维护挂起读不出来:Guardian 一律 fail-closed(不起 Core),而保护**不会**
	// 自己恢复。挂起只是一次升级留下的临时标记,内容读不懂时直接删掉即可 ——
	// 这条出路必须写在 CLI 侧:响应体刻意不外传原始错误串,daemon 那边写的
	// 错误文本用户根本看不到。
	"intent_unreadable": "维护挂起文件读不出来,保护不会自动恢复:检查 " + defaultMaintenanceHoldPath +
		"(它只是一次升级的临时标记,可直接删除),再 sudo bx up",
	// 挂起删不掉(多半是 /var/lib/bx 或那个文件本身不可写)。Guardian **拒绝**
	// 在这种情况下打开保护:挂起还武装着而保护开着,意味着 Core 一退出就既不
	// 重启也不装屏障,保护会在用户以为开着的时候悄悄退回明文直连。
	maintenanceHoldClearFailedCode: "维护挂起删不掉,保护因此没有打开(挂起还在就等于 Core 退出后不会被拉回来):" +
		"检查 " + defaultMaintenanceHoldPath + " 及其目录是否可写(ls -l /var/lib/bx)," +
		"必要时 sudo rm -f " + defaultMaintenanceHoldPath + " 后重试 sudo bx up",
	// recoveryBlocked 是**锁存**的:Up 的第一句就短路,而那句检查排在销挂起
	// 之前 —— 于是 `bx up` 既不会清挂起也不会启动。Down 会清掉这个状态
	// (它自己的 defer 也会销挂起),所以出路只有一条,必须写出来。
	"recovery_incomplete": "上一次启动恢复没能完成,Guardian 把后续操作锁住了(bx up 会在第一句就返回):" +
		"先 sudo bx down 再 sudo bx up —— 只有 down 会清掉这个锁存状态",
}

// guardianHTTPError renders a Guardian error response. Every 500 carries the
// troubleshooting hint, with or without a code: the failures that arrive
// without one (short circuits that never reach needsAttention, or an older
// Guardian) are precisely the ones a user cannot act on unaided, so gating
// the hint on "has a code" withheld it from the cases that needed it most.
func guardianHTTPError(path string, statusCode int, body []byte) error {
	var failure guardianFailureBody
	_ = json.Unmarshal(body, &failure)
	message := fmt.Sprintf("Guardian %s returned %d", path, statusCode)
	if failure.Error != "" {
		message += ": " + failure.Error
	}
	if failure.Code != "" {
		message += fmt.Sprintf("(code=%s)", failure.Code)
	}
	if statusCode == http.StatusInternalServerError {
		// 专用指引在前、通用排查在后:前者是这一类失败的直接出路。
		// 与通用指引一样只挂在 500 上(非 500 保持素净,见
		// TestGuardianHTTPErrorNon500StaysPlain)。
		if hint := guardianCodeHints[failure.Code]; hint != "" {
			message += "。" + hint
		}
		message += "。" + guardianTroubleshootingHint
	}
	return errors.New(message)
}

func guardianHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
			if err != nil {
				return nil, &guardianDialError{err: err}
			}
			return conn, nil
		}},
	}
}

// AddServer 把一台服务器加进清单,**不动 current**(见 addServerEntry)。
//
// 它存在的理由是 `bx server deploy`:部署完一台新 VPS 之后要把它记下来,而写
// /etc/bx/config.yaml 要 root —— deploy 跑在用户身份下。经 Guardian 走,是**唯一
// 一条不用中途弹提权框的路**;而 Guardian 那一侧的门是 authorizeOwnerPeer,
// 与菜单换服务器同一道。
func (c *Client) AddServer(ctx context.Context, name, link, udp string) error {
	payload, err := json.Marshal(map[string]string{
		"action": "add", "name": name, "link": link, "udp": udp,
	})
	if err != nil {
		return err
	}
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v1/servers", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		var dialErr *guardianDialError
		if errors.As(err, &dialErr) {
			return &UnavailableError{Err: dialErr.err}
		}
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return guardianHTTPError("/v1/servers", response.StatusCode, body)
	}
	return nil
}

// ListServers 取服务器清单(含吞吐历史)。
//
// **CLI 经 Guardian 拿,而不是自己读盘**:吞吐历史是 0600 root 的,而
// `bx server list` 常常以普通用户跑;更要紧的是,两处各读一份就会各渲染一份,
// 而菜单与 CLI 对同一台服务器说不同的话正是这个仓库反复栽的形状。
func (c *Client) ListServers(ctx context.Context) (ServerListResponse, error) {
	return c.serversRequest(ctx, http.MethodGet, nil)
}

// ProbeServers 逐台量一次直连往返时间,返回带结论的完整清单。
//
// 探测走在隧道外面,所以它只该由用户显式触发(`bx server list --test`)。
func (c *Client) ProbeServers(ctx context.Context) (ServerListResponse, error) {
	payload, err := json.Marshal(serversRequest{Action: "probe"})
	if err != nil {
		return ServerListResponse{}, err
	}
	return c.serversRequest(ctx, http.MethodPost, payload)
}

func (c *Client) serversRequest(ctx context.Context, method string, payload []byte) (ServerListResponse, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local/v1/servers", body)
	if err != nil {
		return ServerListResponse{}, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		var dialErr *guardianDialError
		if errors.As(err, &dialErr) {
			return ServerListResponse{}, &UnavailableError{Err: dialErr.err}
		}
		return ServerListResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		return ServerListResponse{}, guardianHTTPError("/v1/servers", response.StatusCode, raw)
	}
	var out ServerListResponse
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return ServerListResponse{}, err
	}
	return out, nil
}

// AppTraffic 取一份应用流量归因报告(GET /v1/apps)。授权门与规则/服务器同一道
// (authorizeOwnerPeer),三态(没人订阅 / 订阅了但问不出来 / 订阅了且确实没有
// 连接)原样透传 —— 与 Guardian 那一侧同一条纪律,这里不重新判断、不重新聚合。
func (c *Client) AppTraffic(ctx context.Context) (supervisor.AppTrafficResponse, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local/v1/apps", nil)
	if err != nil {
		return supervisor.AppTrafficResponse{}, err
	}
	response, err := client.Do(req)
	if err != nil {
		var dialErr *guardianDialError
		if errors.As(err, &dialErr) {
			return supervisor.AppTrafficResponse{}, &UnavailableError{Err: dialErr.err}
		}
		return supervisor.AppTrafficResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		return supervisor.AppTrafficResponse{}, guardianHTTPError("/v1/apps", response.StatusCode, raw)
	}
	var out supervisor.AppTrafficResponse
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return supervisor.AppTrafficResponse{}, err
	}
	return out, nil
}
