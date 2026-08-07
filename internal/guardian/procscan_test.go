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
