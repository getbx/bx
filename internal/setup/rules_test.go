package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

// 用户真机上那份配置的形状(2026-08-13 现场取的),包含手写注释与分组 ——
// **改规则不许把这些冲掉**,这正是 UpdateTransports 当初做外科手术的理由。
// 服务器链接是 bx blink 生成的**合法但虚构**的一条(203.0.113.10,TEST-NET-3)——
// config.Parse 会校验它,而把真实 VPS 写进仓库是另一回事。
const liveConfigShape = `server: bx://eyJ2IjoxLCJ0cmFuc3BvcnQiOiJyZWFsaXR5IiwibGluayI6InZsZXNzOi8vMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1QDIwMy4wLjExMy4xMDo0NDM_c2VjdXJpdHk9cmVhbGl0eVx1MDAyNnBiaz1BQUFBXHUwMDI2c25pPXd3dy5jbG91ZGZsYXJlLmNvbVx1MDAyNmZwPWNocm9tZSJ9
global: true
killswitch: true
owner_uid: 501
udp:
    transport: bx://eyJ2IjoxLCJ0cmFuc3BvcnQiOiJyZWFsaXR5IiwibGluayI6InZsZXNzOi8vMTExMTExMTEtMjIyMi0zMzMzLTQ0NDQtNTU1NTU1NTU1NTU1QDIwMy4wLjExMy4xMDo0NDM_c2VjdXJpdHk9cmVhbGl0eVx1MDAyNnBiaz1BQUFBXHUwMDI2c25pPXd3dy5jbG91ZGZsYXJlLmNvbVx1MDAyNmZwPWNocm9tZSJ9
rules:
    - direct:
        # 苹果服务:走隧道会掉登录态
        - '*.icloud.com'
        - gsa.apple.com
        - '*.steamstatic.com'
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// **删掉一条死规则是这整件事的主用例。** 排查里点名了 `'*.steamstatic.com'`,
// 用户要做的就是把它拿掉 —— 而其余每一行必须原样留着。
func TestRemoveRuleKeepsEverythingElse(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	if err := RemoveRule(path, "direct", "*.steamstatic.com"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	body := string(out)
	if strings.Contains(body, "steamstatic") {
		t.Errorf("规则没删掉:\n%s", body)
	}
	for _, keep := range []string{"'*.icloud.com'", "gsa.apple.com", "killswitch: true", "owner_uid: 501"} {
		if !strings.Contains(body, keep) {
			t.Errorf("改规则冲掉了无关内容 %q:\n%s", keep, body)
		}
	}
	// **注释必须活下来。** 整份重写会把它冲掉,而那正是 2026-08-06 真机事故的形状。
	if !strings.Contains(body, "苹果服务:走隧道会掉登录态") {
		t.Errorf("用户手写的注释被冲掉了:\n%s", body)
	}
}

func TestAddRuleAppendsToTheRightList(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	if err := AddRule(path, "direct", "*.qq.com"); err != nil {
		t.Fatal(err)
	}
	rules, err := ListRules(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(rules.Direct, "*.qq.com") || !contains(rules.Direct, "gsa.apple.com") {
		t.Fatalf("加规则后列表 = %+v", rules)
	}
}

// **重复添加不许把同一条写两遍。** 用户会重复点,而两条一样的规则除了让
// 归因计数分裂之外没有任何效果。
func TestAddRuleIsIdempotent(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	for i := 0; i < 3; i++ {
		if err := AddRule(path, "direct", "*.qq.com"); err != nil {
			t.Fatal(err)
		}
	}
	rules, _ := ListRules(path)
	n := 0
	for _, r := range rules.Direct {
		if r == "*.qq.com" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("重复添加写进去 %d 条", n)
	}
}

// 配置里还没有 rules 段时要能创建出来 —— 全新安装就是这个状态。
func TestAddRuleCreatesTheSectionWhenAbsent(t *testing.T) {
	path := writeTemp(t, "server: bx://abc\nglobal: true\n")
	if err := AddRule(path, "proxy", "*.example.com"); err != nil {
		t.Fatal(err)
	}
	rules, err := ListRules(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(rules.Proxy, "*.example.com") {
		t.Fatalf("没建出 rules 段:%+v", rules)
	}
}

// **删一条不存在的规则要如实报错,不能静默成功。** 静默成功会让菜单显示
// 「已删除」而配置一个字没变 —— 用户重启一次网络,然后发现问题还在。
func TestRemoveUnknownRuleReportsInsteadOfSilentlySucceeding(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	if err := RemoveRule(path, "direct", "*.never-existed.com"); err == nil {
		t.Fatal("删一条不存在的规则报告了成功")
	}
}

// **非法输入必须在写盘之前挡掉。** 一条带空格或引号的「域名」进了 config,
// 下次 bx up 才会发现,而那时用户已经断过一次网了。
func TestRejectsMalformedPatternsBeforeTouchingDisk(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	before, _ := os.ReadFile(path)
	for _, bad := range []string{"", "  ", "has space.com", "a\nb.com", "*.", "*", "-", "a..b.com"} {
		if err := AddRule(path, "direct", bad); err == nil {
			t.Errorf("接受了非法规则 %q", bad)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("非法输入动了盘上的文件")
	}
}

// kind 只有 direct / proxy 两种。写错一个会静默落到某个不存在的列表里。
func TestRejectsUnknownKind(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	if err := AddRule(path, "blocked", "a.com"); err == nil {
		t.Fatal("接受了不存在的规则类型")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// **改完的配置必须还能被 bx 自己读回来。** 这条比「yaml 能解析」强:
// 它走的是生产用的那个 config.Load,同一个 schema、同一个校验。
//
// 具体防的是一个不显眼的坑:带 `*` 的值在 YAML 里不加引号就是**别名语法**,
// 写出去的配置读回来直接报解析错误 —— 而 bx 的规则里绝大多数都带 `*`。
func TestEditedConfigStillLoadsWithBXsOwnParser(t *testing.T) {
	path := writeTemp(t, liveConfigShape)
	if err := AddRule(path, "direct", "*.qq.com"); err != nil {
		t.Fatal(err)
	}
	if err := AddRule(path, "proxy", "*.blocked-site.com"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRule(path, "direct", "*.steamstatic.com"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		body, _ := os.ReadFile(path)
		t.Fatalf("bx 自己读不回改后的配置:%v\n%s", err, body)
	}
	var direct, proxy []string
	for _, r := range cfg.Rules {
		direct = append(direct, r.Direct...)
		proxy = append(proxy, r.Proxy...)
	}
	if !contains(direct, "*.qq.com") || contains(direct, "*.steamstatic.com") || !contains(proxy, "*.blocked-site.com") {
		t.Fatalf("规则没落到 bx 看得见的地方:direct=%v proxy=%v", direct, proxy)
	}
	// 无关字段一个都不许丢。
	if !cfg.Global || cfg.OwnerUID != 501 {
		t.Errorf("改规则动了无关配置:global=%v owner_uid=%v", cfg.Global, cfg.OwnerUID)
	}
}
