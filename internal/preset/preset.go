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
	// Title 是界面上那一行的名字(比 Name 好读)。
	Title  string
	Direct []string
}

var all = map[string]Preset{
	"gaming": {
		Title:   "Steam / 游戏",
		Name:    "gaming",
		Summary: "游戏更新/CDN 可用性;当前聚焦 Steam 更新与下载 CDN",
		Direct: []string{
			"client-update.akamai.steamstatic.com",
			"steamcdn-a.akamaihd.net",
			"media.steampowered.com",
			"*.steamcontent.com",
			"*.steamstatic.com",
		},
	},
	"apple": {
		Title:   "Apple 服务",
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
		Title:   "国内 App / CDN",
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
		for _, domain := range p.Direct {
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
