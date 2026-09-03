package guardian

import (
	"strings"
	"testing"
)

// 「枚举到了进程,却一个的参数都读不出来」必须报错,不能返回空列表。
// 空列表在调用方眼里就是「确认没有 Core,可以自愈」,而这种系统状态下
// 系统里完全可能正跑着一个 Core —— 放行就是第二个 Core。
//
// 这条下限此前零覆盖:把它改成 `if false && readable == 0` 整套测试依旧全绿。
func TestDecideCoreScanRefusesWhenNothingWasReadable(t *testing.T) {
	cores, err := decideCoreScan(874, 0, nil)
	if err == nil {
		t.Fatal("readable==0 必须报错——不能把「读不出任何进程参数」当成「没有 Core」")
	}
	if cores != nil {
		t.Errorf("报错时不得返回结果,实际 = %+v", cores)
	}
	if !strings.Contains(err.Error(), "874") {
		t.Errorf("错误应带上枚举总数便于排查,实际 = %v", err)
	}
}

// 一个进程都没枚举到同样落在这条下限里(readable 必然为 0)。
func TestDecideCoreScanRefusesWhenNothingWasEnumerated(t *testing.T) {
	if _, err := decideCoreScan(0, 0, nil); err == nil {
		t.Fatal("枚举不到任何进程时不能报告「没有 Core」")
	}
}

// 真查过、确实没有 Core:这才是允许自愈的唯一形态。
func TestDecideCoreScanReportsNoCoresWhenTrulyAbsent(t *testing.T) {
	cores, err := decideCoreScan(874, 812, nil)
	if err != nil {
		t.Fatalf("如实查过就该给出答案,实际 = %v", err)
	}
	if len(cores) != 0 {
		t.Fatalf("cores = %+v, want 空", cores)
	}
}

func TestDecideCoreScanPassesFoundCoresThrough(t *testing.T) {
	found := []Process{{PID: 4242, Executable: "/usr/local/bin/bx", UID: 0}}
	cores, err := decideCoreScan(874, 812, found)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cores) != 1 || cores[0].PID != 4242 {
		t.Fatalf("cores = %+v, want 原样透传", cores)
	}
}

// 僵尸不是在跑的 Core。这条判据支撑着崩溃重启路径:旧 Core 刚死、可能还以
// 僵尸形态挂在进程表里,若把它算成「有 Core 在跑」,每次崩溃都会变成永久失联。
func TestIsZombieProcessOnlyMatchesSZOMB(t *testing.T) {
	if !isZombieProcess(5) {
		t.Error("SZOMB(5) 必须判为僵尸")
	}
	for _, stat := range []int8{1, 2, 3, 4} { // SIDL/SRUN/SSLEEP/SSTOP
		if isZombieProcess(stat) {
			t.Errorf("p_stat=%d 是活着的进程,不得判为僵尸——漏认一个活 Core 是灾难", stat)
		}
	}
}

// —— 部分读不出来同样不能说「没有」(2026-09-03)——
//
// 真机:一台 macOS 26.5.2 上 `bx status` 明明从 Core 的控制 socket 拿到了节点、
// 隧道、连接、流量,同一行的调谐环却报「扫到 0 个 Core 进程」。此前只有
// readable==0 这一档,于是「900 个里读到 500 个、没找到」会产出一个**自信的 0**
// —— 而 Core 完全可能就在没读到的那 400 个里。
//
// **它喂的是准入判据**:扫到 0 ⇒ 允许启动一个新 Core ⇒ 两个 Core 抢同一个控制
// socket、争 split-default 路由,先退出的那个用旧快照还原掀掉另一个的劫持 ——
// status 显绿而流量明文直连。CLAUDE.md 原话:漏认的代价是灾难。

func TestCoreScanRefusesToSayNoneWhenHalfTheProcessesWereUnreadable(t *testing.T) {
	_, err := decideCoreScan(900, 400, nil)
	if err == nil {
		t.Fatal("只读到 400/900 却报告「没有 Core」—— 「问不出来」被说成了「确定没有」")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "900") {
		t.Errorf("错误里没带上视野有多窄,排查时无从判断:%v", err)
	}
}

// 视野完整时照常给答案 —— 否则每一次正常扫描都变成错误,而 fail-closed 的
// 调用方会**永远拒绝启动 Core**,那是把一个灾难换成另一个。
func TestCoreScanStillAnswersWhenTheViewIsComplete(t *testing.T) {
	got, err := decideCoreScan(892, 891, nil)
	if err != nil {
		t.Fatalf("891/892(项目所有者机器上的实测比例)被判成视野不足:%v", err)
	}
	if len(got) != 0 {
		t.Errorf("凭空多出 %d 个 Core", len(got))
	}
}

// **找到了 Core 就不因视野不足报错。**
// 调用方本来就 fail-closed 拒绝;把它换成一句「扫描出错」只会丢掉
// 「有 Core 在跑(PID N)」这个更可操作的信息。
func TestCoreScanKeepsTheActionableAnswerWhenItFoundOne(t *testing.T) {
	found := []Process{{PID: 4688}}
	got, err := decideCoreScan(900, 400, found)
	if err != nil {
		t.Fatalf("找到了 Core 却报成扫描错误,丢掉了 PID 这个线索:%v", err)
	}
	if len(got) != 1 || got[0].PID != 4688 {
		t.Errorf("找到的 Core 没被带出来:%+v", got)
	}
}

// readable==0 那一档原样保留(它比比例判据更硬:一个都读不出来是系统反常)。
func TestCoreScanStillRefusesWhenNothingWasReadable(t *testing.T) {
	if _, err := decideCoreScan(900, 0, nil); err == nil {
		t.Fatal("一个进程的参数都读不出来却报告「没有 Core」")
	}
}
