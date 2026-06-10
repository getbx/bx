//go:build darwin || windows

// wgbridge.go 把 wireguard-go 的跨平台 tun.Device(macOS=utun、Windows=wintun)
// 桥接成 gVisor 的 stack.LinkEndpoint。Linux 走 device_linux.go 的 fdbased,不用这里。
//
// 原理:用 gVisor channel.Endpoint 当中转,起两条 pump goroutine——
//
//	入站(设备 → 协议栈):Read 一个 IP 包 → InjectInbound
//	出站(协议栈 → 设备):ReadContext 取一个 IP 包 → Write
//
// wireguard 的 tun.Device 已在内部处理各平台帧差异(如 macOS utun 的 4 字节 AF 头),
// 我们只跟「裸 IP 包」打交道;但其 Read/Write 要求每个缓冲区在 offset 前留 >=4 字节余量。
package tun

import (
	"context"

	wgtun "golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// wgOffset 是给 wireguard tun.Device Read/Write 预留的头部余量(macOS 需 >=4 放 AF 头)。
const wgOffset = 4

// wgEndpoint 桥接 wireguard tun.Device ↔ gVisor 协议栈。
type wgEndpoint struct {
	*channel.Endpoint
	dev    wgtun.Device
	mtu    uint32
	cancel context.CancelFunc
	done   chan struct{} // 出站 pump 退出信号
}

// NewWGEndpoint 在 dev 上启动收发 pump,返回可交给 tun.New 的 LinkEndpoint
// 与一个停止/清理闭包(停 pump、关设备、关 endpoint,幂等)。
func NewWGEndpoint(dev wgtun.Device, mtu uint32) (stack.LinkEndpoint, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	e := &wgEndpoint{
		Endpoint: channel.New(256, mtu, ""),
		dev:      dev,
		mtu:      mtu,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go e.pumpInbound()
	go e.pumpOutbound(ctx)
	return e, e.close
}

// pumpInbound:从 tun 设备读裸 IP 包,注入协议栈。设备关闭时 Read 返回错误而退出。
func (e *wgEndpoint) pumpInbound() {
	buf := make([]byte, wgOffset+int(e.mtu)+4)
	bufs := [][]byte{buf}
	sizes := make([]int, 1)
	for {
		n, err := e.dev.Read(bufs, sizes, wgOffset)
		if err != nil {
			return
		}
		if n == 0 || sizes[0] == 0 {
			continue
		}
		pkt := bufs[0][wgOffset : wgOffset+sizes[0]]
		var proto tcpip.NetworkProtocolNumber
		switch pkt[0] >> 4 {
		case 4:
			proto = header.IPv4ProtocolNumber
		case 6:
			proto = header.IPv6ProtocolNumber
		default:
			continue
		}
		// 复制一份:bufs[0] 下一轮 Read 会复用,InjectInbound 后协议栈仍持有数据。
		data := make([]byte, sizes[0])
		copy(data, pkt)
		pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(data),
		})
		e.Endpoint.InjectInbound(proto, pb)
		pb.DecRef()
	}
}

// pumpOutbound:从协议栈取出站 IP 包,写回 tun 设备。ctx 取消或 endpoint 关闭时退出。
func (e *wgEndpoint) pumpOutbound(ctx context.Context) {
	defer close(e.done)
	for {
		pb := e.Endpoint.ReadContext(ctx)
		if pb == nil {
			return
		}
		view := pb.ToView()
		data := view.AsSlice()
		// wireguard Write 用 buf[offset-4:] 写 AF 头,故缓冲区需含 wgOffset 头部余量。
		out := make([]byte, wgOffset+len(data))
		copy(out[wgOffset:], data)
		_, _ = e.dev.Write([][]byte{out}, wgOffset)
		view.Release()
		pb.DecRef()
	}
}

// close 停 pump、关设备、关 endpoint(幂等:cancel/Close 可重复调用)。
func (e *wgEndpoint) close() {
	e.cancel()         // 停出站 pump
	_ = e.dev.Close()  // 让入站 pump 的 Read 返回错误退出 + 移除 utun/wintun
	e.Endpoint.Close() // 丢弃排队包,唤醒 ReadContext
	<-e.done           // 等出站 pump 收尾
}
