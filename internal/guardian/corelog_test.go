package guardian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Core 的输出此前继承 Guardian 的 stdout/stderr(osProcessOperations.Start 里那
// 两句 cmd.Stdout = os.Stdout),两个进程于是挤进同一个文件。真机 2026-09-01:
// /var/log/bx-guard.err.log 100MB,其中 99% 是 Core 转发的传输子进程输出,而
// Guardian 自己那几千行审计线索 —— 尤其 guardian_core_scan(「我允许了一个 Core
// 启动,因为我认为没有别的 Core 在跑」)—— 被埋在底下。

// 日志超过上限时先轮转再写:旧的留一份,新的从头开始。
func TestOpenCoreLogRotatesWhenOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bx.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := openCoreLog(path, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("轮转后新日志不是空的: size=%v err=%v", info, err)
	}
	archived, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("旧日志没有留档: %v", err)
	}
	if len(archived) != 100 {
		t.Errorf("留档内容被改动了: %d 字节", len(archived))
	}
}

// **没超上限就绝不轮转,而且必须续写而不是截断。**
// 截断掉的正是排查一次故障要看的上文 —— 而 Core 每次崩溃重启都会走这条路。
func TestOpenCoreLogAppendsWhenUnderTheLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bx.log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := openCoreLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("later\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "earlier\nlater\n" {
		t.Errorf("没有续写:%q", got)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Error("没超上限却轮转了")
	}
}

// 日志里有服务器 IP 与 116 条 bypass 网段 —— 这正是 2026-08-05 把 guard 日志
// 从 launchd 默认的 0644 收紧到 0600 的理由。新开的这一份不许比它松。
func TestOpenCoreLogCreatesTheFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bx.log")
	f, err := openCoreLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("日志权限是 %o,应当是 600", mode)
	}
}

// 只留一代归档。留更多是拿磁盘换一段没人会读的历史 ——
// 而这台机器上真正发生过的事故是**磁盘被日志吃掉 100MB**。
func TestOpenCoreLogKeepsOnlyOneGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bx.log")
	for i, content := range []string{"first", "second", "third"} {
		if err := os.WriteFile(path, []byte(strings.Repeat(content, 20)), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := openCoreLog(path, 10)
		if err != nil {
			t.Fatalf("第 %d 轮: %v", i, err)
		}
		f.Close()
	}
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Error("留了第二代归档 —— 上限应当只有一代")
	}
	archived, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(archived), "third") {
		t.Errorf("归档不是最近那一代:%.10q", archived)
	}
}

// **路径为空 = 本平台没有给 Core 单开日志这回事**,不是错误。
// 它与「该开却开不出来」是两件事:后者要退回继承并留一行日志,前者本就无事可做。
func TestOpenCoreLogRejectsAnEmptyPath(t *testing.T) {
	if _, err := openCoreLog("", 1<<20); err == nil {
		t.Error("空路径应当报错,让调用方退回继承父进程的 stdout/stderr")
	}
}

// **接线守卫**:Start 真的把子进程的 stdout 与 stderr 都接到了 Core 日志上。
//
// 判据是行为不是文本 —— 起一个真进程,看它的输出落没落进那个文件。上面几条
// 测的是 openCoreLog 这个判据,而这个仓库反复栽的正是「判据有测试、接线没有」:
// 把这两句改回 cmd.Stdout = os.Stdout 之后,那几条照样全绿。
func TestCoreSpawnSendsBothStreamsToTheCoreLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bx.log")
	ops := osProcessOperations{logPath: path}

	started, err := ops.Start("/bin/sh", []string{"-c", "echo to-stdout; echo to-stderr >&2"}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Wait(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Core 日志没被建出来 —— 输出多半还继承着 Guardian 的: %v", err)
	}
	for _, want := range []string{"to-stdout", "to-stderr"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("%q 没有落进 Core 日志:%q", want, got)
		}
	}
}

// 日志开不出来时**绝不许挡住 Core 启动** —— 保护比日志重要。
func TestCoreSpawnStillStartsWhenTheLogCannotBeOpened(t *testing.T) {
	// 用一个「父目录是文件」的路径,保证 OpenFile 必失败。
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ops := osProcessOperations{logPath: filepath.Join(blocker, "bx.log")}

	started, err := ops.Start("/bin/sh", []string{"-c", "exit 0"}, os.Environ())
	if err != nil {
		t.Fatalf("日志开不出来时 Core 没能启动:%v", err)
	}
	if err := started.Wait(); err != nil {
		t.Fatal(err)
	}
}
