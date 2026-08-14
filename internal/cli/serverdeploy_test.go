package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	updatepkg "github.com/getbx/bx/internal/update"
)

// **`uname -m` 认不出来时必须硬失败。**
//
// 装错架构的二进制,远端报的是 `exec format error` —— 一句与真实原因毫无关系的话,
// 而那时用户已经把文件传上去、把服务装了一半。宁可在传任何东西之前就停下。
func TestReleaseArchFromUname(t *testing.T) {
	for _, tc := range []struct {
		uname string
		want  string
		bad   bool
	}{
		{uname: "x86_64", want: "amd64"},
		{uname: "  x86_64\n", want: "amd64"},
		{uname: "amd64", want: "amd64"},
		{uname: "aarch64", want: "arm64"},
		{uname: "arm64", want: "arm64"},
		{uname: "armv7l", bad: true},
		{uname: "i686", bad: true},
		{uname: "", bad: true},
		{uname: "bash: uname: command not found", bad: true},
	} {
		t.Run(tc.uname, func(t *testing.T) {
			got, err := releaseArchFromUname(tc.uname)
			if tc.bad {
				if err == nil {
					t.Fatalf("%q 应当被拒,却得到 %q", tc.uname, got)
				}
				if !strings.Contains(err.Error(), strings.TrimSpace(tc.uname)) && tc.uname != "" {
					t.Errorf("错误里没带上实际看到的内容,用户无从判断:%v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("%q → %q,%v;want %q", tc.uname, got, err, tc.want)
			}
		})
	}
}

// **真机输出:链接是带单引号的,而且有两条(主 + UDP)。**
//
// 2026-08-14 第一次对真 VPS 跑 deploy 时,前面每一步都成功,唯独最后取链接失败 ——
// 我的 fixture 用的是裸 `bx://…`,而 `bx server install` 打的是可直接复制的
// 一整条命令:`sudo bx setup 'bx://AAA' --udp 'bx://BBB'`。**假 fixture 被真机
// 一次跑就打穿了。** 这份用真实输出。
func TestClientLinksFromRealInstallOutput(t *testing.T) {
	real := `✅ bx server 已安装(协议 reality)。下一步:sudo bx server start
如果 VPS 启用了防火墙,请放行 TCP 443(ufw + 云安全组都要); ufw: sudo ufw allow 443/tcp
🔀 reality(TCP/隐蔽)+ hysteria2(UDP/加速)就绪。客户端一条命令配齐:
  sudo bx setup 'bx://MAIN' --udp 'bx://UDP'`

	main, udp, err := clientLinksFromInstallOutput(real)
	if err != nil {
		t.Fatalf("真机输出里认不出链接:%v", err)
	}
	if main != "bx://MAIN" {
		t.Errorf("主链接 = %q,单引号没剥掉?", main)
	}
	if udp != "bx://UDP" {
		t.Errorf("UDP 链接 = %q —— 远端给了两条,漏掉第二条会让 UDP 走主传输", udp)
	}
}

// 只有一条链接时 UDP 为空,不许把主链接复制一份当 UDP。
func TestClientLinksWithoutUDP(t *testing.T) {
	main, udp, err := clientLinksFromInstallOutput("  sudo bx setup 'bx://ONLY'")
	if err != nil || main != "bx://ONLY" {
		t.Fatalf("main=%q err=%v", main, err)
	}
	if udp != "" {
		t.Fatalf("凭空多出一条 UDP 链接:%q", udp)
	}
}

// **从 `bx server install` 的输出里取客户端链接。**
//
// 取不到就**硬失败并把输出原样交出去** —— 绝不能在没拿到链接的情况下往下走,
// 那会写出一份没有服务器的本机配置,而用户以为部署成功了。
func TestClientLinkFromInstallOutput(t *testing.T) {
	good := `✅ bx server 已安装
客户端链接:
bx://eyJ2IjoxLCJ0cmFuc3BvcnQiOiJyZWFsaXR5In0
下一步:在客户端上跑 bx setup <链接>`
	link, err := clientLinkFromInstallOutput(good)
	if err != nil {
		t.Fatalf("认不出链接:%v", err)
	}
	if !strings.HasPrefix(link, "bx://") || strings.ContainsAny(link, " \n\t") {
		t.Fatalf("取出来的链接不干净:%q", link)
	}

	// 取不到:报错里必须带上远端到底说了什么。
	_, err = clientLinkFromInstallOutput("Permission denied (publickey).")
	if err == nil {
		t.Fatal("没有链接却报告成功 —— 那会写出一份没有服务器的配置")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("错误里没有远端的原话:%v", err)
	}
}

// 多条链接时取**第一条** —— server install 先打主链接,后面可能还有 UDP 那条。
func TestClientLinkTakesTheFirstOne(t *testing.T) {
	out := "bx://AAAA\nUDP:\nbx://BBBB\n"
	link, err := clientLinkFromInstallOutput(out)
	if err != nil || link != "bx://AAAA" {
		t.Fatalf("= %q,%v", link, err)
	}
}

// **部署计划:传之前先问架构,装完再取链接。顺序不能乱。**
//
// 特别是「先探架构」——它决定了传哪个二进制,放在传输之后就没有意义了。
func TestDeployPlanOrder(t *testing.T) {
	steps := deployPlanSteps()
	want := []string{"detect-arch", "upload", "install", "read-link"}
	if len(steps) != len(want) {
		t.Fatalf("步骤数 = %d,want %d:%v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("第 %d 步是 %q,want %q —— 顺序错了整条流程就没有意义", i, steps[i], want[i])
		}
	}
}

// **远端命令里不许出现用户提供的原始字符串未加引号的拼接。**
//
// 目标主机名来自命令行,而这条命令交给 shell 执行。判据钉的是 install 那条:
// 它带用户给的 --sni / --port,拼错一个引号就是远程命令注入。
func TestRemoteInstallCommandQuotesUserInput(t *testing.T) {
	cmd := remoteInstallCommand(deployOptions{Protocol: "reality", SNI: "a.com; rm -rf /", Port: 443})
	if strings.Contains(cmd, "; rm -rf /") && !strings.Contains(cmd, "'a.com; rm -rf /'") {
		t.Fatalf("用户输入没被引起来,这是远程命令注入:%s", cmd)
	}
	if !strings.Contains(cmd, "--protocol") {
		t.Fatalf("命令里没有协议:%s", cmd)
	}
}

// 上传路径固定,且**先传到临时位置再原子移动** —— 直接覆盖正在运行的二进制
// 会得到 "text file busy",而那时旧服务已经停了。
func TestUploadUsesATempPathThenMoves(t *testing.T) {
	upload, move := remoteUploadPaths()
	if upload == move {
		t.Fatal("直接往目标路径传 —— 覆盖正在跑的二进制会 text file busy")
	}
	if !strings.HasPrefix(move, "/usr/local/bin/") {
		t.Fatalf("目标路径不对:%q", move)
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

// **单引号转义是一道注入闸门,必须单独钉。**
//
// 远端命令交给 shell 执行,而其中的 SNI / 主机名来自命令行。这里错一个字符,
// 就是把「一条部署命令」变成「远端任意命令执行」。
func TestShellSingleQuoted(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a.com", `'a.com'`},
		{"", `''`},
		{"a b", `'a b'`},
		{"a; rm -rf /", `'a; rm -rf /'`},
		{"$(whoami)", `'$(whoami)'`},
		{"`id`", "'`id`'"},
		{"it's", `'it'\''s'`},
		{`'; id; '`, `''\''; id; '\'''`},
	} {
		if got := shellSingleQuoted(tc.in); got != tc.want {
			t.Errorf("shellSingleQuoted(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	// **真的丢给 shell 跑一遍。**
	//
	// 第一版判据是「引号数目为偶数」—— 那是个错的代理指标:`'\''` 里那个
	// 反斜杠转义的引号在引号区之外,本来就不需要配对,于是完全正确的输出被判成
	// 「没闭合」。要判的性质只有一个:**shell 解析出来的那一个词等于原文**。
	for _, evil := range []string{
		"'", "''", `'; rm -rf / ; '`, `\'`, "a'b'c",
		"$(id)", "`id`", "a b", "\n", `"double"`, "‌零宽", "; touch /tmp/bx-injection-canary",
	} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellSingleQuoted(evil)).Output()
		if err != nil {
			t.Errorf("%q 引出来的东西 shell 解析失败:%v", evil, err)
			continue
		}
		if string(out) != evil {
			t.Errorf("%q 经 shell 之后变成了 %q —— 引用没做对", evil, string(out))
		}
	}
	if _, err := os.Stat("/tmp/bx-injection-canary"); err == nil {
		_ = os.Remove("/tmp/bx-injection-canary")
		t.Fatal("注入成功了 —— 上面那条 `; touch` 真的被执行了")
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
	if !strings.Contains(err.Error(), "校验和") {
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

// **谁下载、谁验证,是两件事。**
//
// 真机实测(2026-08-14,142.111.173.173):同一个 27.6MB 的 GitHub 资产,
// VPS 直下 **8.36 MB/s**(~3 秒),而本机经隧道绕美国再回来只有 **17 KB/s**
// (~27 分钟)—— 差 490 倍。**「本机环境已知可用」在可靠性上成立,在吞吐上
// 正好相反**,因为 VPS 本来就在目的地那一侧。
//
// 于是:签名清单在本机取(小、验签),大文件让远端自己下,**用本机拿到的
// 校验和在远端核对**。远端下不动就回落到本机下载 + scp。
func TestRemoteFetchPlanPrefersTheServerThenFallsBack(t *testing.T) {
	asset := updatepkg.Asset{Platform: "linux/amd64", Name: "bx_linux_amd64.tar.gz", SHA256: "abc123"}

	// 远端能下:走远端,而且**必须带上本机拿到的校验和**去核对。
	cmd := remoteFetchCommand("v0.2.7", asset)
	for _, want := range []string{"bx_linux_amd64.tar.gz", "v0.2.7", "abc123", "sha256sum"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("远端下载命令缺 %q —— 少了校验和就等于让服务器自己说自己下对了:%s", want, cmd)
		}
	}
	// 校验和不符时远端命令必须**失败**,而不是留下一个坏文件继续往下走。
	if !strings.Contains(cmd, "exit 1") && !strings.Contains(cmd, "||") {
		t.Errorf("远端校验失败时没有硬失败:%s", cmd)
	}
}

// **远端下载失败要回落,但校验失败绝不回落。**
//
// 前者是「这台机器连不上 GitHub」——换条路是对的;后者是「拿到的东西不对」,
// 换条路再拿一遍只会掩盖问题。
func TestDeployFallsBackOnFetchFailureButNotOnChecksumFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteErr  string
		wantFallbk bool
	}{
		{"远端没有 curl", "curl: command not found", true},
		{"远端连不上", "curl: (6) Could not resolve host", true},
		{"远端超时", "curl: (28) Operation timed out", true},
		{"校验和不符", "bx: checksum mismatch", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFallBackToLocalUpload(errors.New(tc.remoteErr)); got != tc.wantFallbk {
				t.Fatalf("回落判定 = %v, want %v(%q)", got, tc.wantFallbk, tc.remoteErr)
			}
		})
	}
}

// **装完的服务器要真的够得到 —— 防火墙是最后一道,而它默认关着。**
//
// 真机(2026-08-14,Ubuntu 24.04):`bx server install` 装完、systemd 起来了、
// sing-box 在 443 上 TCP+UDP 都 LISTEN,而 **ufw 默认 `deny (incoming)` 且没有
// 443 规则** —— 服务器在跑,外面进不来。`bx server install` 只打了一句提示,
// 而一条声称「一条命令装好」的路径把最后一道留给用户去读提示,等于没装好。
//
// **UDP 也要放行**:hysteria2 走 QUIC/UDP,只开 TCP 会让 UDP 那半静默失效。
func TestFirewallCommandOpensBothProtocols(t *testing.T) {
	cmd := remoteFirewallCommand(443)
	// **断言真正的动作,不是那句 echo 文案。**
	//
	// 第一版查的是「命令里有没有 443/udp」—— 而收尾那句
	// `echo 'bx: ufw 已放行 443/tcp 与 443/udp'` 里就有这个串,于是把 udp 那条
	// allow 整行删掉仍然全绿(变异验证当场发现)。判据钉在了旁边的东西上。
	var allows []string
	for _, line := range strings.Split(cmd, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "ufw allow ") {
			allows = append(allows, trimmed)
		}
	}
	if len(allows) != 2 {
		t.Fatalf("ufw allow 只有 %d 条,want 2(TCP + UDP):%v", len(allows), allows)
	}
	joined := strings.Join(allows, " | ")
	for _, want := range []string{"443/tcp", "443/udp"} {
		if !strings.Contains(joined, want) {
			t.Errorf("没有真的放行 %q —— hysteria2 走 UDP,只开 TCP 会让它静默失效:%s", want, joined)
		}
	}
	// **ufw 不存在或没启用时必须安静通过**,不能让一条没装防火墙的机器部署失败。
	if !strings.Contains(cmd, "command -v ufw") {
		t.Errorf("没有先判断 ufw 在不在:%s", cmd)
	}
	// 非默认端口要跟着走。
	if alt := remoteFirewallCommand(9999); !strings.Contains(alt, "9999/tcp") || strings.Contains(alt, "443") {
		t.Errorf("换端口时没跟上:%s", alt)
	}
}
