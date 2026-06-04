// Command tuncheck 是 TUN 引擎的端到端冒烟工具(需 root)。
//
// 它建一个 TUN 设备,用「固定上游」Dialer(忽略目标、一律连到 -upstream)
// 跑 tun.Engine,从而把引擎从 brook/分流里隔离出来,单独验证
// 「TUN 捕获 → netstack 终结 TCP → 双向 splice」在真实内核上跑通。
//
// 用法(root):
//
//	tuncheck -tun bx0 -upstream 127.0.0.1:9999 -setup -route 10.99.0.2/32
//	# 另开一个 echo/http 服务监听 127.0.0.1:9999
//	# 然后:curl http://10.99.0.2/   → 应被引擎捕获并转发到 127.0.0.1:9999
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/tun"
)

// fixedDialer 忽略目标,一律连到固定上游;并打印捕获到的 Meta。
type fixedDialer struct{ upstream string }

func (d fixedDialer) Dial(ctx context.Context, m route.Meta) (net.Conn, error) {
	netw := "tcp"
	if m.UDP {
		netw = "udp"
	}
	log.Printf("捕获连接: dst=%s:%d udp=%v → %s %s", m.IP, m.Port, m.UDP, netw, d.upstream)
	return (&net.Dialer{}).DialContext(ctx, netw, d.upstream)
}

func main() {
	tunName := flag.String("tun", "bx0", "TUN 设备名")
	mtu := flag.Uint("mtu", 1500, "MTU")
	upstream := flag.String("upstream", "127.0.0.1:9999", "固定上游 host:port")
	addr := flag.String("addr", "10.99.0.1/24", "给 TUN 配的地址")
	routeCIDR := flag.String("route", "10.99.0.2/32", "测试:把该网段路由进 TUN")
	setup := flag.Bool("setup", false, "自动用 ip 命令配置地址/up/路由")
	flag.Parse()

	ep, err := tun.OpenDevice(*tunName, uint32(*mtu))
	if err != nil {
		log.Fatalf("建 TUN 失败: %v", err)
	}
	log.Printf("TUN %s 已建立 (mtu=%d)", *tunName, *mtu)

	if *setup {
		mustRun("ip", "addr", "add", *addr, "dev", *tunName)
		mustRun("ip", "link", "set", *tunName, "up")
		mustRun("ip", "route", "add", *routeCIDR, "dev", *tunName)
		log.Printf("已配置: addr=%s up route=%s→%s", *addr, *routeCIDR, *tunName)
	}

	eng, err := tun.New(ep, fixedDialer{upstream: *upstream}, uint32(*mtu))
	if err != nil {
		log.Fatalf("启动引擎失败: %v", err)
	}
	log.Printf("引擎已启动,上游=%s。现在向 %s 内地址发流量试试(如 curl)。Ctrl-C 退出。", *upstream, *routeCIDR)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("收到信号,清理中…")
	eng.Close()
	if *setup {
		// 删设备会一并清掉地址/路由
		run("ip", "link", "del", *tunName)
	}
}

func mustRun(name string, args ...string) {
	if err := run(name, args...); err != nil {
		log.Fatalf("%s %v: %v", name, args, err)
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
