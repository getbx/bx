//go:build darwin

package guardian

import (
	"encoding/binary"
	"testing"
)

// buildProcArgs 造一份 kern.procargs2 的字节布局:
// [4 字节 argc][可执行路径 NUL][若干填充 NUL][argv[0] NUL][argv[1] NUL]…
func buildProcArgs(executable string, argv []string, padding int) []byte {
	raw := make([]byte, 4)
	binary.NativeEndian.PutUint32(raw, uint32(len(argv)))
	raw = append(raw, []byte(executable)...)
	raw = append(raw, 0)
	for i := 0; i < padding; i++ {
		raw = append(raw, 0)
	}
	for _, arg := range argv {
		raw = append(raw, []byte(arg)...)
		raw = append(raw, 0)
	}
	return raw
}

func TestParseProcArgsReadsExecutableAndArgv(t *testing.T) {
	raw := buildProcArgs("/Library/Application Support/bx/runtime/dev/bx",
		[]string{"/usr/local/bin/bx", "run", "-c", "/etc/bx/config.yaml"}, 3)

	executable, argv, err := parseProcArgs(raw)
	if err != nil {
		t.Fatalf("parseProcArgs 失败: %v", err)
	}
	if executable != "/Library/Application Support/bx/runtime/dev/bx" {
		t.Errorf("executable = %q", executable)
	}
	if len(argv) != 4 || argv[1] != "run" || argv[3] != "/etc/bx/config.yaml" {
		t.Errorf("argv = %q, want 完整四项", argv)
	}
}

// 截断/畸形的缓冲区必须报错,不能返回一个看起来合理的空结果——
// 那会让上层以为「这个进程不像 Core」,进而漏认。
func TestParseProcArgsRejectsMalformed(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
	}{
		{"太短", []byte{1, 2}},
		{"只有 argc 没有路径", []byte{1, 0, 0, 0}},
		{"路径没有 NUL 结尾", append([]byte{1, 0, 0, 0}, []byte("/usr/local/bin/bx")...)},
		{"argc>0 但缓冲区在路径后耗尽", func() []byte {
			raw := make([]byte, 4)
			binary.NativeEndian.PutUint32(raw, 2) // argc=2
			raw = append(raw, []byte("/usr/local/bin/bx")...)
			raw = append(raw, 0) // path NUL，然后没有更多数据
			return raw
		}()},
		{"argv 项截断(无 NUL 结尾)", func() []byte {
			raw := make([]byte, 4)
			binary.NativeEndian.PutUint32(raw, 2)
			raw = append(raw, []byte("/usr/local/bin/bx")...)
			raw = append(raw, 0)
			raw = append(raw, []byte("bx")...)
			raw = append(raw, 0)
			raw = append(raw, "ru"...) // 截断的 "run"，没有 NUL 结尾
			return raw
		}()},
		{"argc=0", func() []byte {
			raw := make([]byte, 4)
			binary.NativeEndian.PutUint32(raw, 0)
			raw = append(raw, []byte("/usr/local/bin/bx")...)
			raw = append(raw, 0)
			return raw
		}()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := parseProcArgs(tt.raw); err == nil {
				t.Error("畸形输入必须报错")
			}
		})
	}
}

// 判据的核心:认得出 Core,且**不靠具体路径**认。
func TestLooksLikeCoreIgnoresExecutablePath(t *testing.T) {
	// 更新之后旧版 Core 跑在另一个路径下。用「路径 == 当前 Core 路径」做判据
	// 会漏认它,于是起第二个 Core —— 正是 af81632 被回退的那个风险。
	for _, executable := range []string{
		"/Library/Application Support/bx/runtime/dev/bx",
		"/Library/Application Support/bx/runtime/v0.2.7/bx",
		"/usr/local/bin/bx",
	} {
		argv := []string{executable, "run", "-c", "/etc/bx/config.yaml"}
		if !looksLikeCore(executable, argv, 0) {
			t.Errorf("%s 必须被认成 Core —— 判据不能依赖具体路径", executable)
		}
	}
}

func TestLooksLikeCoreRejectsNonCore(t *testing.T) {
	for _, tt := range []struct {
		name       string
		executable string
		argv       []string
		uid        int
	}{
		{"非 root", "/usr/local/bin/bx", []string{"bx", "run", "-c", "x"}, 501},
		{"不是 run 子命令", "/usr/local/bin/bx", []string{"bx", "status", "--json"}, 0},
		{"没有子命令", "/usr/local/bin/bx", []string{"bx"}, 0},
		{"别的程序", "/usr/bin/ssh", []string{"ssh", "run"}, 0},
		{"argv 为空", "/usr/local/bin/bx", nil, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if looksLikeCore(tt.executable, tt.argv, tt.uid) {
				t.Errorf("不该认成 Core:%s %q uid=%d", tt.executable, tt.argv, tt.uid)
			}
		})
	}
}

// 经软链或改名调用时,argv[0] 仍然叫 bx —— 偏向过度匹配,认出来。
func TestLooksLikeCoreAcceptsBxViaArgv0(t *testing.T) {
	if !looksLikeCore("/opt/weird/path/bx-renamed", []string{"bx", "run", "-c", "x"}, 0) {
		t.Error("argv[0] 是 bx 就该认 —— 过度匹配的后果是 fail-closed,方向安全")
	}
}

// 真实扫描必须能在本机跑通且不 panic。它至少应该看到本测试进程之外的东西,
// 但不断言具体内容——那取决于机器状态。这条只证明 syscall 路径是活的。
func TestScanRunningCoresDoesNotFailOnRealSystem(t *testing.T) {
	if _, err := scanRunningCores(); err != nil {
		t.Fatalf("在真实系统上扫描不应失败: %v", err)
	}
}
