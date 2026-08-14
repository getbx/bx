//go:build darwin

package cli

import (
	"context"
	"strings"
	"testing"
)

// **bootout 之后必须等标签真的消失,再 bootstrap。**
//
// 真机(2026-08-14,连着两次安装都复现):
//
//	launchctl asuser 501 launchctl bootstrap gui/501 …:
//	exit status 5: Bootstrap failed: 5: Input/output error
//
// 而菜单当时**正在运行**。`launchctl bootstrap` 对一个已加载的 job 返回的正是
// EIO(5) —— launchd 这个错误码出了名的没信息量。安装路径走 forceReload:
// bootout 之后**立刻** bootstrap,而 bootout 返回时 launchd 还没卸完。
//
// **与 2026-08-13 修的 Guardian 那个竞态同源**:那次也是 bootout 不等,
// 紧接着的命令撞上一个正在拆除中的服务。修 Guardian 时等了,菜单这条没等。
//
// (代码里原有的注释把这个 EIO 归因到「root 没有目标用户的 session token」——
// 那是 2026-08-05 那次的原因,而现在已经用了 asuser。**同一个 errno 有两个来源,
// 而上一次修复只针对了其中一个。**)
func TestMenuWaitsForBootoutBeforeBootstrapping(t *testing.T) {
	control := &racyMenuLaunchdControl{
		// 前两次问还「在」(launchd 正在拆),第三次才消失。
		loadedScript: []bool{true, true, true, false, true},
	}
	deps := menuBootstrapDeps{
		homeDir:    func(int) (string, error) { return t.TempDir(), nil },
		fileExists: func(string) (bool, error) { return true, nil },
		remove:     func(string) error { return nil },
		control:    control,
	}
	if err := ensureMacOSMenuRunningWithDeps(context.Background(), 501, true, deps); err != nil {
		t.Fatalf("重载失败:%v", err)
	}
	bootoutAt, bootstrapAt := -1, -1
	for i, call := range control.calls {
		if strings.HasPrefix(call, "bootout ") && bootoutAt < 0 {
			bootoutAt = i
		}
		if strings.Contains(call, "bootstrap") && bootstrapAt < 0 {
			bootstrapAt = i
		}
	}
	if bootoutAt < 0 || bootstrapAt < 0 {
		t.Fatalf("没有 bootout+bootstrap 序列:%v", control.calls)
	}
	// **判据是「问过内核」,不是「睡了一会儿」** —— 睡固定时长既可能不够,
	// 也在正常情况下白等。
	if control.loadedQueriesBetween(bootoutAt, bootstrapAt) == 0 {
		t.Fatalf("bootout 与 bootstrap 之间一次都没问「卸完没有」:%v", control.calls)
	}
}

// **等不到也不许失败。** 卸不干净时仍要继续尝试 bootstrap:那一步失败会带上
// 可操作的提示,而在这里直接放弃只会让用户连提示都拿不到。
func TestMenuReloadProceedsEvenIfTheLabelNeverUnloads(t *testing.T) {
	control := &racyMenuLaunchdControl{alwaysLoaded: true}
	deps := menuBootstrapDeps{
		homeDir:    func(int) (string, error) { return t.TempDir(), nil },
		fileExists: func(string) (bool, error) { return true, nil },
		remove:     func(string) error { return nil },
		control:    control,
	}
	if err := ensureMacOSMenuRunningWithDeps(context.Background(), 501, true, deps); err != nil {
		t.Fatalf("卸不干净就直接失败了:%v", err)
	}
	joined := strings.Join(control.calls, " | ")
	if !strings.Contains(joined, "bootstrap") {
		t.Fatalf("没有尝试 bootstrap:%s", joined)
	}
}

type racyMenuLaunchdControl struct {
	loadedScript []bool
	alwaysLoaded bool
	loadedAt     []int
	queries      int
	calls        []string
}

func (f *racyMenuLaunchdControl) Loaded(_ context.Context, label string) (bool, error) {
	f.loadedAt = append(f.loadedAt, len(f.calls))
	defer func() { f.queries++ }()
	// legacy 标签恒不在册 —— 真机上它早就没了,而让它「一直在」会把测试
	// 拖去测另一件事(legacy 清理),而不是本条要测的竞态。
	if strings.Contains(label, legacyMenuLaunchdLabel) {
		return false, nil
	}
	if f.alwaysLoaded {
		return true, nil
	}
	if f.queries < len(f.loadedScript) {
		return f.loadedScript[f.queries], nil
	}
	return true, nil
}

func (f *racyMenuLaunchdControl) Run(_ context.Context, args ...string) error {
	f.calls = append(f.calls, strings.Join(args, " "))
	return nil
}

func (f *racyMenuLaunchdControl) loadedQueriesBetween(from, to int) int {
	n := 0
	for _, at := range f.loadedAt {
		if at > from && at <= to {
			n++
		}
	}
	return n
}
