package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 真机事故 2026-08-06:用户 `sudo bx setup <新服务器链接>`,看到「✅ 服务器连通
// 366ms」以为切好了,实际配置压根没写(旧逻辑:配置已存在就拒绝),随后 bx up
// 用旧配置起来、显示 Protected —— 用户完全有理由相信自己在新服务器上,实际不是。
//
// 而唯一能绕过拒绝的 --force 会整份重写配置,把用户手写的 apple/steam 直连策略
// 一起冲掉。所以需要一条「只换传输、其余一个字不动」的路。
const configWithHandWrittenPolicy = `# bx 客户端配置(手写过,注释必须活下来)
server: bx://OLD-MAIN
killswitch: true
owner_uid: 501
udp:
  mode: proxy
  transport: bx://OLD-UDP
rules:
  - direct:
      # iCloud 走隧道会出问题
      - '*.icloud.com'
      - gsa.apple.com
  - proxy:
      - '*.openai.com'
lists:
  china_domain: /var/lib/bx/china_domain.txt
`

func TestUpdateTransportsPreservesEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(configWithHandWrittenPolicy), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := UpdateTransports(path, []string{"bx://NEW-MAIN"}, "bx://NEW-UDP")
	if err != nil {
		t.Fatalf("UpdateTransports 失败: %v", err)
	}
	if before.Server != "bx://OLD-MAIN" || before.UDPTransport != "bx://OLD-UDP" {
		t.Errorf("必须回报改动前的值供调用方展示,实际 = %+v", before)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)

	if !strings.Contains(text, "bx://NEW-MAIN") || !strings.Contains(text, "bx://NEW-UDP") {
		t.Errorf("新链接必须写进去,实际 =\n%s", text)
	}
	if strings.Contains(text, "OLD-MAIN") || strings.Contains(text, "OLD-UDP") {
		t.Errorf("旧链接必须被替换掉,实际 =\n%s", text)
	}

	// 这些是用户手写的,一个字都不能丢
	for _, must := range []string{
		"# bx 客户端配置(手写过,注释必须活下来)",
		"# iCloud 走隧道会出问题",
		"'*.icloud.com'",
		"gsa.apple.com",
		"'*.openai.com'",
		"china_domain: /var/lib/bx/china_domain.txt",
		"owner_uid: 501",
		"mode: proxy",
	} {
		if !strings.Contains(text, must) {
			t.Errorf("改传输不得丢掉 %q,实际 =\n%s", must, text)
		}
	}
}

// 原本没有 udp 段时要能建出来,而不是报错或静默丢弃。
func TestUpdateTransportsCreatesUDPSectionWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: bx://OLD\nkillswitch: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTransports(path, []string{"bx://NEW"}, "bx://NEW-UDP"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, "bx://NEW-UDP") {
		t.Errorf("原本没有 udp 段时也要写进去,实际 =\n%s", text)
	}
	if !strings.Contains(text, "killswitch: true") {
		t.Errorf("其余字段仍须保留,实际 =\n%s", text)
	}
}

// 不给 UDP 链接时,不得动原有的 udp.transport —— 用户只想换主传输。
func TestUpdateTransportsLeavesUDPAloneWhenNotGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: bx://OLD\nudp:\n  mode: proxy\n  transport: bx://KEEP-UDP\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTransports(path, []string{"bx://NEW"}, ""); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, "bx://KEEP-UDP") {
		t.Errorf("没给 UDP 链接就不该动它,实际 =\n%s", text)
	}
}

// 多传输(容灾列表)要写成 transports,并清掉单条 server,避免两处打架。
func TestUpdateTransportsWritesMultiTransportList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server: bx://OLD\nkillswitch: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateTransports(path, []string{"bx://A", "bx://B"}, ""); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, "bx://A") || !strings.Contains(text, "bx://B") {
		t.Errorf("两条传输都要写进去,实际 =\n%s", text)
	}
	if strings.Contains(text, "bx://OLD") {
		t.Errorf("旧的单条 server 必须被替换,实际 =\n%s", text)
	}
}

// 配置文件不存在时必须明确报错,不能悄悄建一个半成品。
func TestUpdateTransportsRefusesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := UpdateTransports(path, []string{"bx://NEW"}, ""); err == nil {
		t.Error("配置不存在时必须报错——那条路该走全新写入,不是就地改")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
