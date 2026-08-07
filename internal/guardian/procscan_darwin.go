//go:build darwin

package guardian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
)

// parseProcArgs 解析 macOS 的 kern.procargs2 布局:
//
//	[4 字节 argc][可执行路径 NUL][若干填充 NUL][argv[0] NUL][argv[1] NUL]…
//
// 畸形输入一律报错,绝不返回「看起来合理的空结果」——那会让调用方以为这个进程
// 不像 Core,进而漏认一个真的 Core。
func parseProcArgs(raw []byte) (string, []string, error) {
	if len(raw) < 4 {
		return "", nil, errors.New("procargs2 too short")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return "", nil, errors.New("procargs2 has no executable path")
	}
	executable := string(rest[:end])
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 { // 路径与 argv[0] 之间的填充
		rest = rest[1:]
	}
	var argv []string
	for i := 0; i < argc && len(rest) > 0; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			argv = append(argv, string(rest))
			break
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, argv, nil
}

// looksLikeCore 判定一个进程是不是 bx 的 Core。
//
// **刻意不依赖具体可执行路径。** 更新之后旧版 Core 跑在 runtime/<旧版本>/bx 下,
// 用「路径 == 当前 Core 路径」做判据会漏认它,于是起第二个 Core——正是 af81632
// 被回退的那个双 Core 风险。
//
// 也刻意偏向过度匹配:多认一个的后果是拒绝启动(安全),漏认一个是灾难。用户手工
// `sudo bx run` 会被认出来,而那本来就是一个 Core,认出来是对的。
func looksLikeCore(executable string, argv []string, uid int) bool {
	if uid != 0 {
		return false
	}
	if len(argv) < 2 || argv[1] != "run" {
		return false
	}
	if filepath.Base(executable) == "bx" {
		return true
	}
	return filepath.Base(argv[0]) == "bx"
}
