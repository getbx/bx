// Package dialfail 把一次拨号失败归成**可行动的几类**。
//
// 起因是一个真机数字:`*.qq.com` 累计 2679 次判定里失败 410 次(15.4%,单一
// 版本、1.1 天)。那个百分比**答不出该不该管** —— CLAUDE.md 自己写着
// 「`network is unreachable` 是路由问题,`i/o timeout` 是目标不应答」,两者
// 是完全不同的诊断:前者要修 bx(2026-08-13 那个 DirectDialer 的签名),
// 后者一个字都不用改。
//
// 而错误对象一直就在手边:`conn, err := d.Direct.DialContext(...)`,err 进了
// 一行 debug 日志然后被扔掉,只留下「失败了一次」这个计数。**bx 看见了,然后
// 不说** —— 这个包就是把它说出来。
//
// 类别名下沉到叶子包(不 import 本仓库任何东西)的理由与 internal/udpsource
// 相同:它是 dialer(记账的一侧)与 stats(汇总的一侧)之间唯一按字面对齐的
// 东西,两边各写一份的后果**彻底静默** —— 计数照记,汇总一条都匹配不上,
// 输出与「一次失败都没有」逐字节相同。
package dialfail

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
)

const (
	// Unreachable:内核说这条路走不通(ENETUNREACH/EHOSTUNREACH)。
	// **这一类通常是 bx 自己的问题** —— 2026-08-13 那次 macOS DirectDialer
	// 拿不到 scoped 默认路由,全部用户 direct 规则都落在这一类上。
	Unreachable = "unreachable"
	// Timeout:连上不了,对端不应答。**这一类通常不是 bx 的问题。**
	Timeout = "timeout"
	// Refused:对端明确拒绝(ECONNREFUSED)——端口没人听。
	Refused = "refused"
	// Reset:连接被对端重置。跨墙路径上它常常是被干扰,不是对端的意思。
	Reset = "reset"
	// DNS:名字没解析出来。它与「连不上」是两件事:一条把域名逼向坏解析器的
	// 规则会全部落在这里,而那时去查网络路径是白费功夫。
	DNS = "dns"
	// Canceled:调用方自己走了(context 取消),**严格说不是这条规则的失败**。
	//
	// 本次改动**只给它一个名字,不改变它是否计入失败** —— 先量,再决定。
	// 反过来做(先改计数再看数据)会让改动前后的累计值不可比,而那份累计正是
	// 判断「这条规则该不该管」的唯一依据。
	Canceled = "canceled"
	// EgressUnwired:具名出口在 config 里有、运行时没接上。
	// **它不是一次拨号失败**,归成 unreachable/timeout 会把人送去查网络,
	// 而该改的是配置;归成 Other 又把它混进「认不出原因」那一堆。
	// Classify 永远不产出它 —— 它由那条路径直接指定。
	EgressUnwired = "egress_unwired"
	// Other:认不出来。**零值刻意不是它** —— 见 Classify。
	Other = "other"
)

// Classify 把一个拨号错误归类。
//
// **nil 返回空串,不是 Other。** 「没有失败」与「失败了但认不出原因」是两件事,
// 压成同一个值会让一次成功的拨号在汇总里长出一条 other 记录。
//
// 顺序是判据的一部分:DNS 排在超时之前(一次 DNS 超时的可行动信息是「解析器
// 有问题」,不是「对端不应答」);不可达排在最前(它不是超时,而且是最要紧的
// 那一类)。
func Classify(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return Unreachable
	case errors.Is(err, syscall.ECONNREFUSED):
		return Refused
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		return Reset
	case errors.Is(err, context.Canceled):
		return Canceled
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return DNS
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return Timeout
	}
	return Other
}

// LooksLikeOurFault 说明这一类失败**通常指向 bx 自己**,而不是对端。
//
// 它是给渲染层用的措辞依据,不是判决:一条 15% 都是 unreachable 的规则值得
// 立刻去查路由,而 15% 都是 timeout 的规则多半什么都不用做。
func LooksLikeOurFault(kind string) bool {
	return kind == Unreachable || kind == DNS
}
