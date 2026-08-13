package leakserve

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// **每个有实现的平台都必须真的接上,而不是留一个空壳。**
//
// `facts_other.go` 曾经覆盖 !darwin —— 于是 `bx leakcheck` 在 Linux 上把每一条本机
// 结论都报成 not checked,而 Linux 是 bx 的第一平台。那份「空是诚实的」注释本身没错,
// 错的是它一直没被填上,而**没有任何东西会提醒**。
//
// 这条守卫钉的是「本平台的 LiveFactDeps 真的给了能力」。它在每个平台上跑,
// 所以 CI 三条腿各自会验自己那一份。
func TestLiveFactDepsAreWiredOnThisPlatform(t *testing.T) {
	deps := LiveFactDeps()
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
	default:
		t.Skipf("%s 上还没有本机事实的原语", runtime.GOOS)
	}
	for _, cap := range []struct {
		name string
		got  bool
	}{
		{"LookupRoute", deps.LookupRoute != nil},
		{"InspectDNS", deps.InspectDNS != nil},
		{"ListRoutes", deps.ListRoutes != nil},
		{"ListInterfaceKinds", deps.ListInterfaceKinds != nil},
		{"ListInterfaceAddrs", deps.ListInterfaceAddrs != nil},
		{"ListOverlays", deps.ListOverlays != nil},
		{"GuardianStatus", deps.GuardianStatus != nil},
	} {
		if !cap.got {
			t.Errorf("%s 上 %s 没接 —— 依赖它的结论会全部变成 not checked,而且没有任何症状",
				runtime.GOOS, cap.name)
		}
	}
}

// **Windows 那一份真机未验,而这件事必须写在代码里、不能只在我脑子里。**
//
// 那个平台上没有容器可跑、没有机器可取样,所以它全部用带类型的 API 而不是解析
// 命令输出(`route print` 的表头在中文 Windows 上就不是英文)。这条守卫钉住那个
// 决定:谁在 Windows 采集里引入文本解析,就该先弄到一台真机取样。
func TestWindowsFactsUseTypedAPIsNotTextParsing(t *testing.T) {
	path := filepath.Join("facts_windows.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 守卫失去意义,必须响亮失败", path, err)
	}
	source := string(raw)

	// **钉结构,不钉散文。** 第一版查的是 "route print" / "ipconfig" 这些词,
	// 结果匹配到了代码注释里拿它们当**反面例子**的那几句 —— 守卫钉错了东西,
	// 而这个仓库为同一类错误栽过不止一次。
	//
	// 判据改成:这个文件不许 import os/exec。散文触发不了它,而任何真的要去解析
	// 命令输出的改动都绕不过它。
	if strings.Contains(source, `"os/exec"`) {
		t.Error("Windows 采集 import 了 os/exec —— 命令输出的格式随版本与语言环境而变" +
			"(中文 Windows 的 route print 表头就不是英文),而这份代码没有真机可以取样" +
			"验证。用带类型的 API;真要解析文本,先弄到一台真机取样。")
	}
	if !strings.Contains(source, "真机未验") {
		t.Error("Windows 采集必须自己声明真机未验 —— 那件事不该只活在提交信息里")
	}
}
