package dialfail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// 这个包存在的全部理由:`*.qq.com 2679 次 / 410 次失败 (15.4%)` 这个真机数字
// **答不出该不该管**。同一个百分比,全是 unreachable 就要立刻去查路由
// (2026-08-13 那个 DirectDialer 故障的签名),全是 timeout 就一个字都不用改。

func TestClassifyDistinguishesTheActionableCases(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		// 路由问题 —— 通常是 bx 自己的。
		{"网络不可达", &net.OpError{Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}, Unreachable},
		{"主机不可达", &net.OpError{Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}, Unreachable},
		// 对端问题 —— 通常不是。
		{"连接被拒", &net.OpError{Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, Refused},
		{"连接被重置", &net.OpError{Err: os.NewSyscallError("read", syscall.ECONNRESET)}, Reset},
		{"超时", os.ErrDeadlineExceeded, Timeout},
		{"ctx 超时", context.DeadlineExceeded, Timeout},
		// 解析问题 —— 去查网络路径是白费功夫。
		{"DNS", &net.DNSError{Err: "no such host", Name: "x.example"}, DNS},
		// 调用方自己走了 —— 严格说不是这条规则的失败。
		{"取消", context.Canceled, Canceled},
		{"认不出", errors.New("something else entirely"), Other},
	} {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: 归成了 %q,应当是 %q", tc.name, got, tc.want)
		}
	}
}

// **包起来的错误也要认得出。** 生产里的错误从来不是裸的 —— dialer 拿到的是
// net.OpError 套 os.SyscallError 套 errno,而中间还可能有 fmt.Errorf 包一层。
// 只认裸 errno 的分类器在真机上会把每一条都归成 other,而那与没有分类完全一样。
func TestClassifySeesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("dial direct: %w",
		&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)})
	if got := Classify(wrapped); got != Unreachable {
		t.Errorf("包了两层之后归成了 %q,应当是 %q", got, Unreachable)
	}
}

// **nil 返回空串,不是 Other。**
// 「没有失败」与「失败了但认不出原因」是两件事;压成同一个值,一次成功的拨号
// 会在汇总里长出一条 other 记录,而那份汇总正是用来判断一条规则该不该管的。
func TestClassifyReturnsNothingForNoError(t *testing.T) {
	if got := Classify(nil); got != "" {
		t.Errorf("nil 归成了 %q,应当是空串", got)
	}
}

// **DNS 排在超时之前。** 一次 DNS 超时的可行动信息是「解析器有问题」,
// 不是「对端不应答」—— 后者会把人送去查一条完全正常的网络路径。
func TestClassifyPrefersDNSOverTimeout(t *testing.T) {
	dnsTimeout := &net.DNSError{Err: "i/o timeout", Name: "x.example", IsTimeout: true}
	if got := Classify(dnsTimeout); got != DNS {
		t.Errorf("DNS 超时归成了 %q,应当是 %q —— 它的可行动信息是解析器,不是对端", got, DNS)
	}
}

// 「通常是谁的错」只对真正指向 bx 的那两类为真。
// 判错方向的代价不对称:把对端的问题说成 bx 的,会让人去改一个没坏的东西。
func TestOnlyRoutingAndResolutionLookLikeOurFault(t *testing.T) {
	for _, kind := range []string{Unreachable, DNS} {
		if !LooksLikeOurFault(kind) {
			t.Errorf("%s 应当指向 bx 自己", kind)
		}
	}
	for _, kind := range []string{Timeout, Refused, Reset, Canceled, Other, ""} {
		if LooksLikeOurFault(kind) {
			t.Errorf("%s 不该被说成 bx 的问题 —— 会让人去改一个没坏的东西", kind)
		}
	}
}
