package cli

import (
	"encoding/json"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/getbx/bx/internal/preset"
)

// 离屏快照的输入必须只装**生产真的会产出**的东西。
//
// 它的产物是一张图,而那张图会被当成「用户看到的样子」的证据 —— 我自己就照着它
// 报过一个不存在的缺陷(CLAUDE.md 里那条「用假例子撑着的记述」)。2026-09-18 一次
// 复核当场抓到三处,每一处都会让人看着图得出错的结论:
//
//   - `servers.json` 里躺着**项目所有者真实的 VPS 地址**,而这是公开仓库,
//     而本仓库自己把服务器 IP 当敏感信息(Guardian 日志强制 0600 的理由原文就是
//     「里面有服务器 IP 与 bypass 网段」);
//   - `rules.json` 的分组 name 写的是 `steam`,而生产里那一组叫 `gaming`
//     (它的 **Title** 才是 Steam)—— 快照因此渲染出「认不出的组」那条回落副标题,
//     而真实用户永远看不到那个画面;
//   - `doctor.json` / `logs.json` 里是几句**编造的中文**,而那几个面这一轮已经
//     全改英文 —— 快照把一个**已经修好**的问题画得还在。
//
// **一个会画出产品不存在状态的快照工具,比没有这个工具更坏:它看起来是证据。**
//
// **扫整个 Snapshots/,不只是 fixtures/。** 第一版只扫 JSON —— 而快照驱动
// `Snapshots/main.swift` 里还内联着一份同样的数据(探测结果里的出口 IP),于是
// 修完 fixture 再跑一次,真实地址**照样画在图上**。守卫钉住了缺陷旁边的东西,
// 那是本仓库列过的第一种失效写法,这次当场自己犯了一遍。
func TestSnapshotFixturesOnlyContainThingsProductionCanProduce(t *testing.T) {
	root := filepath.Join("..", "..", "apps", "macos", "BxMenu", "Snapshots")
	var inputs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && (strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".swift")) {
			inputs = append(inputs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走不进快照目录 %s —— 这条守卫失去意义,必须响亮失败:%v", root, err)
	}
	if len(inputs) == 0 {
		t.Fatal("一个快照输入都没扫到 —— 安静地扫了零个的守卫,与没有这条守卫完全一样")
	}

	sawJSON := false
	for _, path := range inputs {
		name := filepath.Base(path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s: %v", path, err)
		}
		body := string(raw)

		// ① 只许出现文档保留网段 / 私网 / 回环 / bx 自己的假 IP。**两种文件都查** ——
		// 真实地址在 JSON 与 Swift 驱动里各有一份。
		for _, literal := range ipv4Literal.FindAllString(body, -1) {
			addr, parseErr := netip.ParseAddr(literal)
			if parseErr != nil {
				continue
			}
			if !addressIsSafeForAFixture(addr) {
				t.Errorf("%s 里有一个真实的公网地址 %s —— 这是公开仓库,而本仓库把"+
					"服务器 IP 当敏感信息(Guardian 日志 0600 的理由)", name, literal)
			}
		}

		// ② 这几个面全是英文;出现 CJK 说明 fixture 停在了产品的某个旧版本上。
		// **只查 JSON**:Swift 驱动里的中文是注释,那是正常的。
		if !strings.HasSuffix(path, ".json") {
			continue
		}
		sawJSON = true
		for _, r := range body {
			if unicode.Is(unicode.Han, r) {
				t.Errorf("%s 里有中文(%q)—— 这几个面已经全改英文,fixture 会把一个"+
					"已经修好的问题画得还在", name, string(r))
				break
			}
		}
	}
	if !sawJSON {
		t.Fatal("一份 JSON fixture 都没扫到 —— 判据认不出这个目录的形状了")
	}

	// ③ rules.json 的分组 name 必须是真的 preset name。
	raw, err := os.ReadFile(filepath.Join(root, "fixtures", "rules.json"))
	if err != nil {
		t.Fatalf("读 rules.json: %v", err)
	}
	var rules struct {
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatalf("解 rules.json: %v", err)
	}
	if len(rules.Groups) == 0 {
		t.Fatal("rules.json 里一个分组都没有 —— 守卫读不懂它了")
	}
	for _, group := range rules.Groups {
		if _, ok := preset.Lookup(group.Name); !ok {
			t.Errorf("rules.json 的分组 name %q 不是生产里的 preset —— 快照会画出"+
				"「认不出的组」那条回落副标题,而真实用户永远看不到那个画面", group.Name)
		}
	}
}

var ipv4Literal = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)

// addressIsSafeForAFixture:文档保留网段(RFC5737)、私网、回环、链路本地、
// CGNAT,以及 bx 自己的假 IP 段(198.18/15,fixture 里会出现)。
func addressIsSafeForAFixture(addr netip.Addr) bool {
	// 组播与保留段按定义不是谁的机器(mDNS 的 224.0.0.251 就落在这里)。
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalMulticast() {
		return true
	}
	for _, cidr := range []string{
		"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", // RFC5737 文档用
		"198.18.0.0/15", // bx 的 fake-IP
		"100.64.0.0/10", // CGNAT / tailscale
		"240.0.0.0/4",   // 保留段
		"192.0.0.0/24",  // IETF 协议分配
	} {
		if netip.MustParsePrefix(cidr).Contains(addr) {
			return true
		}
	}
	return false
}
