package supervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/tunnel"
)

// 注入缝的存在理由是集成台要在 netns 里跑完整 Run() 而不发任何真实外网流量。
// 它是本代码库唯一一处「组装根可以被外部指定」的缝 —— 本轮两个 Critical 都住在
// Run() 的接线里,而接线不可测正是它们能活下来的原因。
func TestOptionsExposeTunnelBuilderSeam(t *testing.T) {
	f, ok := reflect.TypeOf(Options{}).FieldByName("BuildTunnel")
	if !ok {
		t.Fatal("Options 需要 BuildTunnel 注入缝")
	}
	want := "func(string, string, bool) (*tunnel.Tunnel, error)"
	if got := f.Type.String(); got != want {
		t.Fatalf("BuildTunnel 签名必须与内部 buildTunnel 闭包一致\ngot  %s\nwant %s", got, want)
	}
}

// 上面那条只证明字段**存在**。复审实测:把 Run() 里那句
// `if opts.BuildTunnel != nil { return opts.BuildTunnel(...) }` 整个删掉
// —— 字段还在、类型还对、那条测试照样绿,而缝已经死了。这正是本项目反复
// 交付过的那种「守卫通过而它守的东西是坏的」。
//
// 这一条走真正的 Run()。它是本仓库**第一个**调用 supervisor.Run() 的测试
// (此前是零:整个生产建隧道分支的执行覆盖率为 0)。
//
// 为什么它不碰系统:global=true 时 china 列表整段跳过;newPlatform() 是裸结构体
// 字面量;而建隧道是 Run() 里第一个能失败返回的动作 —— 早于 OpenTUN、早于 Hijack、
// 早于任何监听与提权操作。注入一个立即报错的 builder,Run() 就在动任何东西之前回来。
func TestRunActuallyConsultsTheTunnelSeam(t *testing.T) {
	cfg, err := config.Parse([]byte("server: vless://u@192.0.2.10:443\nglobal: true\ndata_dir: " + t.TempDir() + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("seam-probe")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	called := 0
	err = Run(ctx, cfg, Options{
		BuildTunnel: func(link, recoveryID string, auxiliaryHTTP bool) (*tunnel.Tunnel, error) {
			called++
			if link != cfg.Server {
				t.Errorf("缝收到的 link 应是当前服务器, got %q", link)
			}
			return nil, sentinel
		},
	})
	if called != 1 {
		t.Fatalf("Run() 必须真的经过这道缝建隧道(调用 %d 次)—— 字段存在不等于被用上", called)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("注入的错误必须一路传回来, got %v", err)
	}
}
