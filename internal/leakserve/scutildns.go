package leakserve

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// parseSCUtilDNS 从 `scutil --dns` 的输出里取出**默认解析器**的地址。
//
// # 为什么需要它:没有它,DNS 那条规则在它唯一该工作的场景里是死的
//
// 采集层原本只用 `networksetup -getdnsservers`,而那条命令只报**手动设置**的解析器。
// 一台靠 DHCP 拿 DNS 的 Mac 上它什么都不返回 —— 于是 judgeDNS 永远是 `not checked`。
//
// 而「靠 DHCP 拿 DNS」恰恰就是这条规则的目标场景:第三方 VPN 装了默认路由却不改
// 系统 DNS(wg-quick 不写 `DNS =`、Tailscale exit node 关掉 MagicDNS、公司
// route-all VPN 保留 Wi-Fi 的解析器)。**规则在它唯一该发现问题的机器上问不出话来**
// —— 那是设计风险二的镜像:不是恒绿,是恒「没查」,而两者一样会把这一行变成装饰。
//
// # 判据:只认「未限定域」的 resolver 块
//
// 真机实测(2026-08-11,项目所有者的 Mac,bx 开着)输出里有 8 个 resolver 块:
//
//   - `resolver #1` 带 `search domain[0]` 与 `nameserver[0]`,**没有** `domain :` 行
//     —— 这是默认解析器(实测指向 loopback,即 bx 自己);
//   - `#2`…`#7` 每个都带 `domain : local` / `domain : 254.169.in-addr.arpa` 之类,
//     并且 `options : mdns` —— 那是 mDNS 与反向 DNS 的**域名限定**解析器,
//     不回答「我的 DNS 归谁」这个问题。
//
// 所以判据是:**带 `domain :` 行的块一律跳过**。
//
// 另外输出里还有第二段 `DNS configuration (for scoped queries)`,那是按接口分的
// 解析器 —— 同样不回答默认解析是谁,**读到那个标题就停**。
//
// # 读不出来时返回空,而不是编一个
//
// 格式变了、命令不存在、输出为空 —— 一律返回空切片,让上层把这一项记成
// 「没问出来」。这条规则的全部价值建立在「问了,答案是坏的」与「没问出来」分得开,
// 编一个默认值会把这一整条规则变成谎话。
func parseSCUtilDNS(output string) []string {
	const scopedHeader = "DNS configuration (for scoped queries)"

	var (
		servers     []string
		seen        = map[string]bool{}
		inResolver  bool
		domainBound bool
		pending     []string
	)

	flush := func() {
		if inResolver && !domainBound {
			for _, s := range pending {
				if !seen[s] {
					seen[s] = true
					servers = append(servers, s)
				}
			}
		}
		inResolver, domainBound, pending = false, false, nil
	}

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == scopedHeader {
			// 按接口分的解析器不回答「默认解析归谁」。到此为止。
			break
		}
		if strings.HasPrefix(line, "resolver #") {
			flush()
			inResolver = true
			continue
		}
		if !inResolver {
			continue
		}
		key, value, ok := splitSCUtilField(line)
		if !ok {
			continue
		}
		switch {
		case key == "domain":
			// 域名限定的解析器(mDNS、反向 DNS 区)不是默认解析器。
			domainBound = true
		case strings.HasPrefix(key, "nameserver["):
			if value != "" {
				pending = append(pending, value)
			}
		}
	}
	flush()
	return servers
}

// splitSCUtilField 拆 `  key : value` 这种行。
//
// 键里可能带下标(`nameserver[0]`、`search domain[0]`),值里可能带空格
// (`flags : Request A records, Request AAAA records`),所以只按**第一个**冒号拆,
// 两边各自去空白。
func splitSCUtilField(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

// scutilDefaultResolvers 跑 `scutil --dns` 并取默认解析器。
//
// 跑不起来、超时、输出认不出来 —— 一律返回空。**绝不编一个答案**:上层会把空
// 记成「没问出来」,而那比一个猜出来的解析器地址诚实得多。
func scutilDefaultResolvers(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--dns").Output()
	if err != nil {
		return nil
	}
	return parseSCUtilDNS(string(out))
}
