package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

// 真机 2026-09-02:`sudo bx probe` 在**用户的** ~/Library/Caches/bx 里建出 root
// 属主的文件(macOS 的 sudo 默认保留 HOME),之后普通用户跑 probe 就是
// permission denied —— 而那个失败**看起来像网络问题**(`8s 内未连通`),排障者
// 据此去查服务器和线路。**bx 自己就是造成它的那个人。**

// 属主对不上时要说清楚是什么、以及怎么修。
func TestCacheDirHintNamesTheCauseAndTheFix(t *testing.T) {
	got := cacheDirOwnershipHintFrom(0, true, 501, "/Users/y/Library/Caches/bx")
	if got == "" {
		t.Fatal("属主是 root 而我不是,却一个字没说")
	}
	if !strings.Contains(got, "sudo") {
		t.Errorf("没说清是 sudo 跑过造成的:%s", got)
	}
	if !strings.Contains(got, "chown") {
		t.Errorf("没给修法:%s", got)
	}
}

// **属主是自己就一个字不说。** 这是常态,恒真的提示会被训练成噪声。
func TestCacheDirHintStaysQuietWhenTheOwnerIsUs(t *testing.T) {
	if got := cacheDirOwnershipHintFrom(501, true, 501, "/tmp/x"); got != "" {
		t.Errorf("属主正常却报了:%s", got)
	}
}

// **拿不到属主就沉默,不猜。**
// 目录不存在(首次运行)、平台不给 Stat_t,都不该被读成「属主是 0」——
// 一句猜出来的所有权诊断比 permission denied 更能把人带偏。
func TestCacheDirHintSaysNothingWhenItCannotTell(t *testing.T) {
	if got := cacheDirOwnershipHintFrom(0, false, 501, "/tmp/absent"); got != "" {
		t.Errorf("拿不到属主却下了结论:%s", got)
	}
}

// **root 绝不写进调用者的家目录。**
//
// 这条是整件事的根:macOS 的 sudo 默认保留 HOME,os.UserCacheDir() 在
// `sudo bx probe` 下返回的是用户的 ~/Library/Caches。少了这道分叉,一条 sudo
// 命令就把后续所有非 root 调用变成一个指向错误方向的错误。
func TestRootProbeDirDoesNotLandInAUsersHome(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("这条要以非 root 跑才验得到那半分支")
	}
	// 非 root 时走用户缓存目录 —— 这半是原样行为。
	dir, err := userRuntimeDir()
	if err != nil {
		// 属主已经被 root 换掉时会报错,那正是另一条守卫要的行为。
		if !strings.Contains(err.Error(), "属主") {
			t.Fatalf("非 root 取目录失败,而且不是属主问题:%v", err)
		}
		return
	}
	if strings.HasPrefix(dir, config.DefaultDataDir) {
		t.Errorf("非 root 却落到了 root 的数据目录 %s", dir)
	}
	home, _ := os.UserHomeDir()
	if home != "" && !strings.HasPrefix(dir, home) {
		t.Errorf("非 root 的缓存目录 %s 不在自己家目录下", dir)
	}
}

// root 那半只能断言常量:测试进程不是 root,跑不到那条分支。
// **但「root 走的是哪个目录」这件事必须被写死钉住** —— 它是这个修复的全部内容。
func TestRootProbeDirIsTheRootOwnedDataDir(t *testing.T) {
	if !strings.HasPrefix(config.DefaultDataDir, "/var/") && !strings.HasPrefix(config.DefaultDataDir, "C:\\") {
		t.Fatalf("root 的落点 %s 不像一个 root 拥有的系统目录", config.DefaultDataDir)
	}
}

// **root 落在 root 自己的目录,非 root 落在用户缓存 —— 这是整个修复。**
//
// 抽成纯函数的理由:测试进程不是 root,root 那条分支在真调用里跑不到。
// 变异实测:把分叉去掉(root 照样用 os.UserCacheDir()),整包测试全绿 ——
// 而那正是这个 bug 本身。
func TestProbeRuntimeDirSplitsOnEffectiveUID(t *testing.T) {
	const cache = "/Users/y/Library/Caches"
	if got := probeRuntimeDirFor(0, cache); got != config.DefaultDataDir {
		t.Errorf("root 落到了 %s —— 它会在用户家目录里建出 root 属主的文件", got)
	}
	if got := probeRuntimeDirFor(501, cache); got == config.DefaultDataDir {
		t.Error("普通用户被送进了 root 的数据目录,那儿他写不了")
	}
	if got := probeRuntimeDirFor(501, cache); !strings.HasPrefix(got, cache) {
		t.Errorf("普通用户没落在自己的缓存目录:%s", got)
	}
}
