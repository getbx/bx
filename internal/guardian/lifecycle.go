package guardian

import (
	"context"
	"fmt"
	"net"
	"reflect"
)

// lifecyclePlatform 是 Guardian 的平台缝清单:daemon 组装(RunDaemon/StartDaemon)
// 对平台可变实现的每一次选用,都必须经这里的一个字段,而不是直接点名 build-tag
// 自由函数——这样「Guardian 在这个平台上需要什么」是一份类型可见的检查表,
// 终局第 3 步给 Linux 供货时照单实现即可。
//
// 选择机制刻意保持编译期(字段引用的符号由 build tag 落到本 OS 实现),
// 与数据面 platform_<os>.go 同构;这里不做运行时注入——options.PeerCredentials
// 与 options.networkObserver 两个既有注入缝已覆盖全部测试需要,多一个没有
// 消费者的注入面只是多一处静默失配的机会。
//
// 刻意不在清单里的缝(别往里加,每条有记档的理由,详见
// docs/superpowers/plans/2026-08-29-lifecycle-platform-seam.md):
//   - scanRunningCores:ExecCoreRunner 的注入钩子无参,经转发会丢
//     reason=lifecycle|observe 审计标签;
//   - inspectProcess:process_unix.go 已覆盖 Linux,无缝可画;
//   - NewDNSManager / LegacyCore:平台差异住在 internal/install,不在 guardian;
//   - RemoveBlockingBarrierRoutes:CLI 逃生口专用,按不变量独立于 daemon。
type lifecyclePlatform struct {
	RequireDaemon      func() error
	NewBarrier         func(CommandRunner) Barrier
	DiscoverGateway    func(context.Context) (string, error)
	NewNetworkObserver func(networkRecoveryRequester) daemonNetworkObserver
	PeerCredentials    func(net.Conn) (uint32, bool)
}

func newLifecyclePlatform() lifecyclePlatform {
	return lifecyclePlatform{
		RequireDaemon:      requireDaemonPlatform,
		NewBarrier:         NewBarrier,
		DiscoverGateway:    DiscoverDefaultGateway,
		NewNetworkObserver: newPlatformNetworkObserver,
		PeerCredentials:    localPeerCredentials,
	}
}

// validate 用反射穷举字段:新加的字段自动进检查范围,加字段的人不需要记得
// 回来改这里——与 statusdigest 嵌套穷举守卫同一条「默认参与」纪律。
func (p lifecyclePlatform) validate() error {
	v := reflect.ValueOf(p)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.Func {
			return fmt.Errorf("lifecyclePlatform.%s 不是函数字段:清单只收平台构造器", t.Field(i).Name)
		}
		if f.IsNil() {
			return fmt.Errorf("lifecyclePlatform.%s 未接线:平台清单不许有洞", t.Field(i).Name)
		}
	}
	return nil
}
