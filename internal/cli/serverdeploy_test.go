package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/deploy"

	"github.com/getbx/bx/internal/elevate"

	updatepkg "github.com/getbx/bx/internal/update"
)

// 只有一条链接时 UDP 为空,不许把主链接复制一份当 UDP。
// **样本由真正打印它的那个函数生成,不再手抄。** 上面那份手抄样本是「链接在前、--udp 在后」,
// 而 2026-08-14 起 setupCommandLine 改成了 flag 在前(`bx setup` 自己就是这么要求的)——
// 解析器照旧按位置取「第一条 = 主链接」,于是此后每一次部署都把 hysteria2 记成了主链接、
// reality 记成了 UDP(Mac 上碰巧照样能连,所以没人发现;手机只认 reality,「Add to iPhone」
// 那张二维码会被拒)。手抄的样本与真输出漂开,守卫就在最需要它的时候失明。
func TestClientLinksFromWhatInstallActuallyPrints(t *testing.T) {
	out := "🔀 reality … are ready:\n  " + setupCommandLine("bx://MAIN", "bx://UDP") + "\n"
	main, udp, err := deploy.ClientLinksFromInstallOutput(out)
	if err != nil || main != "bx://MAIN" || udp != "bx://UDP" {
		t.Fatalf("from %q: main=%q udp=%q err=%v", out, main, udp, err)
	}
	only := setupCommand("bx://ONLY", "")
	if main, udp, err := deploy.ClientLinksFromInstallOutput(only); err != nil || main != "bx://ONLY" || udp != "" {
		t.Fatalf("from %q: main=%q udp=%q err=%v", only, main, udp, err)
	}
}

// **部署的真实顺序:先问架构,再按那个架构取二进制、上传,装完才取链接、写本机配置。**
//
// 特别是「先探架构」——它决定了传哪个二进制,放在传输之后就没有意义了;而链接
// 只能从 install 的输出里取,取到之前绝不写本机配置。判据打在 runServerDeploy
// 真实发出的调用序列上(此前钉的是一张没有任何代码读的步骤常量表)。
func TestDeployRunsInTheOrderThatMakesSense(t *testing.T) {
	var events []string
	err := runServerDeploy(deployOptions{Host: "root@h", Protocol: "reality"}, deployDeps{
		run: func(name string, args ...string) (string, error) {
			joined := strings.Join(args, " ")
			switch {
			case name == "ssh" && strings.Contains(joined, "id -u"):
				events = append(events, "detect-arch")
				return "0\naarch64\n", nil
			case name == "scp":
				events = append(events, "upload")
				return "", nil
			case name == "ssh" && strings.Contains(joined, "server install"):
				events = append(events, "install")
				return "sudo bx setup 'bx://MAIN'", nil
			}
			return "", nil
		},
		fetchBinary: func(arch string) (string, error) {
			events = append(events, "fetch-"+arch)
			return "/tmp/bx", nil
		},
		writeLocalConfig: func(link string) error {
			events = append(events, "write-config "+link)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("部署失败:%v", err)
	}
	want := []string{"detect-arch", "fetch-arm64", "upload", "install", "write-config bx://MAIN"}
	if strings.Join(events, " | ") != strings.Join(want, " | ") {
		t.Fatalf("部署顺序 = %q\nwant      %q —— 顺序错了整条流程就没有意义", events, want)
	}
}

// 一次失败的部署**绝不能写本机配置**。
func TestDeployDoesNotTouchLocalConfigOnFailure(t *testing.T) {
	var wroteConfig bool
	err := runServerDeploy(deployOptions{Host: "root@1.2.3.4", Protocol: "reality"}, deployDeps{
		run: func(string, ...string) (string, error) { return "", errors.New("ssh: connect: refused") },
		fetchBinary: func(string) (string, error) {
			t.Fatal("连不上就不该去下载二进制")
			return "", nil
		},
		writeLocalConfig: func(string) error { wroteConfig = true; return nil },
	})
	if err == nil {
		t.Fatal("连不上却报告成功")
	}
	if wroteConfig {
		t.Fatal("部署失败却写了本机配置 —— 用户会以为换过去了")
	}
}

// **挑不到对应架构的产物就硬失败。**
//
// 继续下去只会把一个错架构的文件传上去,远端报一句 `exec format error` ——
// 与真实原因毫无关系,而那时文件已经在人家机器上了。
func TestLinuxAssetSelection(t *testing.T) {
	manifest := updatepkg.Manifest{Assets: []updatepkg.Asset{
		{Platform: "darwin/arm64", Name: "bx-macos-arm64.tar.gz", SHA256: "aa"},
		{Platform: "linux/amd64", Name: "bx_linux_amd64.tar.gz", SHA256: "bb"},
		{Platform: "linux/arm64", Name: "bx_linux_arm64.tar.gz", SHA256: "cc"},
	}}
	for _, arch := range []string{"amd64", "arm64"} {
		asset, err := linuxAssetFor(manifest, arch)
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if !strings.Contains(asset.Name, arch) || asset.SHA256 == "" {
			t.Fatalf("%s 挑错了:%+v", arch, asset)
		}
	}
	// **绝不退回 darwin 那条** —— 挑错平台比挑不到更糟。
	if asset, err := linuxAssetFor(manifest, "riscv64"); err == nil {
		t.Fatalf("没有该架构却挑出了 %+v", asset)
	}
	if _, err := linuxAssetFor(updatepkg.Manifest{}, "amd64"); err == nil {
		t.Fatal("空清单却挑出了东西")
	}
}

// **校验和是这条路上唯一的供应链闸门。**
//
// 我们正要把这个文件放进一台机器的 /usr/local/bin。上一版这段判定长在一个做
// 网络 I/O 的函数里,测试进不去 —— 变异验证时是**编译器碰巧**拦住的(变量未使用),
// 而那不是守卫。
func TestVerifyAssetBytes(t *testing.T) {
	payload := []byte("bx binary bytes")
	sum := sha256.Sum256(payload)
	good := updatepkg.Asset{Name: "bx_linux_amd64.tar.gz", SHA256: hex.EncodeToString(sum[:])}

	if err := verifyAssetBytes(payload, good); err != nil {
		t.Fatalf("对得上却报错:%v", err)
	}
	// 大小写不该影响比对(清单里可能是大写十六进制)。
	upper := good
	upper.SHA256 = strings.ToUpper(good.SHA256)
	if err := verifyAssetBytes(payload, upper); err != nil {
		t.Errorf("大写校验和被拒:%v", err)
	}
	// 内容被换掉 → 必须拒绝,且错误里要能看出是校验和的问题。
	err := verifyAssetBytes([]byte("tampered"), good)
	if err == nil {
		t.Fatal("内容被换掉却放行了")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("错误说不清是什么问题:%v", err)
	}
	// **清单里没有校验和 → 拒绝,不是放行。** 「不知道」在供应链上等同于「不可信」。
	if err := verifyAssetBytes(payload, updatepkg.Asset{Name: "x"}); err == nil {
		t.Fatal("清单没给校验和却放行了")
	}
}

// **接线守卫:被篡改的下载物必须在落盘之前就被拒。**
//
// 上一版校验长在一个做网络 I/O 的函数里,把它整段删掉没有任何测试会红 ——
// 函数有测试、调用点没有。这条走的是真实的挑选→下载→校验→解包全链。
func TestBuildLinuxBinaryRejectsTamperedDownload(t *testing.T) {
	good := makeTarGzWithBx(t, []byte("the real bx"))
	sum := sha256.Sum256(good)
	manifest := updatepkg.Manifest{Assets: []updatepkg.Asset{
		{Platform: "linux/amd64", Name: "bx_linux_amd64.tar.gz", SHA256: hex.EncodeToString(sum[:])},
	}}

	// 正常路径:落盘,内容对得上。
	path, err := buildLinuxBinary(manifest, "amd64", "v1.2.3", func(string) ([]byte, error) { return good, nil })
	if err != nil {
		t.Fatalf("正常下载被拒:%v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "the real bx" {
		t.Fatalf("落盘的内容不对:%q", got)
	}

	// **被掉包:必须失败,而且不许留下任何可执行文件。**
	tampered := makeTarGzWithBx(t, []byte("malicious payload"))
	if _, err := buildLinuxBinary(manifest, "amd64", "v1.2.3",
		func(string) ([]byte, error) { return tampered, nil }); err == nil {
		t.Fatal("被掉包的下载物通过了校验 —— 它会被放进别人机器的 /usr/local/bin")
	}
}

func makeTarGzWithBx(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "bx", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sudo 可能要问密码,而问密码需要 TTY。
func TestSSHGetsATTYWhenSudoIsNeeded(t *testing.T) {
	withSudo := sshArgsFor("user@host", true, true)
	if !containsArg(withSudo, "-t") {
		t.Errorf("需要 sudo 却没要 TTY,密码提示会出不来:%v", withSudo)
	}
	if plain := sshArgsFor("root@host", false, true); containsArg(plain, "-t") {
		t.Errorf("root 登录不必占用 TTY:%v", plain)
	}
	// **本地没有 TTY 时不许硬要。** 真机实测:`ssh -t` 在无终端环境里会打一句
	// "Pseudo-terminal will not be allocated…" 然后照跑 —— 噪声之外还给人
	// 一种「出错了」的错觉。
	if noTTY := sshArgsFor("user@host", true, false); containsArg(noTTY, "-t") {
		t.Errorf("本地没有终端却硬要 TTY:%v", noTTY)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// **每一条远端命令都要走同一个 sudo 包装。**
//
// 散在各调用点就会有某一条忘了包,而那条的失败方式极难查:前面每步都成功,
// 只有写文件那一步失败。这条测试逐条检查真实的调用序列。
func TestEveryRemoteCommandGoesThroughSudoWhenNeeded(t *testing.T) {
	var calls [][]string
	err := runServerDeploy(deployOptions{Host: "u@h", Protocol: "reality"}, deployDeps{
		run: func(name string, args ...string) (string, error) {
			calls = append(calls, append([]string{name}, args...))
			switch {
			case len(args) > 0 && strings.Contains(strings.Join(args, " "), "id -u"):
				return "1000\nx86_64\n", nil // 非 root
			case name == "scp":
				return "", nil
			}
			return "" + elevate.Prefix + "bx setup 'bx://MAIN'", nil
		},
		remoteFetch:      func(string, func(string) (string, error)) error { return nil },
		fetchBinary:      func(string) (string, error) { return "/tmp/bx", nil },
		writeLocalConfig: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("部署失败:%v", err)
	}
	var unwrapped []string
	for _, c := range calls {
		if c[0] != "ssh" {
			continue
		}
		joined := strings.Join(c, " ")
		// 探测那一条是在知道 uid 之前跑的,按定义不能包 sudo。
		if strings.Contains(joined, "id -u") {
			continue
		}
		if !strings.Contains(joined, "sudo sh -c") {
			unwrapped = append(unwrapped, joined)
		}
	}
	if len(unwrapped) > 0 {
		t.Fatalf("非 root 时有 %d 条远端命令没走 sudo:\n%s", len(unwrapped), strings.Join(unwrapped, "\n"))
	}
}

// root 登录时**不许**平白包一层 sudo(有些精简镜像根本没装 sudo)。
func TestRootLoginDoesNotWrapInSudo(t *testing.T) {
	var sawSudo bool
	_ = runServerDeploy(deployOptions{Host: "root@h"}, deployDeps{
		run: func(name string, args ...string) (string, error) {
			if strings.Contains(strings.Join(args, " "), "sudo") {
				sawSudo = true
			}
			if strings.Contains(strings.Join(args, " "), "id -u") {
				return "0\nx86_64\n", nil
			}
			return "" + elevate.Prefix + "bx setup 'bx://MAIN'", nil
		},
		remoteFetch:      func(string, func(string) (string, error)) error { return nil },
		fetchBinary:      func(string) (string, error) { return "/tmp/bx", nil },
		writeLocalConfig: func(string) error { return nil },
	})
	if sawSudo {
		t.Fatal("root 登录却包了 sudo —— 精简镜像可能根本没装它")
	}
}

// 「主链接 --udp UDP链接」要能拆回两半 —— 传给 `bx setup` 时它们是两个参数。
func TestSplitDeployedLink(t *testing.T) {
	main, udp := splitDeployedLink("bx://MAIN --udp bx://UDP")
	if main != "bx://MAIN" || udp != "bx://UDP" {
		t.Fatalf("main=%q udp=%q", main, udp)
	}
	main, udp = splitDeployedLink("bx://ONLY")
	if main != "bx://ONLY" || udp != "" {
		t.Fatalf("单链接被拆错:main=%q udp=%q", main, udp)
	}
	// **绝不能把主链接当成 UDP 用**(那会让 TCP 和 UDP 都走同一条,
	// 而用户以为自己有按类分流)。
	if _, udp := splitDeployedLink("bx://A bx://B"); udp != "" {
		t.Fatalf("没有 --udp 标记却认出了 UDP 链接:%q", udp)
	}
}
