package guardian

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/platformcheck"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

// CapabilityDoctor:这一版 Guardian 会经 /v1/doctor 发布一份 doctor.Report。
//
// 菜单只在声明了它时才把「Check for Problems」指向 Checks 页,否则退回终端那条路
// —— 与 CapabilityLogs 同一条纪律:**绝不「试着拨一下看看」**,旧版 Guardian 对
// /v1/doctor 回的是 404,而 404 在菜单上表达不出来。
const CapabilityDoctor = "doctor"

// DoctorFactsFunc 是 Guardian 侧的事实采集。**判断一句都不在这里** —— 全在
// doctor.Judge;这一层与 internal/cli 的 collectDoctorFacts 是同一份判据的第二个
// 采集方。status 由 handler 算好传进来(它要 controller,采集函数不该自己去拿)。
type DoctorFactsFunc func(ctx context.Context, configPath string, status Status) doctor.Facts

// doctorTimeout 是整轮采集的上限。bx status 的观测是 5 秒,doctor 多一次出网探测
// (Core 侧那次探测自己的上限是 8 秒),故给 10 秒。超时的项如实 warn,绝不让
// 整份应答失败。
//
// **它必须是真的上限,不是一句声明** —— 采集里每一个会等的原语都要吃这个 ctx:
// 探测(`supervisor.ProbeControlContext`,它自己的客户端超时是 12 秒,比这里还长)、
// 问 launchd(`install.GuardianLoaded`,不给 ctx 的那个版本压根没有超时)、
// 拨 Core 控制 socket、以及规则体检里那次取统计。少接一个,这个常量就只是注释。
//
// 为什么较真:`Daemon.Shutdown` 要等在飞的 handler 返回,于是一次卡住的
// `launchctl` 会坐在 daemon 的关机路径上 —— **停止路径不许因为别的事没做完
// 而变慢**(2026-08-04 那次 71 分钟事故的同一条不变量)。
const doctorTimeout = 10 * time.Second

// probeOutcome 是探测的原始结果(与 supervisor.ProbeResult 同形,单独定义是为了
// 让单测不依赖 supervisor 的构造)。
type probeOutcome struct {
	Reachable bool
	RTTMS     int64
	Error     string
}

// doctorCollectorDeps 是采集的可注入原语。**每一个都吃 ctx**,因为整轮只有一份
// 预算(doctorTimeout),而一个不吃 ctx 的依赖会让那份预算变成一句空话。
//
// nil 的含义按用途分成两类,各自写明:
//   - probe / platform / directEgress:nil ⇒ **不做**。这三个是会出网/会 spawn
//     一堆命令的,单测必须能把它们整个关掉(测试不出网)。
//   - service / dial:nil ⇒ 用生产那份。它们是「问 launchd」与「拨本机 socket」,
//     单测跑真的也无害,而让它们可注入是为了能断言它们确实拿到了那份预算。
type doctorCollectorDeps struct {
	probe    func(ctx context.Context, host string, port int) (probeOutcome, error)
	platform func(context.Context) []doctor.Check
	service  func(context.Context) []doctor.Check
	dial     func(ctx context.Context, path string) error
	// directEgress 问「bx 自己的直连出不出得去」。**nil ⇒ Unknown,不是 True** ——
	// 「没问」与「问了、是好的」在这一格上必须分得开:判据只在明确观测到 False
	// 时才把「不是你的规则」那句话说出口,而把没问过读成好的,等于让诊断继续
	// 把一次系统故障说成用户的配置问题。
	directEgress func(context.Context) observe.Tristate
	// sock 是 Core 控制 socket 的路径;空 = 生产那个常量。
	//
	// **它存在的唯一理由是让这一层的单测与机器状态无关**:开发机上 bx 正跑着,
	// supervisor.SockPath 真的拨得通,于是「拨不通就填 StatusSocketErr」这条
	// 断言会跟着「此刻有没有开保护」在红绿之间摇摆 —— 而它要守的性质与那件事
	// 毫无关系。同一条路上顺带把规则体检与那次探测要问的 Core 也钉住(三者读的
	// 是同一个 socket),免得单测去读用户正跑着的那份统计。
	sock string
}

func (d doctorCollectorDeps) sockPath() string {
	if d.sock == "" {
		return supervisor.SockPath
	}
	return d.sock
}

// liveDoctorDeps 是生产那份原语。**sock 是参数,不是常量** —— 它管着的三件事
// (探测、拨控制 socket、规则体检里那次取统计)必须问的是**同一个** Core;
// 探测这一处曾单独写死 `supervisor.SockPath`,于是 `deps.sock` 那段注释
// (「同一条路上顺带把规则体检要问的 Core 也钉住」)对它并不成立,而单测把 sock
// 指到一个死路径时它照旧去拨用户正跑着的那份 —— 一份被机器状态左右的测试。
func liveDoctorDeps(sock string) doctorCollectorDeps {
	deps := doctorCollectorDeps{
		platform: platformcheck.Collect,
		service:  liveServiceChecks,
		dial:     dialControlSocket,
		sock:     sock,
	}
	deps.probe = func(ctx context.Context, host string, port int) (probeOutcome, error) {
		r, err := supervisor.ProbeControlContext(ctx, deps.sockPath(), host, port)
		return probeOutcome{Reachable: r.Reachable, RTTMS: r.RTTMS, Error: r.Error}, err
	}
	// 「bx 自己的直连出得去吗」。**走 observe 那一份判据,不自己问 supervisor** ——
	// (reachable, known, err) → Tristate 那段映射只许有一份,而 CLI 那侧的
	// doctor 采集读的正是它。只问这一个问题、不跑整轮 Observe:那要多两次路由
	// 查询、一次 DNS 查询、一次控制 socket 往返,而这一轮只有一份预算。
	deps.directEgress = func(ctx context.Context) observe.Tristate {
		return observe.DirectEgress(ctx, observe.LiveDeps(deps.sockPath()))
	}
	return deps
}

// liveServiceChecks 问 launchd「Guardian 这个服务加载了没有」。
//
// **走 GuardianLoaded(ctx) 而不是 GuardianActive()**:后者用的是
// context.Background(),`launchctl print` 一卡就是永远,而这一轮采集是有预算的。
// 语义逐字相同 —— 问不出来(err != nil)一律当成没加载,与 GuardianActive 把
// error 压成 false 是同一句话。
func liveServiceChecks(ctx context.Context) []doctor.Check {
	active, err := install.GuardianLoaded(ctx)
	if err != nil {
		active = false
	}
	return doctor.DarwinServiceChecks(install.GuardianInstalled(), active)
}

// collectDoctorFacts 是生产那份采集(localAPIOptionsFor 接的就是它)。
func collectDoctorFacts(ctx context.Context, configPath string, status Status) doctor.Facts {
	return collectDoctorFactsWith(ctx, configPath, status, liveDoctorDeps(supervisor.SockPath))
}

func collectDoctorFactsWith(ctx context.Context, configPath string, status Status, deps doctorCollectorDeps) doctor.Facts {
	f := doctor.Facts{Version: version.String(), ConfigPath: configPath, Darwin: runtime.GOOS == "darwin"}
	b, err := os.ReadFile(configPath)
	if err != nil {
		f.Config.ReadErr = err.Error()
		f.Config.PermissionDenied = errors.Is(err, fs.ErrPermission)
		// Guardian 自己就是 root:它读不到就没有第二条路 —— CLI 那侧的「权限
		// 不够就问 Guardian」退路在这里不存在,如实说,别让 Judge 以为还有人
		// 可以问。
		f.GuardianRules = doctor.GuardianRulesFact{
			ConfigPath: configPath,
			Err:        "guardian 自己也读不到这份配置:" + err.Error(),
		}
	} else {
		if info, serr := os.Stat(configPath); serr == nil {
			f.Config.Mode0600 = doctor.ConfigMode0600(info.Mode().Perm())
		}
		cfg, perr := config.Parse(b)
		if perr != nil {
			f.ParseErr = perr.Error()
		} else {
			f.Parsed = cfg
			if cfg.Server != "" {
				if deps.probe != nil {
					f.Probe = doctorProbeCheck(ctx, cfg.Server, deps.probe)
				}
				// **Guardian 做的这份体检是完整的四类**:它有 root,读得到
				// 配置、Core 在用的那张 china 列表与累计历史(见 reviewRulesAt
				// 的类型头),而非 root 的 `bx doctor` 只能给三类。
				sock := deps.sockPath()
				f.RuleReview = reviewRulesAt(configPath, func() (stats.Report, error) {
					return supervisor.FetchStatusReportContext(ctx, sock)
				})
			}
		}
	}
	service := deps.service
	if service == nil {
		service = liveServiceChecks
	}
	f.Service = service(ctx)
	dial := deps.dial
	if dial == nil {
		dial = dialControlSocket
	}
	if err := dial(ctx, deps.sockPath()); err != nil {
		f.StatusSocketErr = err.Error()
	}
	// **这里不抄 CLI 那侧的 darwin 门。** 那道门在 CLI 里的含义是「够不够得着
	// Guardian」(非 darwin 上根本问不到);而这里我们**就是** Guardian,自己的
	// 状态永远在手上,nil 在这个字段里的语义是「没问到」,填成 nil 就是撒谎。
	// 渲不渲染仍由 doctor.Judge 按 f.Darwin 决定,与这份事实无关。
	g := DoctorGuardianFact(status)
	f.Guardian = &g
	if deps.platform != nil {
		f.Platform = deps.platform(ctx)
	}
	// 流量成败。**这一份事实以前只有 CLI 的文本路径在采**,于是菜单的 Checks 页
	// (走的正是这个采集方)对「哪条规则在成片失败」一个字都不说、还顶着一句
	// 加粗的「0 failed」。Guardian 够得着 Core —— `failingRules` 早就为 /v1/status
	// 跨过这条边界了,这里只是让同一批数字也进 doctor。
	f.Traffic = guardianTrafficFact(ctx, deps.sockPath(), deps.directEgress)
	return f
}

// guardianTrafficFact 问一次 Core 的统计。**永不返回 nil**:问不到就带着原因
// 回来(Judge 会把它报成 not_checked),nil 的语义是「这条路径根本没问」。
//
// **DirectEgress 一起问**(2026-09-12 补)。此前这一格恒 Unknown,理由是
// 「原语住在 internal/observe,Guardian 这一侧没接」—— 诚实,但那正好把这份
// 诊断最值钱的一句话弃权掉了:2026-08-13 真机上十条 direct 规则 100% 失败,
// 坏的不是规则,是 bx 自己的直连器(macOS 上那条 scoped 默认路由不见了)。
// 这一格恒 Unknown 时,Checks 页会一本正经地建议用户去改那些**完全正确**的
// 规则 —— 一个在最需要它的时候把人指向错误方向的诊断,比不给建议更糟。
//
// egress 为 nil = 这条路径没问(单测把它关掉,或者哪天平台原语没接线)⇒
// Unknown。**绝不倒向任何一边**:判成 True 会让上面那句错的建议照旧发出去,
// 判成 False 会在一台规则真写错了的机器上告诉用户「不是你的规则」。
func guardianTrafficFact(ctx context.Context, sock string, egress func(context.Context) observe.Tristate) *doctor.TrafficFact {
	report, err := supervisor.FetchStatusReportContext(ctx, sock)
	fact := &doctor.TrafficFact{Report: report}
	if egress != nil {
		fact.DirectEgress = egress(ctx)
	}
	if err != nil {
		fact.Err = err.Error()
	}
	return fact
}

// doctorProbeCheck 把一次探测折成 probe 那一行。**它与 bx doctor 那条不是同一种
// 探测**:CLI 做的是完整传输握手(setup.ProbeServer),Guardian 做的是经 Core
// 控制面的 TCP 往返。名字同为 probe(它是「服务器够不够得着」这一格),detail 里
// 写明是 tcp,读的人分得开。
//
// **三种结局要分开**:通、不通、以及**没探出来**。第三种判 warn 不判 fail ——
// 一次没拿到答案的探测被报成「服务器不可达」,会让人去修一台好好的服务器。
func doctorProbeCheck(ctx context.Context, link string, probe func(ctx context.Context, host string, port int) (probeOutcome, error)) *doctor.Check {
	host, ok := setup.LinkHost(link)
	port := setup.LinkPort(link)
	if !ok || host == "" || port == 0 {
		return &doctor.Check{Name: "probe", Status: "warn", Detail: "could not read the server address from the link"}
	}
	r, err := probe(ctx, host, port)
	target := fmt.Sprintf("tcp %s:%d", host, port)
	switch {
	case err != nil:
		return &doctor.Check{Name: "probe", Status: "warn", Detail: target + " not probed: " + err.Error()}
	case !r.Reachable:
		return &doctor.Check{Name: "probe", Status: "fail", Detail: target + " " + r.Error}
	default:
		return &doctor.Check{Name: "probe", Status: "ok", Detail: fmt.Sprintf("%s %dms", target, r.RTTMS)}
	}
}

// dialControlSocket 只问「Core 的控制 socket 在不在应答」——**socket 应答本身
// 就是存活观测**,不需要 PID 文件(与 internal/observe 同一条)。
//
// 自己的 500 毫秒是「一次本机拨号该多快」,ctx 是整轮的预算 —— 两者取先到的那个。
func dialControlSocket(ctx context.Context, path string) error {
	conn, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	return conn.Close()
}

// doctorHandler 服务 GET /v1/doctor。owner 门(与 /v1/rules、/v1/logs 同一道);
// **门之后才采集** —— 采集会经 Core 出网探测一次,被拒的请求不许触发它。
//
// budget 做成参数而不是直接读 doctorTimeout:测「卡住的依赖不会让 handler 活过
// 它的预算」得能注入一个很短的值,而一条要睡十秒的测试没人愿意留着。生产接线
// 传的就是 doctorTimeout(由 TestNewLocalAPIGivesDoctorTheRealBudget 钉住)。
func doctorHandler(collect DoctorFactsFunc, configPath string, ownerUID uint32, status func() Status, budget time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "doctor requires owner or root peer"})
			return
		}
		// 「没接线」回 501,不是一份看起来正常的空报告 —— 一份没有 check 的
		// Report 与「你的机器全好」在应答上完全一样。
		if collect == nil || configPath == "" {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "doctor unavailable: not wired"})
			return
		}
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		uid, _ := peerUIDFrom(r.Context())
		log.Printf("guardian_doctor_requested uid=%d", uid)
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		started := time.Now()
		rep := doctor.Judge(collect(ctx, configPath, status()))
		// 结局那一行(与 mutation handler 的 guardian_mutation_result 同形)。
		// 少了它,「采集卡到预算耗尽」与「一切正常、几毫秒答完」在日志里逐字
		// 相同 —— 而 elapsed 正是那份 10 秒预算唯一留得下的证据。
		// **只发数字,不发内容**:check 的 detail 里有服务器地址。
		log.Printf("guardian_doctor_result uid=%d ok=%t checks=%d elapsed=%s",
			uid, rep.OK, len(rep.Checks), time.Since(started).Round(time.Millisecond))
		writeGuardianJSON(w, http.StatusOK, rep)
	}
}
