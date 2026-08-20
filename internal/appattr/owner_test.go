package appattr

import "testing"

func aliveSet(pids ...int32) func(int32) bool {
	live := map[int32]bool{}
	for _, p := range pids {
		live[p] = true
	}
	return func(p int32) bool { return live[p] }
}

// so_e_pid 是「替谁干活」——把 nsurlsessiond / trustd 这类代劳者的流量归回真正
// 发起的那个应用。它优先。
func TestChooseOwnerPrefersLiveDelegatingProcess(t *testing.T) {
	pid, ok := ChooseOwner(PCB{LastPID: 485, EPID: 7961}, aliveSet(485, 7961))
	if !ok || pid != 7961 {
		t.Fatalf("ChooseOwner = %d,%v; want 7961,true", pid, ok)
	}
}

// **spike 真机撞到过这一条**:trustd 的 so_e_pid 指向 7961,而 7961 早就退出了。
// 不查活性就会把流量记在一个不存在的应用上 —— 而那种错误在界面上完全看不出来。
func TestChooseOwnerFallsBackWhenDelegatorIsDead(t *testing.T) {
	pid, ok := ChooseOwner(PCB{LastPID: 485, EPID: 7961}, aliveSet(485))
	if !ok || pid != 485 {
		t.Fatalf("ChooseOwner = %d,%v; want 485,true(委托方已退出应回落 so_last_pid)", pid, ok)
	}
}

func TestChooseOwnerUsesLastPIDWhenNoDelegation(t *testing.T) {
	pid, ok := ChooseOwner(PCB{LastPID: 607, EPID: 0}, aliveSet(607))
	if !ok || pid != 607 {
		t.Fatalf("ChooseOwner = %d,%v; want 607,true", pid, ok)
	}
}

// 「问不出来」不许被压成一个具体答案(与 Tristate、WhoOwnsTheRoute 四态同源)。
func TestChooseOwnerReportsUnknownRatherThanGuessing(t *testing.T) {
	for _, p := range []PCB{
		{LastPID: 0, EPID: 0},
		{LastPID: -1, EPID: -1},
		{LastPID: 999, EPID: 0}, // last_pid 也已退出
	} {
		if pid, ok := ChooseOwner(p, aliveSet()); ok {
			t.Fatalf("%+v 应判 unknown,却给出了 pid=%d", p, pid)
		}
	}
}

func TestDisplayNameUsesBundleNameThenBasename(t *testing.T) {
	cases := map[string]string{
		"/Applications/TencentMeeting.app/Contents/MacOS/TencentMeeting":                                         "TencentMeeting",
		"/Applications/Claude.app/Contents/Frameworks/Claude Helper.app/Contents/MacOS/Claude Helper":            "Claude",
		"/System/Library/PrivateFrameworks/IDS.framework/identityservicesd.app/Contents/MacOS/identityservicesd": "identityservicesd",
		"/usr/libexec/trustd":       "trustd",
		"/opt/homebrew/bin/limactl": "limactl",
		"":                          "",
	}
	for in, want := range cases {
		if got := DisplayName(in); got != want {
			t.Fatalf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
