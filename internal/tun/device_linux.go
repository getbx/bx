//go:build linux

package tun

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	gvtun "gvisor.dev/gvisor/pkg/tcpip/link/tun"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// OpenDevice 打开/创建名为 name 的 TUN 设备(IFF_TUN|IFF_NO_PI,裸 IP),
// 返回可交给 New 的 netstack link 端点。需要 CAP_NET_ADMIN(root)。
//
// 注意:本函数只建设备并接上协议栈;给设备配地址、置 up、加路由
// 由上层(Supervisor)用 ip 命令或 netlink 完成。
func OpenDevice(name string, mtu uint32) (stack.LinkEndpoint, error) {
	fd, err := gvtun.Open(name)
	if err != nil {
		return nil, fmt.Errorf("打开 TUN 设备 %q(需 root): %w", name, err)
	}
	ep, err := fdbased.New(&fdbased.Options{
		FDs:            []int{fd},
		MTU:            mtu,
		EthernetHeader: false, // TUN 是裸 IP,无以太头
		// 主机内核写进 TUN 的本地包可能是 partial checksum,关掉 RX 校验避免被丢;
		// TX 仍由 netstack 计算正确校验和(回包要过内核校验)。
		RXChecksumOffload: true,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 fdbased 端点: %w", err)
	}
	return ep, nil
}
