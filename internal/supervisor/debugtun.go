package supervisor

import (
	"context"
	"log"
)

// DebugTUN 只创建 TUN 设备并 hold 到 ctx 取消,**不起隧道、不碰路由/DNS/WFP**(系统网络零改动)。
// 供 `bx debug-tun` 在真机隔离验证最底层的接缝——wintun.dll 能否加载、适配器能否创建并进
// `ipconfig`、closeTUN 能否干净移除——而完全不冒断网/断 SSH 的风险,是安全梯度的第 1 步。
//
// 注意:OpenTUN 只建设备、不配地址(地址/路由都在 Hijack 里),故此处适配器无 IP,仅用于
// 「起得来、看得见、拆得掉」的冒烟验证。wgbridge 收发 pump 空转(无引擎读、无路由喂),
// closeTUN 停 pump + 关设备。
func DebugTUN(ctx context.Context, name, addr string, mtu uint32) error {
	plat := newPlatform()
	_, tunH, closeTUN, err := plat.OpenTUN(name, addr, mtu)
	if err != nil {
		return err
	}
	defer closeTUN()
	log.Printf("debug-tun: 已创建 TUN %q(mtu=%d);不起隧道、不碰路由/DNS/WFP。等待退出信号(Ctrl+C)…", tunH.Name, mtu)
	<-ctx.Done()
	log.Printf("debug-tun: 收到退出,移除 TUN…")
	return nil
}
