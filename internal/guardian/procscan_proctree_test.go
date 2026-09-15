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

	enumerated, kernel, readable, cores := scanLinuxProcTree(root)
	if enumerated != 4 || kernel != 0 || readable != 4 {
		t.Fatalf("enumerated=%d kernel=%d readable=%d want 4/0/4", enumerated, kernel, readable)
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

	_, _, _, cores := scanLinuxProcTree(root)
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

	enumerated, kernel, readable, _ := scanLinuxProcTree(root)
	if enumerated != 2 || readable != 1 {
		t.Fatalf("enumerated=%d readable=%d want 2/1", enumerated, readable)
	}
	// 2026-09-15 补:它**同时**要被记成内核线程 —— 不算 readable(这条原样成立)
	// 之外,还要从「视野够不够宽」那道门的分母里去掉,否则那道门在 Linux 上恒假。
	if kernel != 1 {
		t.Fatalf("kernel=%d want 1 —— 内核线程没被认出来,它会被当成一处盲区", kernel)
	}
}

func TestScanLinuxProcTreeRecognizesDeletedExecutable(t *testing.T) {
	// 升级把二进制换掉之后,旧 Core 的 /proc/<pid>/exe 读出来是
	// "/path/bx (deleted)"——按 af81632 的教训,漏认它就是双 Core;
	// argv[0] 这条兜底也在,但 exe 这半不许自己先漏。
	root := t.TempDir()
	writeProcEntry(t, root, 4242, "bx", 'S', []string{"weird", "run"}, 0, "/usr/local/bin/bx (deleted)")

	_, _, _, cores := scanLinuxProcTree(root)
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

	enumerated, _, _, _ := scanLinuxProcTree(root)
	if enumerated != 1 {
		t.Fatalf("enumerated=%d want 1(sys 不是进程)", enumerated)
	}
}

// —— 内核线程不是盲区,不许进「视野够不够宽」那道门的分母(2026-09-15,CI 抓到)——
//
// `coreScanReadableFloor` 要求 readable/enumerated ≥ 0.5,而那个 0.5 是按
// **darwin** 定的(所有者的 Mac 上实测 891/892 = 99.9%)。Linux 上它结构性地
// 不成立:内核线程(kworker/ksoftirqd/rcu_* …)没有用户态 argv,按这份实现算
// 「没读成」,而它们常常占进程表的四分之三 —— CI 的 ubuntu runner 上实测
// **169 个进程里 126 个是内核线程**,readable=43 ⇒ 25% ⇒ 那道门在一台完全
// 正常的机器上恒假,于是 netns 集成台上五个测试一直红着。
//
// **判据上的要点**:内核线程给得出结论 ——「它不可能是 Core」,因为 Core 按构造
// 是一个 argv[1]=="run" 的**用户态**进程。它不是一处盲区,所以不该进分母。

func TestKernelThreadsAreNotCountedAsBlindSpots(t *testing.T) {
	root := t.TempDir()
	writeProcEntry(t, root, 1, "systemd", 'S', []string{"/sbin/init"}, 0, "/sbin/init")
	writeProcEntry(t, root, 4242, "bx", 'S', []string{"/usr/local/bin/bx", "run"}, 0, "/usr/local/bin/bx")
	// 十个内核线程:没有 argv,也没有 exe(内核线程没有用户态映像)。
	for pid := 100; pid < 110; pid++ {
		writeProcEntry(t, root, pid, "kworker/0:1", 'S', nil, 0, "")
	}

	enumerated, kernel, readable, cores := scanLinuxProcTree(root)
	if enumerated != 12 || kernel != 10 || readable != 2 {
		t.Fatalf("enumerated=%d kernel=%d readable=%d,want 12/10/2", enumerated, kernel, readable)
	}
	// **决定性的那一句**:把内核线程从分母里去掉之后,这次扫描必须被接受。
	// 去掉之前是 2/12 = 17%,那道门会拒绝一台完全正常的机器。
	if _, err := decideCoreScan(enumerated-kernel, readable, cores); err != nil {
		t.Errorf("一台只是内核线程多的正常机器被拒了:%v", err)
	}
}

// **空 argv 而 exe 读得出来的,是真的没读成 —— 那是盲区,照旧进分母。**
// 少了这一半,「凡是读不出 argv 一律不算数」就会把一台 /proc 真的被遮住的机器
// 说成视野干净,而那正是这套扫描最忌讳的假「全清」(hidepid 那条纪律)。
func TestAProcessWithAnExecutableButNoArgvIsStillABlindSpot(t *testing.T) {
	root := t.TempDir()
	writeProcEntry(t, root, 1, "systemd", 'S', []string{"/sbin/init"}, 0, "/sbin/init")
	// 用户态进程(有 exe)却读不出 argv:我们对它下不了「不是 Core」的结论。
	writeProcEntry(t, root, 900, "hidden", 'S', nil, 0, "/usr/bin/hidden")

	enumerated, kernel, readable, _ := scanLinuxProcTree(root)
	if enumerated != 2 || kernel != 0 || readable != 1 {
		t.Fatalf("enumerated=%d kernel=%d readable=%d,want 2/0/1 —— 有 exe 的空 argv 不是内核线程",
			enumerated, kernel, readable)
	}
}
