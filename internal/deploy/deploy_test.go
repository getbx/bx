package deploy

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"
	"github.com/getbx/bx/internal/update"
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
			got, err := ArchFromUname(tc.uname)
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

	main, udp, err := ClientLinksFromInstallOutput(real)
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

func TestClientLinksWithoutUDP(t *testing.T) {
	main, udp, err := ClientLinksFromInstallOutput("  " + elevate.Prefix + "bx setup 'bx://ONLY'")
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
	link, _, err := ClientLinksFromInstallOutput(good)
	if err != nil {
		t.Fatalf("认不出链接:%v", err)
	}
	if !strings.HasPrefix(link, "bx://") || strings.ContainsAny(link, " \n\t") {
		t.Fatalf("取出来的链接不干净:%q", link)
	}

	// 取不到:报错里必须带上远端到底说了什么。
	_, _, err = ClientLinksFromInstallOutput("Permission denied (publickey).")
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
	link, _, err := ClientLinksFromInstallOutput(out)
	if err != nil || link != "bx://AAAA" {
		t.Fatalf("= %q,%v", link, err)
	}
}

// **远端命令里不许出现用户提供的原始字符串未加引号的拼接。**
//
// 目标主机名来自命令行,而这条命令交给 shell 执行。判据钉的是 install 那条:
// 它带用户给的 --sni / --port,拼错一个引号就是远程命令注入。
func TestRemoteInstallCommandQuotesUserInput(t *testing.T) {
	cmd := InstallCommand(Options{Protocol: "reality", SNI: "a.com; rm -rf /", Port: 443})
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
	upload, move := UploadPaths()
	if upload == move {
		t.Fatal("直接往目标路径传 —— 覆盖正在跑的二进制会 text file busy")
	}
	if !strings.HasPrefix(move, "/usr/local/bin/") {
		t.Fatalf("目标路径不对:%q", move)
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
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %s, want %s", tc.in, got, tc.want)
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
		out, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(evil)).Output()
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

// **谁下载、谁验证,是两件事。**
//
// 真机实测(2026-08-14,203.0.113.173):同一个 27.6MB 的 GitHub 资产,
// VPS 直下 **8.36 MB/s**(~3 秒),而本机经隧道绕美国再回来只有 **17 KB/s**
// (~27 分钟)—— 差 490 倍。**「本机环境已知可用」在可靠性上成立,在吞吐上
// 正好相反**,因为 VPS 本来就在目的地那一侧。
//
// 于是:签名清单在本机取(小、验签),大文件让远端自己下,**用本机拿到的
// 校验和在远端核对**。远端下不动就回落到本机下载 + scp。
func TestRemoteFetchPlanPrefersTheServerThenFallsBack(t *testing.T) {
	asset := update.Asset{Platform: "linux/amd64", Name: "bx_linux_amd64.tar.gz", SHA256: "abc123"}

	// 远端能下:走远端,而且**必须带上本机拿到的校验和**去核对。
	cmd := FetchCommand("v0.2.7", asset)
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
			if got := ShouldFallBackToLocalUpload(errors.New(tc.remoteErr)); got != tc.wantFallbk {
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
	cmd := FirewallCommand(443)
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
	if alt := FirewallCommand(9999); !strings.Contains(alt, "9999/tcp") || strings.Contains(alt, "443") {
		t.Errorf("换端口时没跟上:%s", alt)
	}
}

// **只有 sudo 用户的人今天会撞到一句没有指引的 `Permission denied`。**
//
// `PermitRootLogin prohibit-password` 是 Debian/Ubuntu 的默认值 —— 项目所有者
// 第一次给我那台 VPS 时就是这个状态(2026-08-14)。deploy 却假定以 root 登录。
//
// **判据是问远端 `id -u`,不是让用户自己记得加 --sudo** —— 他多半不知道要加。
func TestNeedsSudoFromRemoteID(t *testing.T) {
	for _, tc := range []struct {
		out  string
		sudo bool
		bad  bool
	}{
		{out: "0", sudo: false},
		{out: " 0\n", sudo: false},
		{out: "1000", sudo: true},
		{out: "1000\n", sudo: true},
		{out: "", bad: true},
		{out: "bash: id: command not found", bad: true},
	} {
		got, err := NeedsSudo(tc.out)
		if tc.bad {
			if err == nil {
				t.Errorf("%q 应当报错,却得到 sudo=%v", tc.out, got)
			}
			continue
		}
		if err != nil || got != tc.sudo {
			t.Errorf("NeedsSudo(%q) = %v,%v;want %v", tc.out, got, err, tc.sudo)
		}
	}
}

// **`sudo` 前缀对多行脚本只作用于第一条命令 —— 这是这件事的要害。**
//
// 远端要跑的是带 `set -e`、重定向、管道的多行脚本(下载+校验+解包)。
// 简单地在前面加 "sudo " 会让后面每一条都以普通用户身份跑,而失败方式极其
// 难查:文件下下来了、校验过了,却写不进 /usr/local/bin。
func TestRemoteScriptWrapsTheWholeScriptUnderSudo(t *testing.T) {
	script := "set -e\ncurl -o /tmp/x https://example.com\nmv /tmp/x /usr/local/bin/bx"

	plain := WrapSudo(script, false)
	if plain != script {
		t.Errorf("非 sudo 时不该改写脚本:%q", plain)
	}

	wrapped := WrapSudo(script, true)
	if !strings.HasPrefix(wrapped, "sudo sh -c ") {
		t.Fatalf("没有把整段脚本交给一个 sudo 的 shell:%s", wrapped)
	}
	// 整段必须**在同一对引号里** —— 否则后面几行会掉到 sudo 之外。
	body := strings.TrimPrefix(wrapped, "sudo sh -c ")
	if !strings.HasPrefix(body, "'") || !strings.HasSuffix(body, "'") {
		t.Fatalf("脚本没被整段引起来:%s", body)
	}
	for _, line := range strings.Split(script, "\n") {
		if !strings.Contains(body, line) {
			t.Errorf("脚本里的 %q 掉在了 sudo 之外", line)
		}
	}
	// **真的丢给 shell 跑一遍**,确认包完之后仍然是一段完整可解析的东西。
	out, err := exec.Command("sh", "-c", "printf %s "+body).Output()
	if err != nil {
		t.Fatalf("包完之后 shell 解析不了:%v", err)
	}
	if string(out) != script {
		t.Fatalf("脚本经 shell 之后变了:\n%q\n%q", string(out), script)
	}
}
