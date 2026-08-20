//go:build darwin

package supervisor

import (
	"bytes"
	"fmt"

	"github.com/getbx/bx/internal/appattr"
	"golang.org/x/sys/unix"
)

type darwinAppSource struct{}

func newAppSource() appSource { return darwinAppSource{} }

// pcbTable 是 pcbTables 里的一项:一个 sysctl mib 名字 + 它对应的协议标记。
type pcbTable struct {
	mib   string
	isUDP bool
}

// pcbTables 是 mib 与 isUDP 的**一一对应表** —— TCP 表的每条记录归到
// UDP:false,UDP 表的每条归到 UDP:true。这两个表各自的端口空间互不相干:
// 同一个数字完全可能同时被一个 TCP socket 和一个 UDP socket 占用,若把它们
// 写进同一个键(裸 uint16),后写入的协议会静默覆盖先写入的那个 —— 是结构性
// 碰撞而非 TOCTOU。
//
// **这张表任何一项写反(比如 UDP 那行的 isUDP 误写成 false)都会让所有 UDP
// 流量被静默归到 TCP 键上**,而这在界面上完全看不出来 —— 用户只会看到一个
// 应用名,不会看到两个协议的归因被悄悄合并。表提升为包级具名变量,单独由
// appsource_darwin_test.go 的 TestPCBTablesTagProtocolCorrectly 钉住,不依赖
// 任何 syscall(与 internal/guardian/procscan.go 的 decideCoreScan 同一个
// 手法:syscall 那一半单测造不出来,能测的部分单独抽出来测)。
var pcbTables = [...]pcbTable{
	{"net.inet.tcp.pcblist_n", false},
	{"net.inet.udp.pcblist_n", true},
}

// OwnersByPort 读一次 TCP + UDP 的 pcblist,把 (端口,协议) join 成应用显示名。
//
// 真机实测(2026-08-19):两张表读+解析共 451µs~1.5ms,产出 ~243 条映射;
// 再解 45 个不同 PID 的进程名约 330µs。所以整个函数可以按秒级频率调用。
//
// **必须以 root 跑** —— kern.procargs2 读 root 进程要权限,非 root 会让所有
// 系统守护进程的名字变成空串。Core 本身就是 root,菜单(uid 501)不行。
func (darwinAppSource) OwnersByPort() (map[appattr.PortKey]appattr.Owner, error) {
	owners := map[appattr.PortKey]appattr.Owner{}
	byPID := map[int32]appattr.Owner{}
	aliveCache := map[int32]bool{}
	alive := func(pid int32) bool {
		if v, ok := aliveCache[pid]; ok {
			return v
		}
		// kill(pid, 0):ESRCH 才是「不存在」。EPERM 说明进程活着但不归我们管
		// (Core 是 root,实际不会遇到),仍算活着 —— 与 Guardian 那边
		// ErrProcessNotRunning 的判据同源:只有明确的 ESRCH 才判死。
		err := unix.Kill(int(pid), 0)
		v := err == nil || err == unix.EPERM
		aliveCache[pid] = v
		return v
	}

	for _, tbl := range pcbTables {
		raw, err := unix.SysctlRaw(tbl.mib)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", tbl.mib, err)
		}
		pcbs, err := appattr.ParsePcbList(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", tbl.mib, err)
		}
		for _, pcb := range pcbs {
			pid, ok := appattr.ChooseOwner(pcb, alive)
			if !ok {
				continue // 查不出的端口**不进 map**,上层据此判 unknown
			}
			owner, cached := byPID[pid]
			if !cached {
				// **路径与显示名同源同一次读**:显示名本来就是从这条路径推出来的
				// (`DisplayName`),从前它读完就被丢掉,于是菜单侧只有文字没有
				// 图标。这里把它留下 —— 见 appattr.Owner 头上那段「刻意的信息面
				// 扩大」。
				exec := executablePathOf(pid)
				owner = appattr.Owner{Name: appattr.DisplayName(exec), ExecPath: exec}
				byPID[pid] = owner
			}
			// 空名字与「端口不在 map 里」是同一件事的两种写法 —— 都读作
			// unknown,故这里干脆不写入,省得下游还要再判断一次空串。
			if owner.Name != "" {
				owners[appattr.PortKey{Port: pcb.LocalPort, UDP: tbl.isUDP}] = owner
			}
		}
	}
	return owners, nil
}

// executablePathOf 读 kern.procargs2 的第一段(可执行路径)。
// 布局与 guardian/procscan_darwin.go 的 parseProcArgs 相同:
// [4 字节 argc][可执行路径 NUL]…
func executablePathOf(pid int32) string {
	raw, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil || len(raw) < 4 {
		return ""
	}
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return ""
	}
	return string(rest[:end])
}
