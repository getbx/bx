package guardian

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/supervisor"
)

// coreStartFailureGrace:Guardian 的健康等待已经放弃之后,**再给 Core 多少
// 时间把那份记录写出来**。
//
// 它不是保险,是这一整支修复能不能生效的分水岭。时序是硬事实:
//
//	Guardian 的 defaultHealthTimeout            = 20s
//	Core 的隧道健康窗口(run.go 兜底 / --health-timeout 默认值)= 20s
//	Guardian **从不**给 Core 传 --health-timeout
//
// 两个计时器几乎同时起跑,于是 Guardian 放弃的那一刻,Core 才刚开始那次判别
// 拨号(最长 supervisor.TunnelDiagnosisTimeout),还没写下任何东西 —— 而紧接着
// Guardian 就把它 SIGKILL 了(批一 Task 1 的强杀路径)。不留这段余量,
// 「Core 自报」在它唯一存在的那个场景(隧道起不来 = 2026-09-12 那次事故)里
// **一次都不会生效**,而两侧测试都不会红。
//
// **为什么不是「把 Core 的健康窗口调短」**:那会真的缩短隧道能用多久建起来,
// 是一次产品行为改动 —— 一条 reality 握手在烂链路上慢一点就此起不来。
// **为什么不是「把 Guardian 的健康等待调长」**:那是同样长的等待,却连
// 「答案已经到了」都不看,一律等满。
//
// 这段等待**只在启动已经失败之后**发生,而且一拿到答案就返回;Core 早早死掉
// (config/provision/tun_open/hijack 那几种约 2 秒就写完退出)时它一拍就走。
// 它吃调用方的 ctx —— 用户取消就立刻停,mutation 槽不会被它多押住。
const coreStartFailureGrace = supervisor.TunnelDiagnosisTimeout + 3*time.Second

// coreStartFailurePoll 是等那份记录出现的轮询间隔。
const coreStartFailurePoll = 200 * time.Millisecond

// startFailureReadContext 给「读 Core 自报的那句话」一份**绝不动清理预算**的 ctx。
//
// 这段等待坐在 reserveCleanup 与 cleanupCoreAfterFailedStart 中间,而
// reserveCleanup 存在的全部意义就是「无论上面那些事花了多久,收拾这个 Core
// 的预算还在」。裸拿外层 ctx 去等,花的就是那份预留:
//
//	/v1/up 的 60s 有富余 —— 健康门 20s 到期,清理仍有 25s;
//	而 **三个** startCoreLocked 调用点传的是 m.restartTimeout = 25s:
//	  manager.go 的崩溃重启、reconcile_execute.go 的调谐环 start_core、
//	  以及 **Manager.Down 里 DNS 还原失败之后那次补偿重启 —— 一条停止路径**。
//	那里 reserveCleanup 扣下 min(25, 12.5) = 12.5s,健康门在 t+12.5 断,
//	8 秒宽限等到 t+20.5,清理只剩 4.5s(本该 12.5s)⇒ 清理超时 ⇒
//	retainUncertain ⇒ **core_ownership_uncertain**,正是这一整支要消灭的那条。
//
// 「停止路径不许因为别的事没做完而变慢或失败」是本仓库最硬的一条不变量
// (2026-08-04 那次 71 分钟事故),而 Down 那条补偿恰好在它上面。
//
// 而且在那三条路上宽限是**纯成本**:Guardian 的耐心只有 12.5 秒,而 Core 要到
// 约 25 秒之后才会为「隧道那一族」写下任何东西 —— 唯一需要宽限的那一族根本
// 等不到。**余量不够就一拍都不等**(仍然读一次:config/provision/tun_open/
// hijack 那几种约两秒就写完退出了,记录早在那儿)。
//
// 上界取 operationCtx 的 deadline,**不是自己再算一遍 min(cleanupTimeout,
// remaining/2)** —— 那就是第二份判据,而它会与 reserveCleanup 漂开。
func startFailureReadContext(ctx, operationCtx context.Context) (context.Context, context.CancelFunc) {
	budget := coreStartFailureGrace
	if deadline, ok := operationCtx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		// 已经过期的 ctx:awaitStartFailureRecord 的第一次读排在任何 ctx 检查
		// 之前(由 TestZeroGraceStillReadsOnce 钉住),于是「记录早就写好了」
		// 那几种照样拿得到,而一拍都不等。
		return context.WithDeadline(ctx, time.Now())
	}
	return context.WithTimeout(ctx, budget)
}

// startFailurePath 是 Core 自报那份记录的位置(空 = 这条路整个关掉)。
func (r *ExecCoreRunner) startFailurePath() string { return r.StartFailurePath }

// discardStaleStartFailureRecord 在每次 spawn 之前把旧记录删掉 —— 陈旧记录的
// **第一层**防线。
//
// 第二层是 PID + 本次健康窗口双重匹配(StartFailureCode)。两层都要:上一次
// spawn 留下的记录与这一次的长得一模一样,而这个仓库为陈旧文件栽过三次
// (upgrade-intent.json、core-process.json、那份四分之三是假的缺口清单)。
//
// **删不掉只记一行日志。** 起 Core 不许因为一次诊断准备工作没做成而失败;
// 而漏删的后果由第二层兜住。
func (r *ExecCoreRunner) discardStaleStartFailureRecord() {
	path := r.startFailurePath()
	if path == "" {
		return
	}
	if err := corestartfailure.Remove(path); err != nil {
		log.Printf("guardian_core_start_failure_record_stale_remove_failed path=%s err=%v", path, err)
	}
}

// StartFailureCode 读这一次 spawn 的 Core 自己报的失败码。
//
// **对不上就是「这一次没说」,绝不猜。** 三道:
//   - pid 必须等于本次 fork 出来的那个 PID。这一条是决定性的 —— Guardian 在
//     一段故障里会连着 spawn 好几个 Core(事故日志里五个),少了它,上一次的
//     码会被这一次采信,而那是一句关于错误对象的、自信的假话;
//   - at 必须落在本次健康窗口内。承重的是**下界**(since,取自 fork 之前的
//     那一刻):它挡的正是上一轮留下的记录。上界只挡一个时间戳在未来的垃圾
//     记录 —— 记录必定写在我们读之前,这一半天然成立,写在这里是为了不让
//     一个被人改过的 at 蒙混过去;
//   - code 必须真的属于这一族(supervisor.IsStartFailureCode)。盘上一个被改过
//     的字符串不许直接决定 Status.LastError 上写什么。
//
// **读完即删**;删不掉只记日志,不升级成失败(停止/诊断路径不许因为别的事
// 没做成而失败)。
func (r *ExecCoreRunner) StartFailureCode(ctx context.Context, process Process, since time.Time) string {
	path := r.startFailurePath()
	if path == "" {
		return ""
	}
	record, ok := r.awaitStartFailureRecord(ctx, path, process)
	if !ok {
		return ""
	}
	defer func() {
		if err := corestartfailure.Remove(path); err != nil {
			log.Printf("guardian_core_start_failure_record_remove_failed path=%s err=%v", path, err)
		}
	}()
	switch {
	case record.PID != process.PID:
		log.Printf("guardian_core_start_failure_record_ignored reason=pid_mismatch record_pid=%d spawn_pid=%d",
			record.PID, process.PID)
		return ""
	case record.At.Before(since) || record.At.After(time.Now()):
		log.Printf("guardian_core_start_failure_record_ignored reason=outside_health_window at=%s since=%s",
			record.At.Format(time.RFC3339Nano), since.Format(time.RFC3339Nano))
		return ""
	case !supervisor.IsStartFailureCode(record.Code):
		log.Printf("guardian_core_start_failure_record_ignored reason=unknown_code")
		return ""
	}
	return record.Code
}

// awaitStartFailureRecord 等那份记录出现,最长 coreStartFailureGrace。
//
// **只要那个 Core 的句柄还在就继续等,句柄一没就再读最后一次然后收手。**
// 顺序是承重的:Core 先写记录、再退出,父进程的 waitpid 返回之后才
// forgetStartedCore —— 所以「句柄没了」蕴含「该写的都写完并且可见了」,
// 但必须在观测到句柄消失**之后**再读一次,反过来会漏掉最后那一瞬写下的东西。
//
// 句柄本来就不在(不是我们 fork 的、或者早被摘掉)时只读一次:那种 Core 不会
// 再往这个位置写任何东西,等下去只是白白押着 mutation 槽。
func (r *ExecCoreRunner) awaitStartFailureRecord(ctx context.Context, path string, process Process) (corestartfailure.Record, bool) {
	deadline := time.Now().Add(coreStartFailureGrace)
	for {
		_, tracked := r.startedCoreHandle(process)
		record, err := corestartfailure.Read(path)
		if err == nil {
			return record, true
		}
		if !errors.Is(err, os.ErrNotExist) {
			// 读得到文件却读不懂它(坏 JSON / 认不出的 schema)⇒ 这一次没说。
			// 再等下去也是同一份读不懂的东西。
			log.Printf("guardian_core_start_failure_record_unreadable path=%s err=%v", path, err)
			return corestartfailure.Record{}, false
		}
		if !tracked {
			return corestartfailure.Record{}, false
		}
		if !time.Now().Before(deadline) {
			return corestartfailure.Record{}, false
		}
		timer := time.NewTimer(coreStartFailurePoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return corestartfailure.Record{}, false
		case <-timer.C:
		}
	}
}

// coreStartFailureLastError 把 Core 自报的那个码翻成 Guardian 的失败码。
//
// **前缀 core_ 是刻意的**:Status.LastError 那个命名空间里的每一条都在说
// 「Guardian 这一侧发生了什么」(core_health_failed / core_start_failed /
// core_ownership_uncertain),而这些新码说的是「Core 那一侧发生了什么」——
// 不带前缀会让 tunnel_unreachable 看起来像是 Guardian 自己拨不通。
//
// **Core 没说 ⇒ core_health_failed。** 那句话仍然是真的(健康等待确实失败了),
// 只是没有那一层「为什么」—— 与编一个具体病因相比,这正是本仓库那条
// 「问不出来不是任何一个具体答案」的落点。
func coreStartFailureLastError(reported string) string {
	if reported == "" {
		return "core_health_failed"
	}
	return "core_" + reported
}

// coreReportedStartFailure 问 runner「Core 自己说了什么」。
//
// **不做可选类型断言。** StartFailureCode 就在 CoreRunner 上(见那里的注释):
// 一次断言失败等于这个功能静默消失,而它与「功能不存在」在输出上完全一样。
//
// process 与 since 是新鲜度判据的**全部**:PID 认「是不是这一次那个 Core」,
// since(fork **之前**取的那一刻)认「是不是这一次写的」。递错任何一个,
// 每一份记录都会被丢掉而回落 core_health_failed —— 也就是 2026-09-12 那天
// 用户读到的那句话。由 TestUpHandsTheReaderThisSpawnsProcessAndAPreForkInstant
// 钉住**递过去的那两个值**,不是「这次调用发生过」。
func (m *Manager) coreReportedStartFailure(ctx context.Context, process Process, since time.Time) string {
	return m.runner.StartFailureCode(ctx, process, since)
}
