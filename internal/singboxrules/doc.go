// Package singboxrules 把 bx 配置里的分流意图翻译成 sing-box 的 route.rules,并按
// 实测过的 sing-box 语义评估翻译结果 —— 手机端(libbox 进程内)与桌面(route.Router)
// 对同一个目的地必须给同一个答案,这个包是那条线唯一的落点。
//
// 设计与实测在 docs/superpowers/specs/2026-09-17-mobile-client-design.md(§3 对策、
// §4 语义对照、§8 分期①)。三件事**刻意不在这里**:
//
//   - DNS 分流(dns.split、hosts:):sing-box 的 DNS schema 跨版本有破坏性变更(§4.3),
//     先不翻;
//   - UDP 档(udp.mode / udp.transport):§4.4 未实测;
//   - kill-switch:sing-box 没有「隧道不健康就在拨号前 Block」的语义,「不配回落出站
//     ⇒ 连接失败 ⇒ 不泄漏」只是推理(§4.4),fail-closed 不许靠推理。
//
// 本包是纯判据(purity_test.go):不读文件、不联网、不跑命令;写文件与起 libbox
// 是调用方的事。
package singboxrules

// 出站 tag。手机端 libbox 配置里的三个出站要用同一组名字;翻译只产出前两个,
// block 留给将来 kill-switch 那一半。
const (
	OutboundProxy  = "proxy"
	OutboundDirect = "direct"
	OutboundBlock  = "block"
)

// 两个 rule-set 的 tag(china 列表太大,不内联进 rules;写成 source 格式的本地文件)。
const (
	RuleSetChinaDomain = "bx-china-domain"
	RuleSetChinaCIDR   = "bx-china-cidr"
)
