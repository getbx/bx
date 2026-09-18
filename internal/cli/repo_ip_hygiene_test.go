package cli

import (
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 仓库里不许出现**项目自己的**服务器地址。
//
// 2026-09-18 起因:离屏快照把 `Exit IP: <项目所有者真实的 VPS>` 画在了图上,而它
// 来自提交进仓库的 fixture。往回一查,同一个地址散在 **17 个文件**里 —— 生产
// Go/Swift 源码的注释、Swift 测试、两份 spec、一份 plan、CLAUDE.md 自己;此外还有
// 另外几台测试 VPS、以及项目所有者**家里** derper 的地址。全是记述真机事故时顺手
// 贴的。
//
// **风险不在「IP 被知道」**(那台机器本来就在 443 上对外听,REALITY 的设计就是让它
// 看起来像普通 TLS 站)—— **在「这个公开仓库 ↔ 这台机器是翻墙出口」这条可被爬取的
// 关联**。对一个翻墙工具,这条关联比地址本身值钱。
//
// 判据是**白名单**而不是黑名单:黑名单只拦得住已经知道的那几个,而下一个人粘贴的
// 是一个新的。任何公网地址要么落在文档保留网段(RFC5737)/私网/特殊段里,要么必须
// 在下面这张表里**写明理由**。新加一条的动作本身就是那句要被逼着回答的话:
// 「这是第三方服务,还是我们自己的机器?」
//
// 已经进了 git 历史的那些,改现有文件是拿不掉的 —— 这条守卫管的是**停止扩散**。
func TestNoRealInfrastructureAddressesInTheRepo(t *testing.T) {
	out, err := exec.Command("git", "-C", filepath.Join("..", ".."), "ls-files").Output()
	if err != nil {
		t.Fatalf("列不出仓库文件:%v —— 这条守卫读不懂现在的仓库了,先修它", err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("一个文件都没扫到 —— 安静地扫了零个的守卫,与没有这条守卫完全一样")
	}

	seen := map[string]bool{}
	scanned := 0
	for _, rel := range files {
		// 内嵌的 china 列表是**真实世界数据**,不是我们的机器。
		if strings.HasPrefix(rel, "internal/embedded/assets/") {
			continue
		}
		raw, readErr := readRepoTextFile(filepath.Join("..", "..", rel))
		if readErr != nil {
			continue // 二进制/读不动的跳过
		}
		scanned++
		for _, literal := range ipv4Literal.FindAllString(raw, -1) {
			addr, parseErr := netip.ParseAddr(literal)
			if parseErr != nil || addressIsSafeForAFixture(addr) {
				continue
			}
			seen[literal] = true
			if _, ok := knownPublicAddresses[literal]; !ok {
				t.Errorf("%s 里有一个不在白名单上的公网地址 %s。\n"+
					"  如果它是我们自己的机器:换成文档保留网段(203.0.113.x 给 bx 服务器,"+
					"192.0.2.x 给别的真实主机)。\n"+
					"  如果它是 bx 有意点名的第三方服务(公共 DNS、DERP、ZeroTier root):"+
					"加进 knownPublicAddresses 并写明它是什么。", rel, literal)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("一个文本文件都没读进来 —— 判据认不出这个仓库的形状了")
	}

	// 反向:白名单里不许留指向已经不存在的地址的陈旧条目 —— 陈旧条目看起来与
	// 生效中的一模一样而什么也不守。
	var stale []string
	for addr := range knownPublicAddresses {
		if !seen[addr] {
			stale = append(stale, addr)
		}
	}
	sort.Strings(stale)
	for _, addr := range stale {
		t.Errorf("白名单里的 %s 在仓库里已经找不到了 —— 删掉它,别留一条什么也不守的条目", addr)
	}
}

// knownPublicAddresses:仓库里**允许**出现的公网地址,每一条都要说清它是什么。
//
// 分四类:bx 有意点名的公共服务、判据里作为边界的真实地址、第三方 relay 基础设施、
// 以及约定俗成的占位符。**不包括任何我们自己的机器** —— 那是这条守卫存在的理由。
var knownPublicAddresses = map[string]string{
	// 公共 DNS / 探测目标:bx 的代码与文档按名字点它们。
	"1.1.1.1":         "Cloudflare DNS,bx 的默认探测目标",
	"8.8.8.8":         "Google DNS",
	"9.9.9.9":         "Quad9 DNS",
	"223.5.5.5":       "AliDNS,split 模式的国内上游",
	"223.5.5.0":       "AliDNS 所在 /24,CIDR 判据测试用",
	"114.114.114.114": "114DNS",
	"114.114.114.0":   "114DNS 所在 /24,CIDR 判据测试用",
	"114.114.114.5":   "114DNS 同段,CIDR 判据测试用",
	"129.1.1.1":       "observe 的第二个捕获探针(与 1.1.1.1 一起判劫持是否生效)",

	// 判据里作为**边界**的真实地址:换成文档网段就测不出它要测的东西。
	"110.242.68.66": "一个真实的中国大陆 IP —— pathview 要判「在不在 china 列表里」",
	"104.0.0.0":     "一个够宽的公网 /8 —— leakcheck 判 TunnelVision 要的正是「比单主机更宽」",
	"64.0.0.0":      "屏障四条 /2 之一的网络地址",
	"128.0.0.0":     "屏障四条 /2 之一的网络地址;也是 split-default 的 128.0.0.0/1",
	"172.15.0.1":    "RFC1918 下边界外一格,私网判据的边界用例",
	"172.32.0.1":    "RFC1918 上边界外一格,私网判据的边界用例",
	"100.63.0.1":    "CGNAT 下边界外一格",
	"100.128.0.1":   "CGNAT 上边界外一格",
	"1.0.0.0":       "china CIDR 判据的边界用例",

	// 第三方 relay 基础设施:bx **功能上**要把它们旁路掉,所以地址必须是真的。
	// 上游改了这几个地址时这条守卫会红一次 —— 那正是「我们又硬编了一个真实 IP」
	// 该被看见的时刻。
	"64.225.56.166":   "Tailscale DERP 兜底",
	"68.183.90.120":   "Tailscale DERP 兜底",
	"134.122.94.167":  "Tailscale DERP 兜底",
	"144.202.67.195":  "Tailscale DERP 兜底",
	"149.28.119.105":  "Tailscale DERP 兜底",
	"165.22.33.71":    "Tailscale DERP 兜底",
	"178.62.44.132":   "Tailscale DERP 兜底",
	"192.73.252.134":  "Tailscale DERP 兜底",
	"208.111.34.178":  "Tailscale DERP 兜底",
	"216.128.144.130": "Tailscale DERP 兜底",
	"192.200.0.101":   "Tailscale controlplane 兜底段的下端(生产由 tailscaleControlplaneFallbackCIDRs 生成 101..116)",
	"192.200.0.116":   "Tailscale controlplane 兜底段的上端",
	"104.194.8.134":   "ZeroTier root",
	"103.195.103.66":  "ZeroTier root",
	"50.7.252.138":    "ZeroTier root",
	"84.17.53.155":    "ZeroTier root",
	"107.170.197.14":  "ZeroTier root",

	// 约定俗成的占位符。它们在技术上是被分配的地址,但没有人会把它们读成
	// 「某台真机」—— 而这条守卫要拦的正是「某台真机」。
	"1.2.3.4": "占位符",
	"1.2.3.0": "占位符所在 /24",
	"1.2.0.0": "占位符所在 /16",
	"1.3.0.1": "占位符",
	"2.2.2.2": "占位符",
	"3.3.3.3": "占位符",
	"4.4.4.4": "占位符",
	"5.6.7.8": "占位符",
	"7.7.7.7": "占位符",
}

var errSkipBinary = errors.New("binary or skipped file")

var osReadFile = os.ReadFile

var repoTextSkip = regexp.MustCompile(`\.(png|jpg|jpeg|gif|icns|syso|gz|zip|dmg|pdf)$`)

// readRepoTextFile 读一个文本文件;二进制与读不动的返回错误,由调用方跳过。
func readRepoTextFile(path string) (string, error) {
	if repoTextSkip.MatchString(strings.ToLower(path)) {
		return "", errSkipBinary
	}
	raw, err := osReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.IndexByte(string(raw[:min(len(raw), 4096)]), 0) >= 0 {
		return "", errSkipBinary
	}
	return string(raw), nil
}
