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
		"*.qq.com", // china-cdn 组
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
