package guardian

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// 这些测试无 build tag:/proc 树的布局是 Linux 的,但读它只是文件 I/O,
// 用 fixture 目录在任何 OS 上都测得动——linux-only 的代码在开发机(darwin)
// 上跑不了测试,而「测试输入让缺陷不可见」正是这个仓库反复栽的形状,
// 能在三条 CI 腿上都跑的判据就不要塞进单条腿。

func TestParseProcStatStateReadsStateAfterLastParen(t *testing.T) {
	// comm 里可以有空格和右括号(进程能把自己的名字改成任何东西),
	// 判据必须锚在**最后一个** ')' 上,否则一个叫 "a) Z (b" 的进程能把
	// 自己伪装成僵尸、或把僵尸伪装成活着。
	cases := []struct {
		name string
		stat string
		want byte
	}{
		{"normal", "123 (bx) S 1 123 123 0 -1", 'S'},
		{"zombie", "999 (bx) Z 1 999 999 0 -1", 'Z'},
		{"comm with spaces", "42 (tencent meeting) R 1 42 42", 'R'},
		{"comm with paren", "77 (evil) Z (name) S 1 77", 'S'},
	}
	for _, c := range cases {
		got, err := parseProcStatState([]byte(c.stat))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: state=%c want %c", c.name, got, c.want)
		}
	}
}

func TestParseProcStatStateRejectsMalformed(t *testing.T) {
	// 畸形输入必须报错,不许返回「看起来合理」的状态——与 parseProcArgs 同一条:
	// 安静的空结果会让调用方以为这个进程不像 Core,漏认一个真的 Core。
	for _, bad := range []string{"", "123", "123 (bx)", "123 (bx) "} {
		if _, err := parseProcStatState([]byte(bad)); err == nil {
			t.Fatalf("畸形 stat %q 必须报错", bad)
		}
	}
}

func TestParseProcCmdlineSplitsOnNUL(t *testing.T) {
	argv := parseProcCmdline([]byte("/usr/local/bin/bx\x00run\x00"))
	if len(argv) != 2 || argv[0] != "/usr/local/bin/bx" || argv[1] != "run" {
		t.Fatalf("argv=%q", argv)
	}
	// 内核线程的 cmdline 是空的:必须返回空切片而不是 [""],
	// 否则 looksLikeCore 会拿一个空串去 filepath.Base。
	if argv := parseProcCmdline(nil); len(argv) != 0 {
		t.Fatalf("空 cmdline 应产出空 argv,got %q", argv)
	}
	if argv := parseProcCmdline([]byte{0}); len(argv) != 0 {
		t.Fatalf("只有 NUL 的 cmdline 应产出空 argv,got %q", argv)
	}
}

func TestParseProcStatusUIDTakesEffectiveUID(t *testing.T) {
	// Uid: 一行是 real/effective/saved/fs 四列;取**effective**——与 darwin 侧
	// Eproc.Ucred.Uid(effective)对齐,两个平台对「这是不是 root 的进程」
	// 必须给同一个答案。
	status := "Name:\tbx\nUid:\t501\t0\t0\t0\nGid:\t20\t20\t20\t20\n"
	uid, err := parseProcStatusUID([]byte(status))
	if err != nil {
		t.Fatal(err)
	}
	if uid != 0 {
		t.Fatalf("uid=%d want 0(effective 列)", uid)
	}
	if _, err := parseProcStatusUID([]byte("Name:\tbx\n")); err == nil {
		t.Fatal("没有 Uid 行必须报错")
	}
}

// writeProcEntry 在 fixture 树里造一个进程:stat/cmdline/status 三个文件 +
// 可选的 exe 符号链接。
func writeProcEntry(t *testing.T, root string, pid int, comm string, state byte, argv []string, uid int, exe string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (" + comm + ") " + string(state) + " 1 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	var cmdline []byte
	for _, a := range argv {
		cmdline = append(cmdline, a...)
		cmdline = append(cmdline, 0)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmdline, 0o644); err != nil {
		t.Fatal(err)
	}
	status := "Name:\t" + comm + "\nUid:\t" + strconv.Itoa(uid) + "\t" + strconv.Itoa(uid) + "\t0\t0\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if exe != "" {
		if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanLinuxProcTreeFindsRootCore(t *testing.T) {
	root := t.TempDir()
	writeProcEntry(t, root, 1, "systemd", 'S', []string{"/sbin/init"}, 0, "/sbin/init")
	writeProcEntry(t, root, 4242, "bx", 'S', []string{"/usr/local/bin/bx", "run"}, 0, "/usr/local/bin/bx")
	writeProcEntry(t, root, 4300, "bx", 'S', []string{"bx", "status"}, 0, "/usr/local/bin/bx") // 不是 run
	writeProcEntry(t, root, 4400, "bx", 'S', []string{"/usr/local/bin/bx", "run"}, 501, "")    // 非 root

	enumerated, readable, cores := scanLinuxProcTree(root)
	if enumerated != 4 || readable != 4 {
		t.Fatalf("enumerated=%d readable=%d want 4/4", enumerated, readable)
	}
	if len(cores) != 1 || cores[0].PID != 4242 || cores[0].UID != 0 {
		t.Fatalf("cores=%+v want 只有 4242", cores)
	}
}

func TestScanLinuxProcTreeSkipsZombies(t *testing.T) {
	// 崩溃重启路径靠这条:刚死的旧 Core 以僵尸形态挂在进程表里时,把它算成
	// 「有 Core 在跑」会让每一次崩溃都变成永久失联(与 darwin 的 SZOMB 同一条)。
	root := t.TempDir()
	writeProcEntry(t, root, 1, "systemd", 'S', []string{"/sbin/init"}, 0, "/sbin/init")
	writeProcEntry(t, root, 4242, "bx", 'Z', []string{"/usr/local/bin/bx", "run"}, 0, "")

	_, _, cores := scanLinuxProcTree(root)
	if len(cores) != 0 {
		t.Fatalf("僵尸被算成在跑的 Core: %+v", cores)
	}
}

func TestScanLinuxProcTreeCountsKernelThreadsAsUnreadable(t *testing.T) {
	// 内核线程 cmdline 为空——它连「不是 Core」这个结论都给不出(没有 argv 可判),
	// 只能算「没读成」;把它算进 readable 会稀释 decideCoreScan 那条
	// 「一个都没读成必须报错」的下限。
	root := t.TempDir()
	writeProcEntry(t, root, 2, "kthreadd", 'S', nil, 0, "")
	writeProcEntry(t, root, 1, "systemd", 'S', []string{"/sbin/init"}, 0, "/sbin/init")

	enumerated, readable, _ := scanLinuxProcTree(root)
	if enumerated != 2 || readable != 1 {
		t.Fatalf("enumerated=%d readable=%d want 2/1", enumerated, readable)
	}
}

func TestScanLinuxProcTreeRecognizesDeletedExecutable(t *testing.T) {
	// 升级把二进制换掉之后,旧 Core 的 /proc/<pid>/exe 读出来是
	// "/path/bx (deleted)"——按 af81632 的教训,漏认它就是双 Core;
	// argv[0] 这条兜底也在,但 exe 这半不许自己先漏。
	root := t.TempDir()
	writeProcEntry(t, root, 4242, "bx", 'S', []string{"weird", "run"}, 0, "/usr/local/bin/bx (deleted)")

	_, _, cores := scanLinuxProcTree(root)
	if len(cores) != 1 || cores[0].PID != 4242 {
		t.Fatalf("被删可执行文件的 Core 没被认出: %+v", cores)
	}
}

func TestScanLinuxProcTreeIgnoresNonNumericEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeProcEntry(t, root, 1, "systemd", 'S', []string{"/sbin/init"}, 0, "/sbin/init")

	enumerated, _, _ := scanLinuxProcTree(root)
	if enumerated != 1 {
		t.Fatalf("enumerated=%d want 1(sys 不是进程)", enumerated)
	}
}
