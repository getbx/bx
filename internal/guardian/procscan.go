package guardian

import "fmt"

// procStatZombie 是 BSD/Darwin `struct extern_proc.p_stat` 的 SZOMB。
// x/sys/unix 没有导出这个常量(实测 v0.45.0 无 unix.SZOMB),故在此固定。
// 取值来自 xnu bsd/sys/proc.h:SIDL=1 SRUN=2 SSLEEP=3 SSTOP=4 SZOMB=5。
const procStatZombie int8 = 5

// isZombieProcess 判定一个进程是不是已经死掉、只等父进程回收的僵尸。
//
// 僵尸进程**不是在跑的 Core**:它的地址空间已经释放,不持有控制 socket,
// 也不持有任何路由。把它算成「有 Core 在跑」会卡死 Guardian 的崩溃重启路径
// ——`handleUnexpectedExit` 正是在旧 Core 刚死之后去 fork 新的,若那一瞬间旧
// Core 还以僵尸形态挂在进程表里,启动前的扫描就会认定「已经有 Core 在跑」而
// 拒绝重启,于是每一次崩溃都变成永久失联。
//
// 这不是放宽 fail-closed:僵尸按定义已经退出,漏认它不会产生第二个活着的 Core。
func isZombieProcess(stat int8) bool { return stat == procStatZombie }

// decideCoreScan 把一次进程扫描的普查数据折算成最终答案。
//
// 抽成纯函数是为了让那条**下限**可被单测钉住:syscall 那一半在单测里没法造,
// 而这条下限恰恰是「0 个 Core、err=nil」与「如实查过、确实没有 Core」的唯一
// 分界线——把它改成恒不触发,整套测试仍然全绿(复审实测),等于没有保护。
//
// readable==0(枚举到了进程,却一个的参数都读不出来)必须报错:这种系统状态
// 本身就反常,不能把「读不出任何进程参数」悄悄折叠成「没有 bx 进程」。调用方
// (resolveOrphanLaunchMarker / refuseUnrecordedRunningCore)看到空列表就会
// 判定可以自愈并放行一次 fork,而系统里可能正跑着 Core。
//
// 即使 cores 非空也照样报错:两种返回值都让调用方 fail-closed 拒绝启动,
// 而在一次自相矛盾的普查上多说一句话没有意义。
func decideCoreScan(enumerated, readable int, cores []Process) ([]Process, error) {
	if readable == 0 {
		return nil, fmt.Errorf("read process arguments from 0 of %d enumerated processes: cannot rule out a running Core", enumerated)
	}
	return cores, nil
}
