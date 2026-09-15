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

// 「通常是谁的错」只对真正指向 bx 的那两类为 BlameLocal。
// 判错方向的代价不对称:把对端的问题说成 bx 的,会让人去改一个没坏的东西。
func TestOnlyRoutingAndResolutionLookLikeOurFault(t *testing.T) {
	for _, kind := range []string{Unreachable, DNS} {
		if BlameFor(kind) != BlameLocal {
			t.Errorf("%s 应当指向 bx 自己", kind)
		}
	}
	for _, kind := range []string{Timeout, Refused, Reset, Canceled, Other, ""} {
		if BlameFor(kind) == BlameLocal {
			t.Errorf("%s 不该被说成 bx 的问题 —— 会让人去改一个没坏的东西", kind)
		}
	}
}

// —— NXDOMAIN 与「够不着解析器」处置完全相反,绝不能合并(2026-09-03)——
//
// 真机首次产出:`*.qq.com` 本次运行 1454 次判定 / 963 次失败,分类全是 dns。
// 而那个答案**无法行动** —— 第一版把两件事合成了一档:
//
//   - 名字不存在(微信在查一批不存在的主机名)⇒ 不是 bx 的问题,一个字不用改
//   - 够不着 223.5.5.5 ⇒ 2026-08-13 那个 macOS DirectDialer 拿不到 scoped
//     默认路由的签名,而它 8-16 已经复发过一次,同样表现为 *.qq.com 大比例失败
//
// **net.Resolver 会把传输层失败也包成 *net.DNSError**,所以只判类型不够 ——
// 必须看 IsNotFound。合成一档,66% 这个数就又变回一个无法行动的百分比,
// 而那正是这个功能存在的全部理由。

func TestClassifySeparatesNXDOMAINFromAnUnreachableResolver(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "nope.qq.com", IsNotFound: true}
	if got := Classify(notFound); got != DNSNotFound {
		t.Errorf("NXDOMAIN 归成了 %q —— 那会让「应用在查不存在的名字」看起来像 bx 的路由坏了", got)
	}

	// 连不上 DNS 服务器:Go 把它包成 DNSError,但 IsNotFound 为假。
	unreachableResolver := &net.DNSError{
		Err:  "dial udp 223.5.5.5:53: connect: network is unreachable",
		Name: "mp.weixin.qq.com",
	}
	if got := Classify(unreachableResolver); got != DNS {
		t.Errorf("够不着解析器归成了 %q —— 那正是 2026-08-13 那个故障的签名,不能被当成「域名不存在」", got)
	}
}

// **只有「够不着解析器」指向 bx,NXDOMAIN 不指向。**
// 判错方向的代价:让人去修一个没坏的路由表,而真正该做的是看那个应用在查什么。
func TestNXDOMAINIsNotOurFault(t *testing.T) {
	if BlameFor(DNSNotFound) == BlameLocal {
		t.Error("把「域名不存在」说成了 bx 的问题")
	}
	if BlameFor(DNS) != BlameLocal {
		t.Error("「够不着解析器」应当指向 bx")
	}
}
