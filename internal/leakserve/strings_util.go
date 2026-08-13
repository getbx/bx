package leakserve

import "strings"

func splitLines(s string) []string { return strings.Split(s, "\n") }

func fieldsOf(line string) []string { return strings.Fields(line) }

// routeFamily 是要读的一张路由表,以及该表里 `default` 的含义。
type routeFamily struct {
	flag  string // netstat -f 的参数
	deflt string // 这张表里 `default` 对应的前缀
}

// routeFamilies:**两个地址族都要读。**
//
// 只读 v4 的话,一条抢走全部 v6 流量的隧道**一个字都不会被说出来** —— 而
// TunnelVision 有 v6 版本(RA / DHCPv6 注入路由)。提成具名变量是为了让这件事
// 能被测:留在 exec 那一侧时,删掉 inet6 那一行编译得过、整套测试全绿(实测)。
var routeFamilies = []routeFamily{
	{flag: "inet", deflt: "0.0.0.0/0"},
	{flag: "inet6", deflt: "::/0"},
}
