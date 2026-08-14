package preset

import (
	"sort"
	"strings"
	"testing"
)

// **分组要能认出用户配置里那些域名属于哪一组,而认不出的必须单独留着。**
//
// 「自定义」那一半是 Classify 存在的理由:用户手写的域名与 preset 装进去的混在
// 同一个 direct 列表里,而组开关**绝不能碰**前者 —— 菜单没有资格替用户删掉他
// 自己写的东西。分不出来就只能一起删,那是不可接受的。
func TestClassifySeparatesPresetDomainsFromHandWrittenOnes(t *testing.T) {
	// 项目所有者真机上那份配置的形状(2026-08-13)。
	rules := []string{
		"*.icloud.com", "*.icloud-content.com", // apple 组
		"*.qq.com",                                // china-cdn 组
		"*.steamstatic.com", "*.steamcontent.com", // gaming 组
		"gsa.apple.com", "idmsa.apple.com", // 手写的,不在任何 preset 里
	}
	grouped, custom := Classify(rules)

	if got := grouped["apple"]; len(got) != 2 {
		t.Errorf("apple 组 = %v,want 2 条", got)
	}
	if got := grouped["china-cdn"]; len(got) != 1 || got[0] != "*.qq.com" {
		t.Errorf("china-cdn 组 = %v", got)
	}
	if got := grouped["gaming"]; len(got) != 2 {
		t.Errorf("gaming 组 = %v", got)
	}
	sort.Strings(custom)
	if len(custom) != 2 || custom[0] != "gsa.apple.com" || custom[1] != "idmsa.apple.com" {
		t.Fatalf("自定义 = %v,want [gsa.apple.com idmsa.apple.com] —— "+
			"认错一条,组开关就会删掉用户手写的规则", custom)
	}
}

// 大小写与空白不该影响归类:配置是人手写的。
func TestClassifyIsCaseAndWhitespaceInsensitive(t *testing.T) {
	grouped, custom := Classify([]string{"  *.ICloud.com  "})
	if len(grouped["apple"]) != 1 || len(custom) != 0 {
		t.Fatalf("grouped=%v custom=%v", grouped, custom)
	}
}

// 空输入不该产出空组(界面会画出一堆没意义的行)。
func TestClassifyOnEmptyInput(t *testing.T) {
	grouped, custom := Classify(nil)
	if len(grouped) != 0 || len(custom) != 0 {
		t.Fatalf("grouped=%v custom=%v", grouped, custom)
	}
}

// **每个 preset 都要有给人看的标题。** 界面上显示 `china-cdn` 这种内部名
// 对普通用户毫无意义,而这正是这一版要解决的问题。
func TestEveryPresetHasAHumanTitle(t *testing.T) {
	for _, p := range All() {
		if strings.TrimSpace(p.Title) == "" {
			t.Errorf("preset %q 没有标题", p.Name)
		}
		if strings.TrimSpace(p.Summary) == "" {
			t.Errorf("preset %q 没有说明", p.Name)
		}
		if len(p.Direct) == 0 {
			t.Errorf("preset %q 是空的", p.Name)
		}
	}
}

// 顺序必须稳定 —— 界面每次打开顺序都变会让人以为内容变了。
func TestAllIsStablySorted(t *testing.T) {
	first := All()
	for i := 0; i < 5; i++ {
		for j, p := range All() {
			if p.Name != first[j].Name {
				t.Fatalf("顺序不稳定")
			}
		}
	}
}

// **一个域名不许同时属于两个组。** 否则开关一组会连带影响另一组,
// 而界面上那两个勾选框会互相打架。
func TestNoDomainBelongsToTwoPresets(t *testing.T) {
	owner := map[string]string{}
	for _, p := range All() {
		for _, d := range p.Direct {
			key := strings.ToLower(d)
			if prev, dup := owner[key]; dup {
				t.Errorf("%q 同时属于 %s 与 %s —— 组开关会互相打架", d, prev, p.Name)
			}
			owner[key] = p.Name
		}
	}
}

// **preset 会演进,而演进不许把用户配置里的旧域名变成孤儿。**
//
// 真实场景(2026-08-13):`gaming` 组原本含 `*.steamstatic.com`,后来发现它把
// **商店页图片**和**游戏下载**混成一条规则 —— 同一个页面的 HTML 走隧道、图片走
// 直连,正是当初图片全裂的机制。于是把它从组里拿掉。
//
// 但用户配置里那一条**还在**。若 Classify 不再认它属于本组,它就会掉进「自定义」,
// 而界面对自定义规则**只显示不给开关**(那是用户手写的,菜单没资格删)——
// 于是一条我们主动废弃的规则,静默留在那里继续把流量送错方向,谁也删不掉。
//
// Retired 就是为此:**enable 不装它,disable 仍然清它,Classify 仍然认它是本组的。**
func TestRetiredDomainsStayOwnedByTheirPreset(t *testing.T) {
	grouped, custom := Classify([]string{"*.steamstatic.com", "media.steampowered.com", "*.steamcontent.com"})
	if len(custom) != 0 {
		t.Fatalf("退役域名掉进了自定义:%v —— 组开关将永远删不掉它们", custom)
	}
	if len(grouped["gaming"]) != 3 {
		t.Fatalf("gaming 组 = %v,want 3 条(含 2 条退役)", grouped["gaming"])
	}
}

// **打开一组不许把退役域名装回去** —— 那等于撤销掉这次演进。
func TestEnableNeverInstallsRetiredDomains(t *testing.T) {
	p, ok := Lookup("gaming")
	if !ok {
		t.Fatal("找不到 gaming")
	}
	for _, d := range p.Direct {
		for _, r := range p.Retired {
			if strings.EqualFold(d, r) {
				t.Errorf("%q 同时在 Direct 与 Retired 里", d)
			}
		}
	}
	// Steam 的商店/库/社区图必须**不在** Direct 里:它们和 store 页面同属一个页面,
	// 分走两条路会让区域判定和图片加载各自为政。
	for _, d := range p.Direct {
		if strings.Contains(strings.ToLower(d), "steamstatic.com") && strings.HasPrefix(d, "*") {
			t.Errorf("通配的 *.steamstatic.com 又回到了 Direct 里:%q —— "+
				"它把商店图片和客户端更新混成一条规则", d)
		}
	}
}

// AllDomains 是「这一组认领的全部域名」,disable 与 Classify 都用它。
func TestAllDomainsCoversBothLiveAndRetired(t *testing.T) {
	p, _ := Lookup("gaming")
	all := p.AllDomains()
	if len(all) != len(p.Direct)+len(p.Retired) {
		t.Fatalf("AllDomains = %d 条,want %d", len(all), len(p.Direct)+len(p.Retired))
	}
}

// **标题短到能当标签用。**
//
// 项目所有者的原话:「tag 类似于 github 里面那种标签感觉即可。然后解释也略多,
// 不说人话。宁愿不要解释。」上一版 gaming 的标题是
// 「Steam 下载(游戏本体 / 更新 / 云存档)」—— 那不是标签,是一句说明,
// 而一整列这样的标题会让开关面板读起来像说明书。
//
// 解释仍然留在 Summary(`bx preset show` 用),**界面不显示它**。
func TestPresetTitlesAreShortEnoughToBeTags(t *testing.T) {
	for _, p := range All() {
		runes := []rune(p.Title)
		if len(runes) > 12 {
			t.Errorf("%s 的标题当不了标签(%d 字):%q", p.Name, len(runes), p.Title)
		}
		for _, bad := range []string{"(", "(", "、", "/"} {
			if strings.Contains(p.Title, bad) {
				t.Errorf("%s 的标题里塞了说明(含 %q):%q", p.Name, bad, p.Title)
			}
		}
	}
}
