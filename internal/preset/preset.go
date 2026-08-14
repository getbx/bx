// Package preset 是「一组一起打开或关掉的直连域名」这张表。
//
// **它是叶子包,不做 I/O。** 挪出 internal/cli 的理由:菜单要按组显示与开关规则,
// 而菜单经 Guardian 说话 —— 两边必须读**同一份**清单。复制第二份的后果是它们
// 会漂,而漂开时菜单说「Apple 组已开」而 Core 手里的域名少了两条,谁也不会发现。
package preset

import (
	"sort"
	"strings"
)

// Preset 是一组语义上属于同一件事的直连域名。
type Preset struct {
	Name string
	// Summary 是给人看的一句话。
	Summary string
	// Title 是界面上那个**标签**的名字。
	//
	// **短到能当标签用。** 上一版是「Steam 下载(游戏本体 / 更新 / 云存档)」——
	// 那不是标签,是一句说明。界面里一整列这样的标题会让开关面板读起来像说明书。
	// 解释归 Summary(`bx preset show` 用),界面不显示它。
	Title  string
	Direct []string
	// Retired 是这一组**曾经装过、现在不再装**的域名。
	//
	// preset 会演进,而演进不许把用户配置里的旧域名变成孤儿:若 Classify 不再认
	// 它们,它们就会掉进「自定义」,而界面对自定义规则只显示不给开关(那是用户
	// 手写的,菜单没资格删)—— 于是一条我们主动废弃的规则会静默留在那里继续把
	// 流量送错方向,谁也删不掉。
	//
	// 语义:**enable 不装它,disable 仍然清它,Classify 仍然认它属于本组。**
	Retired []string
}

// AllDomains 是这一组认领的全部域名(在用的 + 退役的)。
func (p Preset) AllDomains() []string {
	out := make([]string, 0, len(p.Direct)+len(p.Retired))
	out = append(out, p.Direct...)
	out = append(out, p.Retired...)
	return out
}

var all = map[string]Preset{
	"gaming": {
		Title: "Steam",
		Name:  "gaming",
		Summary: "只含**纯字节**:游戏文件、客户端更新、云存档/创意工坊。" +
			"商店与社区页面**刻意不在其中** —— 它们的 HTML 走隧道,图片若走直连," +
			"同一个页面就会一半美国一半本地,区域判定与图片加载各自为政" +
			"(2026-08-13 真机上「商店图片全裂」正是这个机制)。",
		Direct: []string{
			"*.steamcontent.com",                   // 游戏本体(SteamPipe)
			"steamcdn-a.akamaihd.net",              // depot / 旧版内容 CDN
			"client-update.akamai.steamstatic.com", // Steam 客户端更新包
			"*.steamusercontent.com",               // 云存档 / 创意工坊 / 截图
		},
		// 曾经装过,现在明确不再走直连 —— 它们属于「页面」而不是「字节」。
		Retired: []string{
			"*.steamstatic.com",      // 商店/库/社区图,与 store 页面同属一页
			"media.steampowered.com", // 预告片,嵌在商店页里
		},
	},
	"apple": {
		Title:   "Apple",
		Name:    "apple",
		Summary: "Apple 系统服务、Game Center、Arcade、iCloud 同步可用性",
		Direct: []string{
			"*.apple.com",
			"*.icloud.com",
			"*.icloud-content.com",
			"*.mzstatic.com",
			"*.aaplimg.com",
			"*.cdn-apple.com",
		},
	},
	"china-cdn": {
		Title:   "China CDN",
		Name:    "china-cdn",
		Summary: "国内 App/视频/电商 CDN 可用性;只含品牌自控域",
		Direct: []string{
			"*.alicdn.com",
			"*.taobao.com",
			"*.tmall.com",
			"*.jd.com",
			"*.bilibili.com",
			"*.bilivideo.com",
			"*.douyin.com",
			"*.douyinpic.com",
			"*.byteimg.com",
			"*.weixin.qq.com",
			"*.qq.com",
		},
	},
}

// All 返回全部内置 preset,按名字稳定排序。
func All() []Preset {
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Preset, 0, len(names))
	for _, name := range names {
		out = append(out, all[name])
	}
	return out
}

// Lookup 按名字取一个 preset。
func Lookup(name string) (Preset, bool) {
	p, ok := all[name]
	return p, ok
}

// Classify 把配置里的直连域名分到各组,并把**不属于任何组的**单独列出。
//
// **「自定义」那一半是这个函数存在的理由。** 用户手写的域名与 preset 装进去的
// 混在同一个列表里,而组开关**绝不能碰**前者 —— 菜单没有资格替用户删他自己
// 写的东西。分不出来就只能一起删,那是不可接受的。
func Classify(directRules []string) (grouped map[string][]string, custom []string) {
	owner := map[string]string{}
	for _, p := range All() {
		// **认领范围含退役域名**:否则我们主动废弃的规则会掉进「自定义」,
		// 而界面对自定义规则只显示不给开关 —— 它将永远删不掉。
		for _, domain := range p.AllDomains() {
			owner[normalize(domain)] = p.Name
		}
	}
	grouped = map[string][]string{}
	for _, rule := range directRules {
		if name, ok := owner[normalize(rule)]; ok {
			grouped[name] = append(grouped[name], rule)
			continue
		}
		custom = append(custom, rule)
	}
	return grouped, custom
}

func normalize(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}
