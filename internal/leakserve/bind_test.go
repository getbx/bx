package leakserve

import (
	"net"
	"reflect"
	"testing"
)

// 约束一:只对本机开口。**断言打在真实 listener 的地址上**,不是打在源码里的
// 那个字符串 —— 后者证明不了内核到底把它绑到了哪。
func TestListenerBindsLoopbackOnly(t *testing.T) {
	srv := newTestServer(t)
	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("监听地址不是 TCP:%T", srv.Addr())
	}
	if addr.IP.IsUnspecified() {
		t.Fatalf("绑到了 %s(0.0.0.0/::):对整个局域网开口", addr.IP)
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("必须只绑 loopback,实际绑在 %s —— 这台机器所在的局域网现在能读它", addr.IP)
	}
	if addr.Port == 0 {
		t.Fatal("端口为 0:listener 没真正绑上")
	}
}

// 随机端口:两个同时活着的服务不能落在同一个端口,而且不能是某个固定值。
// 固定端口会让「同机其它进程猜到在哪」变成一件不用猜的事。
func TestPortIsRandom(t *testing.T) {
	a := newTestServer(t).Addr().(*net.TCPAddr).Port
	b := newTestServer(t).Addr().(*net.TCPAddr).Port
	if a == b {
		t.Fatalf("两个服务落在同一个端口 %d:端口不是内核分配的随机端口", a)
	}
}

// 监听地址是硬编码的 loopback,**不吃任何外部输入**。可配置的监听地址是这类
// 工具最常见的一个洞:某个人为了「在虚拟机里也能开」把它做成 flag,于是默认
// 之外的每一次使用都在局域网上裸奔。
func TestListenAddressIsNotConfigurable(t *testing.T) {
	if listenAddress != "127.0.0.1:0" {
		t.Fatalf("监听地址被改成了 %q:必须恒为 127.0.0.1 上的随机端口", listenAddress)
	}
}

// TestOptionsHasNoListenAddressField 用反射穷举字段名。
//
// **原稿这条是写成结构体字面量的**(`Options{Judge: nil, HardTimeout: 0}`),
// 而带字段名的复合字面量不会因为结构体多了字段而编译失败 —— 那条断言挡不住
// 任何东西,它只是看起来像在守。必须真的去数。
func TestOptionsHasNoListenAddressField(t *testing.T) {
	typ := reflect.TypeOf(Options{})
	want := map[string]bool{"Judge": true, "HardTimeout": true}
	if typ.NumField() != len(want) {
		t.Fatalf("Options 现在有 %d 个字段,守卫只认识 %d 个 —— 加字段请连同这条守卫"+
			"一起论证,尤其别加任何形式的监听地址", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		if !want[typ.Field(i).Name] {
			t.Errorf("Options 多了字段 %q:监听地址必须恒为 127.0.0.1 上的随机端口,不可配置",
				typ.Field(i).Name)
		}
	}
}

// **常量与真实 listener 必须是同一件事。** 上面两条各自证明了「常量写得对」与
// 「跑起来绑在 loopback 上」,但**两条都绿而常量根本没人用**是可能的:Listen 里
// 留一个字面量,常量就成了摆设,改它不会改变服务真正绑在哪,而守常量那条会红得
// 像是「有人手滑改了个没用的东西」。
//
// 这条不读源码文本(那种守卫在本仓库被绕过过八次),它比对**内核给的地址**与
// **常量的主机名部分** —— 两者脱钩时必然不等。
func TestListenerAddressMatchesTheConstant(t *testing.T) {
	wantHost, wantPort, err := net.SplitHostPort(listenAddress)
	if err != nil {
		t.Fatalf("listenAddress %q 不是 host:port:%v", listenAddress, err)
	}
	if wantPort != "0" {
		t.Fatalf("listenAddress 的端口是 %q:必须是 0,交给内核随机分配", wantPort)
	}
	got := newTestServer(t).Addr().(*net.TCPAddr).IP.String()
	if got != wantHost {
		t.Fatalf("常量说绑 %q,内核实际绑在 %q —— 二者脱钩了:Listen 没有用这个常量,"+
			"于是改常量不会改变服务真正绑在哪", wantHost, got)
	}
}
