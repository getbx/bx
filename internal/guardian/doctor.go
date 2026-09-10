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

// doctorTimeout 是整轮采集的上限:探测 5 秒 + 其余。bx status 的观测是 5 秒,
// doctor 多一次出网探测。超时的项如实 warn,绝不让整份应答失败。
const doctorTimeout = 10 * time.Second

// probeOutcome 是探测的原始结果(与 supervisor.ProbeResult 同形,单独定义是为了
// 让单测不依赖 supervisor 的构造)。
type probeOutcome struct {
	Reachable bool
	RTTMS     int64
	Error     string
}

// doctorCollectorDeps 是采集的可注入原语:单测里 probe=nil ⇒ 不探(不出网),
// platform=nil ⇒ 不采平台检查,sock 指一条拨不通的路径 ⇒ 不碰真 Core。生产用
// liveDoctorDeps()。
type doctorCollectorDeps struct {
	probe    func(host string, port int) (probeOutcome, error)
	platform func(context.Context) []doctor.Check
	// sock 是 Core 控制 socket 的路径;空 = 生产那个常量。
	//
	// **它存在的唯一理由是让这一层的单测与机器状态无关**:开发机上 bx 正跑着,
	// supervisor.SockPath 真的拨得通,于是「拨不通就填 StatusSocketErr」这条
	// 断言会跟着「此刻有没有开保护」在红绿之间摇摆 —— 而它要守的性质与那件事
	// 毫无关系。同一条路上顺带把规则体检要问的 Core 也钉住(它读的是同一个
	// socket),免得单测去读用户正跑着的那份统计。
	sock string
}

func (d doctorCollectorDeps) sockPath() string {
	if d.sock == "" {
		return supervisor.SockPath
	}
	return d.sock
}

func liveDoctorDeps() doctorCollectorDeps {
	return doctorCollectorDeps{
		probe: func(host string, port int) (probeOutcome, error) {
			r, err := liveServerProbe(host, port)
			return probeOutcome{Reachable: r.Reachable, RTTMS: r.RTTMS, Error: r.Error}, err
		},
		platform: platformcheck.Collect,
	}
}

// collectDoctorFacts 是生产那份采集(localAPIOptionsFor 接的就是它)。
func collectDoctorFacts(ctx context.Context, configPath string, status Status) doctor.Facts {
	return collectDoctorFactsWith(ctx, configPath, status, liveDoctorDeps())
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
			f.Config.Mode0600 = info.Mode().Perm() == 0o600
		}
		cfg, perr := config.Parse(b)
		if perr != nil {
			f.ParseErr = perr.Error()
		} else {
			f.Parsed = cfg
			if cfg.Server != "" {
				if deps.probe != nil {
					f.Probe = doctorProbeCheck(cfg.Server, deps.probe)
				}
				// **Guardian 做的这份体检是完整的四类**:它有 root,读得到
				// 配置、Core 在用的那张 china 列表与累计历史(见 reviewRulesAt
				// 的类型头),而非 root 的 `bx doctor` 只能给三类。
				sock := deps.sockPath()
				f.RuleReview = reviewRulesAt(configPath, func() (stats.Report, error) {
					return supervisor.FetchStatusReport(sock)
				})
			}
		}
	}
	f.Service = doctor.DarwinServiceChecks(install.GuardianInstalled(), install.GuardianActive())
	if err := dialControlSocket(deps.sockPath()); err != nil {
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
	return f
}

// doctorProbeCheck 把一次探测折成 probe 那一行。**它与 bx doctor 那条不是同一种
// 探测**:CLI 做的是完整传输握手(setup.ProbeServer),Guardian 做的是经 Core
// 控制面的 TCP 往返。名字同为 probe(它是「服务器够不够得着」这一格),detail 里
// 写明是 tcp,读的人分得开。
//
// **三种结局要分开**:通、不通、以及**没探出来**。第三种判 warn 不判 fail ——
// 一次没拿到答案的探测被报成「服务器不可达」,会让人去修一台好好的服务器。
func doctorProbeCheck(link string, probe func(host string, port int) (probeOutcome, error)) *doctor.Check {
	host, ok := setup.LinkHost(link)
	port := setup.LinkPort(link)
	if !ok || host == "" || port == 0 {
		return &doctor.Check{Name: "probe", Status: "warn", Detail: "could not read the server address from the link"}
	}
	r, err := probe(host, port)
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
func dialControlSocket(path string) error {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

// doctorHandler 服务 GET /v1/doctor。owner 门(与 /v1/rules、/v1/logs 同一道);
// **门之后才采集** —— 采集会经 Core 出网探测一次,被拒的请求不许触发它。
func doctorHandler(collect DoctorFactsFunc, configPath string, ownerUID uint32, status func() Status) http.HandlerFunc {
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
		ctx, cancel := context.WithTimeout(r.Context(), doctorTimeout)
		defer cancel()
		rep := doctor.Judge(collect(ctx, configPath, status()))
		writeGuardianJSON(w, http.StatusOK, rep)
	}
}
