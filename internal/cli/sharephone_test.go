package cli

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

// 这条路上输出的是**凭据**。多打一份、少打一句警告,都是安全后果,不是排版问题。

const fakeVless = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com#alice"

func tinyEncode(string) (int, func(x, y int) bool, error) {
	return 3, func(x, y int) bool { return x == y }, nil
}

// **二维码模式下绝不同时打印链接原文。**
//
// 那正好抵消掉用二维码的理由:凭据以图形出现,不进 shell history、不经剪贴板、
// 不经聊天软件。两样都打等于「加了把锁,钥匙插在上面」。
func TestQRShareNeverAlsoPrintsTheRawLink(t *testing.T) {
	out, err := renderPhoneShare(phoneShareOutput{Link: fakeVless, QR: true}, tinyEncode)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, fakeVless) {
		t.Errorf("二维码旁边把链接原文也打出来了:\n%s", out)
	}
	if strings.Contains(out, "5555-555555555555") {
		t.Errorf("凭据以文本形式泄漏了:\n%s", out)
	}
}

// 打印裸链接时**必须警告** —— 它自带凭据,而且刚刚进了 shell 历史。
// 既有的 rawLinkRisk 对 `bx setup <裸链接>` 已经这么做了;这条路是同一件事。
func TestRawLinkShareWarnsAboutCredentials(t *testing.T) {
	out, err := renderPhoneShare(phoneShareOutput{Link: fakeVless}, tinyEncode)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, fakeVless) {
		t.Fatalf("--format link 没打出链接:\n%s", out)
	}
	if !strings.Contains(out, "shell 历史") {
		t.Errorf("打了裸凭据却没警告:\n%s", out)
	}
}

// 没有链接时**说没有,不画一张空码**。
// 一张空白的二维码看起来像「画出来了、只是扫不出来」,会让人去查扫描器。
func TestPhoneShareRefusesWithoutALink(t *testing.T) {
	if _, err := renderPhoneShare(phoneShareOutput{QR: true}, tinyEncode); err == nil {
		t.Error("没有链接却成功了")
	}
}

// 编码失败要**如实报错**,不能悄悄退化成一张画不出来的图。
func TestPhoneShareReportsAnEncodeFailure(t *testing.T) {
	boom := func(string) (int, func(x, y int) bool, error) { return 0, nil, errors.New("too long") }
	if _, err := renderPhoneShare(phoneShareOutput{Link: fakeVless, QR: true}, boom); err == nil {
		t.Error("编码失败却没报错")
	}
}

// 非反色时必须给出「扫不出来怎么办」的出路。
//
// 默认极性对**浅色终端**是对的;深色终端上是反的,而现代扫描器多数认得反色
// 但不是全部 —— 一张扫不出来的码与没有这个功能完全一样,所以出路要写在旁边。
func TestQRSharePointsAtTheInvertEscapeHatch(t *testing.T) {
	out, _ := renderPhoneShare(phoneShareOutput{Link: fakeVless, QR: true}, tinyEncode)
	if !strings.Contains(out, "--qr-invert") {
		t.Errorf("没给深色终端的出路:\n%s", out)
	}
	inverted, _ := renderPhoneShare(phoneShareOutput{Link: fakeVless, QR: true, Invert: true}, tinyEncode)
	if strings.Contains(inverted, "--qr-invert") {
		t.Errorf("已经反色了还叫人加 --qr-invert:\n%s", inverted)
	}
}

// UDP 那条**刻意不画第二张码**:手机客户端一次导入一条,两张码会让人以为
// 两张都得扫。但它存在这件事要说出来,否则用户不知道自己少拿了一个加速档。
func TestQRShareMentionsButDoesNotDrawTheUDPLink(t *testing.T) {
	udp := "hysteria2://pw@203.0.113.9:443#alice-udp"
	out, err := renderPhoneShare(phoneShareOutput{Link: fakeVless, UDPLink: udp, QR: true}, tinyEncode)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, udp) {
		t.Errorf("第二条链接的原文被打出来了:\n%s", out)
	}
	if !strings.Contains(out, "hysteria2") {
		t.Errorf("没提第二条链接的存在,用户不知道自己少拿了加速档:\n%s", out)
	}
}

// --format link 时两条链接都要给 —— 那条路本来就是给「我要原文」的人。
func TestRawLinkShareGivesBothLinks(t *testing.T) {
	udp := "hysteria2://pw@203.0.113.9:443#alice-udp"
	out, _ := renderPhoneShare(phoneShareOutput{Link: fakeVless, UDPLink: udp}, tinyEncode)
	if !strings.Contains(out, udp) {
		t.Errorf("--format link 少给了 UDP 那条:\n%s", out)
	}
}

// —— 重新取出一个已有 share ——
//
// 在此之前链接只在**创建的那一刻**打出来一次:要么当场扫,要么这张码就没了,
// 找回来只能再 share 一个新用户(多一个 uuid、重启一次 server)。而「当场扫」
// 这个前提经常不成立 —— 在 SSH 里敲完命令时手机不一定在手上。

func replayCtx(t *testing.T, args []string, flags map[string]any) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("shares", flag.ContinueOnError)
	set.String("format", "", "")
	set.Bool("qr", false, "")
	set.Bool("qr-invert", false, "")
	set.Bool("json", false, "")
	for k, v := range flags {
		switch val := v.(type) {
		case string:
			if err := set.Set(k, val); err != nil {
				t.Fatal(err)
			}
		case bool:
			if val {
				if err := set.Set(k, "true"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatal(err)
	}
	return cli.NewContext(cli.NewApp(), set, nil)
}

var fixtureShares = []shareInfo{
	{Name: "alice", Config: serverConfig{Type: "reality", Link: fakeVless}},
	{Name: "bob", Config: serverConfig{Type: "reality", Link: "vless://bbbb@203.0.113.9:443#bob"}},
}

// 不给名字时不许把**所有** share 的凭据一次全打出来。
// 一条命令泄漏全部钥匙,而用户想要的几乎总是其中一个。
func TestReplayShareRefusesToDumpEveryCredential(t *testing.T) {
	_, _, err := replayShare(replayCtx(t, nil, map[string]any{"qr": true}), fixtureShares)
	if err == nil {
		t.Fatal("没给名字却成功了")
	}
	if !strings.Contains(err.Error(), "哪个") {
		t.Errorf("错误里没说该给名字:%v", err)
	}
}

// **--json 与 --format link 是矛盾指令,不许静默挑一个。**
// 前者刻意脱敏,后者刻意打凭据原文;悄悄执行其中一个,用户不会知道自己拿到的
// 是哪一种,而这两种的处置完全不同。
func TestReplayShareRejectsRedactedAndRawAtTheSameTime(t *testing.T) {
	_, _, err := replayShare(replayCtx(t, []string{"alice"}, map[string]any{"json": true, "format": "link"}), fixtureShares)
	if err == nil {
		t.Fatal("--json 与 --format link 同时给却成功了 —— 用户不会知道拿到的是哪一种")
	}
}

// 名字不存在要**如实报错**,不能安静地什么都不打(那看起来像成功了)。
func TestReplayShareReportsAnUnknownName(t *testing.T) {
	_, _, err := replayShare(replayCtx(t, []string{"carol"}, map[string]any{"qr": true}), fixtureShares)
	if err == nil {
		t.Fatal("不存在的名字却成功了")
	}
	if !strings.Contains(err.Error(), "carol") {
		t.Errorf("错误里没点名是哪个:%v", err)
	}
}

// 取的必须是**那一个**,不是恰好第一个。
func TestReplayShareReturnsTheNamedOne(t *testing.T) {
	out, ok, err := replayShare(replayCtx(t, []string{"bob"}, map[string]any{"format": "link"}), fixtureShares)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(out, "#bob") {
		t.Errorf("取到的不是 bob:\n%s", out)
	}
	if strings.Contains(out, fakeVless) {
		t.Errorf("把 alice 的凭据也打出来了:\n%s", out)
	}
}

// 不加 flag 时**一个字节都不变** —— 列表照旧。
func TestReplayShareStaysOutOfTheWayByDefault(t *testing.T) {
	_, ok, err := replayShare(replayCtx(t, []string{"alice"}, nil), fixtureShares)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("没要二维码/裸链接却接管了输出")
	}
}
