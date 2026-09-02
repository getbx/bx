package cli

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"
	"rsc.io/qr"
)

// 手机那条路此前是**断的,不是不方便**。
//
// `bx server share <name>` 打的是一整条 `sudo bx setup 'bx://…' --udp 'bx://…'`,
// 而 `bx://` 是 bx 自己的信封(blink.Encode:base64url 的
// `{"v":1,"t":"vless","link":"vless://…"}`)—— sing-box / Hiddify / v2rayN /
// NekoBox / Shadowrocket **一个都不认**。真正的 `vless://` 就躺在信封里,
// 没有任何一条命令能把它拿出来。
//
// 于是想给手机用只能:登 VPS → 翻 /etc/bx-server 的 JSON → 手抄 uuid/公钥/
// SNI/端口 → 自己拼一条链接。
//
// 这个文件补的就是那一米。

// phoneShareOutput 是给人看的那几行:原始链接、或者二维码。
//
// **抽成纯函数才盯得住**:「说了什么」与「说得像句人话」是两件事,而这条路上
// 输出的是**凭据** —— 多打一份、少打一句警告,都是安全后果,不是排版问题。
type phoneShareOutput struct {
	// Link 是主链接(reality/brook…),UDPLink 是可选的 hysteria2 加速档。
	Link, UDPLink string
	// QR 为真时画二维码而不是打印链接原文。
	QR bool
	// Invert 给深色终端。
	Invert bool
}

// renderPhoneShare 产出给人看的那几行。
//
// **二维码模式下绝不同时打印链接原文** —— 那正好抵消掉用二维码的理由:
// 凭据以图形出现,不进 shell history、不经剪贴板、不经聊天软件。两样都打
// 等于「加了把锁,钥匙插在上面」。
func renderPhoneShare(out phoneShareOutput, encode func(string) (int, func(x, y int) bool, error)) (string, error) {
	if strings.TrimSpace(out.Link) == "" {
		// **没有链接就说没有,不画一张空码。** 一张空白的二维码看起来像
		// 「画出来了、只是扫不出来」,会让人去查扫描器。
		return "", fmt.Errorf("这台 server 还没有可分享的链接(先 bx server install,或给 --host)")
	}
	var b strings.Builder
	if !out.QR {
		b.WriteString(out.Link + "\n")
		if out.UDPLink != "" {
			b.WriteString(out.UDPLink + "\n")
		}
		// 与 rawLinkRisk 同一条:裸链接自带凭据,而它刚刚进了 shell 历史。
		b.WriteString("⚠ 以上是含明文凭据的裸链接,已留进 shell 历史;" +
			"发给别人前想清楚经过哪些地方,或者改用 --qr(凭据以图形出现,不进历史、不经剪贴板)\n")
		return b.String(), nil
	}

	size, dark, err := encode(out.Link)
	if err != nil {
		return "", fmt.Errorf("生成二维码: %w", err)
	}
	b.WriteString(qrHalfBlocks(size, dark, out.Invert))
	b.WriteString("用 sing-box / Hiddify / v2rayN / NekoBox 扫这张码导入。\n")
	if !out.Invert {
		// 极性在深色终端上是反的。现代扫描器多数认得反色,但不是全部,
		// 而一张扫不出来的码与没有这个功能完全一样 —— 所以把出路说出来。
		b.WriteString("扫不出来的话终端多半是深色主题,加 --qr-invert 再来一次。\n")
	}
	if out.UDPLink != "" {
		// **第二条链接刻意不画第二张码。** 手机客户端一次导入一条,而 UDP 那档
		// 是 bx 的按类分流(加速,可选);多摆一张码会让人以为两张都得扫。
		b.WriteString("(这台还有一条 hysteria2 UDP 加速链接,手机上可选;要的话用 --format link 看原文)\n")
	}
	return b.String(), nil
}

// qrEncode 是 renderPhoneShare 的生产实现。
func qrEncode(text string) (int, func(x, y int) bool, error) {
	// L 级(约 7% 纠错)是终端二维码的惯例:屏幕上没有磨损与污渍,把纠错
	// 留给更高级别只会把码变大、更难扫。
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return 0, nil, err
	}
	return code.Size, code.Black, nil
}

// phoneShareFor 按 flag 决定要不要走手机那条出口。
//
// 返回的 ok=false 表示「用户没要这条路」，调用方照原样打它本来要打的东西 ——
// **默认行为一个字节都不变**:`bx setup` 那条命令仍是给电脑用的正解。
func phoneShareFor(c *cli.Context, link, udpLink string) (string, bool, error) {
	wantQR := c.Bool("qr")
	format := strings.TrimSpace(c.String("format"))
	if !wantQR && format == "" {
		return "", false, nil
	}
	if format != "" && format != "link" {
		return "", false, fmt.Errorf("--format 只认 link(手机客户端能吃的裸链接);要给电脑用就别加这个 flag")
	}
	out, err := renderPhoneShare(phoneShareOutput{
		Link: link, UDPLink: udpLink, QR: wantQR, Invert: c.Bool("qr-invert"),
	}, qrEncode)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

// findShare 按名字取一个 share。**纯函数**,与读盘分开才好测。
func findShare(shares []shareInfo, name string) (shareInfo, bool) {
	for _, s := range shares {
		if s.Name == name {
			return s, true
		}
	}
	return shareInfo{}, false
}

// replayShare 重新取出一个已有 share 的链接(裸链接或二维码)。
//
// ok=false 表示「用户没要这条路」,调用方照原样列表。
func replayShare(c *cli.Context, shares []shareInfo) (string, bool, error) {
	wantQR := c.Bool("qr")
	format := strings.TrimSpace(c.String("format"))
	if !wantQR && format == "" {
		return "", false, nil
	}
	if format != "" && format != "link" {
		return "", false, fmt.Errorf("--format 只认 link")
	}
	// **--json 与 --format link 是矛盾指令,不许静默挑一个。**
	// 前者刻意脱敏(SecretsRedacted: true),后者刻意打出凭据原文 —— 悄悄执行
	// 其中一个,用户不会知道自己拿到的是哪一种,而这两种的处置完全不同。
	if c.Bool("json") {
		return "", false, fmt.Errorf("--json 是脱敏输出,与 --qr/--format link(打印凭据)矛盾;只用其中一个")
	}
	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		// **不许把所有 share 的凭据一次全打出来。** 一条命令泄漏全部钥匙,
		// 而用户想要的几乎总是其中一个。
		return "", false, fmt.Errorf("要哪个 share?例如:bx server shares alice --qr(先 bx server shares 看名字)")
	}
	s, ok := findShare(shares, name)
	if !ok {
		return "", false, fmt.Errorf("没有名为 %q 的 share(bx server shares 看现有的)", name)
	}
	out, err := renderPhoneShare(phoneShareOutput{
		Link: s.Config.Link, UDPLink: s.Config.UDPLink, QR: wantQR, Invert: c.Bool("qr-invert"),
	}, qrEncode)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}
