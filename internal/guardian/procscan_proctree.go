package guardian

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 本文件是 Linux /proc 树扫描的**纯 I/O 半**:布局是 Linux 的,但读它只是
// 文件操作,故无 build tag——fixture 目录让它在三条 CI 腿上都有测试,
// linux-only 的薄壳(procscan_linux.go)只负责 root 门槛、真实的 /proc 根、
// decideCoreScan 下限与普查日志。
//
// 与 darwin 侧(procscan_darwin.go)同一套纪律:单个进程读不出就跳过,
// 畸形输入报错而不是返回「看起来合理的空结果」;判 Core 用共用的 looksLikeCore。

// parseProcStatState 从 /proc/<pid>/stat 提取进程状态字符(第三字段)。
//
// comm 字段(括号里)可以含空格和右括号——进程能把自己的名字改成任何东西,
// 判据必须锚在**最后一个** ')' 上;按空格或第一个 ')' 切,一个恶意命名的进程
// 就能把自己伪装成僵尸(被扫描漏掉)或把僵尸伪装成活着(卡死崩溃重启)。
func parseProcStatState(raw []byte) (byte, error) {
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 || end+2 >= len(raw) {
		return 0, errors.New("proc stat: no state field after comm")
	}
	state := raw[end+2]
	if state == ' ' {
		return 0, errors.New("proc stat: empty state field")
	}
	return state, nil
}

// parseProcCmdline 按 NUL 切 /proc/<pid>/cmdline。内核线程的 cmdline 为空,
// 返回空切片而不是 [""]——空串进 filepath.Base 会产出 "."。
func parseProcCmdline(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	argv := make([]string, 0, len(parts))
	for _, p := range parts {
		argv = append(argv, string(p))
	}
	return argv
}

// parseProcStatusUID 从 /proc/<pid>/status 的 Uid 行取 **effective** UID
// (四列 real/effective/saved/fs 的第二列)——与 darwin 侧 Eproc.Ucred.Uid
// (effective)对齐:两个平台对「这是不是 root 的进程」必须给同一个答案。
func parseProcStatusUID(raw []byte) (int, error) {
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line[len("Uid:"):])
		if len(fields) < 2 {
			return 0, errors.New("proc status: Uid line has fewer than 2 columns")
		}
		uid, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, errors.New("proc status: effective uid is not a number")
		}
		return uid, nil
	}
	return 0, errors.New("proc status: no Uid line")
}

// scanLinuxProcTree 走一遍 /proc 布局的树,返回普查数据与认出的 Core。
//
// readable 的判据是「cmdline 非空且 status 可解析」——内核线程连「不是 Core」
// 这个结论都给不出(没有 argv 可判),算进 readable 会稀释 decideCoreScan
// 那条「一个都没读成必须报错」的下限。
//
// /proc/<pid>/exe 读不出(权限、进程刚退出)不算致命:looksLikeCore 有
// argv[0] 兜底;读出来带 " (deleted)" 尾巴(升级换掉了二进制、旧 Core 还在跑)
// 必须剥掉再判——漏认一个正在跑的旧版 Core 就是 af81632 那个双 Core 风险。
func scanLinuxProcTree(root string) (enumerated, readable int, cores []Process) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, nil
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		enumerated++
		dir := filepath.Join(root, entry.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue // 进程刚退出:跳过这一个
		}
		state, err := parseProcStatState(stat)
		if err != nil {
			continue
		}
		if state == 'Z' {
			continue // 已经退出、只等回收:不持有 socket 也不持有路由
		}
		cmdlineRaw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		argv := parseProcCmdline(cmdlineRaw)
		if len(argv) == 0 {
			continue // 内核线程:没有 argv 可判,不算 readable
		}
		statusRaw, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil {
			continue
		}
		uid, err := parseProcStatusUID(statusRaw)
		if err != nil {
			continue
		}
		readable++
		executable, err := os.Readlink(filepath.Join(dir, "exe"))
		if err != nil {
			executable = "" // 权限不足:looksLikeCore 走 argv[0] 兜底
		}
		executable = strings.TrimSuffix(executable, " (deleted)")
		if !looksLikeCore(executable, argv, uid) {
			continue
		}
		cores = append(cores, Process{PID: pid, Executable: executable, UID: uid})
	}
	return enumerated, readable, cores
}
