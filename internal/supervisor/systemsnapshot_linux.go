//go:build linux

// systemsnapshot_linux.go 是路由快照器的 IO 薄壳:跑 `ip` 抓状态 / 还原,
// 纯逻辑(解析/diff/重建)在 routesnapshot.go。实现 confirm.Snapshotter。
// 本文件不接 live 路径(newSystemSnapshotter 仍 nop,见 mcp/server.go);9b 同 commit 切真。
package supervisor

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/getbx/bx/internal/confirm"
)

// linuxSnapshot 是一次 last-known-good 路由状态快照,实现 confirm.Snapshot。
type linuxSnapshot struct {
	id      string
	v4Rules []ruleSpec
	v6Rules []ruleSpec
	v4T100  []routeSpec
	v6T100  []routeSpec
}

func (s *linuxSnapshot) ID() string { return s.id }

type linuxSnapshotter struct{ seq int }

// NewSystemSnapshotter 返回 Linux 路由快照器(实现 confirm.Snapshotter)。
func NewSystemSnapshotter() confirm.Snapshotter { return &linuxSnapshotter{} }

// ipShow 跑 `ip <args...>` 抓 stdout 文本。
func ipShow(args ...string) (string, error) {
	out, err := exec.Command("ip", args...).Output()
	if err != nil {
		return "", fmt.Errorf("ip %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// ipShowTolerant 跑 `ip <args...>` 抓 stdout 文本;非零退出(例如 table 100 尚不存在
// 时 `route show table 100` 返回 exit 2)视为空输出而非错误——这是 pre-bx 正常基线。
func ipShowTolerant(args ...string) string {
	out, err := exec.Command("ip", args...).Output()
	if err != nil {
		return "" // 表不存在/空 → 无路由(正常基线)
	}
	return string(out)
}

func (s *linuxSnapshotter) Capture() (confirm.Snapshot, error) {
	v4r, err := ipShow("rule", "list")
	if err != nil {
		return nil, err
	}
	// table 100 在 pre-bx 基线可能不存在(ip 返回 exit 2);tolerant 读,空=无路由。
	v4t := ipShowTolerant("route", "show", "table", "100")
	snap := &linuxSnapshot{
		v4Rules: parseRules(v4r, familyV4),
		v4T100:  parseRoutes(v4t, familyV4),
	}
	if ipv6Enabled() {
		v6r, err := ipShow("-6", "rule", "list")
		if err != nil {
			return nil, err
		}
		v6t := ipShowTolerant("-6", "route", "show", "table", "100")
		snap.v6Rules = parseRules(v6r, familyV6)
		snap.v6T100 = parseRoutes(v6t, familyV6)
	}
	s.seq++
	snap.id = fmt.Sprintf("lkg-%d", s.seq)
	return snap, nil
}

// Restore 精确还原到快照:rules diff-reconcile,table 100 flush+replay。
// 尽力做完所有步骤(单步出错记录但继续),聚合返回。顺序:删多余 rule → flush+replay
// table 100 → 加缺失 rule。
func (s *linuxSnapshotter) Restore(snap confirm.Snapshot) error {
	ls, ok := snap.(*linuxSnapshot)
	if !ok {
		return fmt.Errorf("快照类型不符: %T", snap)
	}
	var errs []error
	run := func(args ...string) {
		if err := runIPQuiet(args...); err != nil {
			errs = append(errs, fmt.Errorf("ip %s: %w", strings.Join(args, " "), err))
		}
	}

	// M3: 只查一次 ipv6Enabled(),避免多次 syscall/文件探测。
	v6 := ipv6Enabled()

	// 1) rules:重抓当前 → diff → 删多余(此步)。
	// I2: parseCurrentRules 失败时上浮错误(toDel=∅ → 坏规则留在内核,回滚不完整)。
	curV4, err := parseCurrentRules(familyV4)
	if err != nil {
		errs = append(errs, fmt.Errorf("读当前 v4 规则失败(无法计算待删,回滚不完整): %w", err))
	}
	delV4, addV4 := diffRules(curV4, ls.v4Rules)
	var delV6, addV6 []ruleSpec
	if v6 {
		curV6, err6 := parseCurrentRules(familyV6)
		if err6 != nil {
			errs = append(errs, fmt.Errorf("读当前 v6 规则失败(无法计算待删,回滚不完整): %w", err6))
		}
		delV6, addV6 = diffRules(curV6, ls.v6Rules)
	}
	for _, r := range delV4 {
		run(ruleArgs("del", r)...)
	}
	for _, r := range delV6 {
		run(ruleArgs("del", r)...)
	}

	// 2) table 100:flush + replay(bx 独占)。
	// flush 是"清空再重放"的准备步骤;table 100 不存在(只加了 ip rule 无路由)时
	// `ip route flush table 100` 返回 exit 2(良性,无可清)——不记为错误。
	// del/add/replay 仍走 run() 严格记错。
	_ = runIPQuiet("route", "flush", "table", "100") // 空/不存在的 table 100 flush 返回 exit 2,良性(无可清),不记为错误
	for _, rt := range ls.v4T100 {
		run(routeAddArgs(rt)...)
	}
	if v6 {
		_ = runIPQuiet("-6", "route", "flush", "table", "100") // 同上,v6
		for _, rt := range ls.v6T100 {
			run(routeAddArgs(rt)...)
		}
	}

	// 3) rules:加缺失(防御性,bx mutation 一般不删基线规则)。
	for _, r := range addV4 {
		run(ruleArgs("add", r)...)
	}
	for _, r := range addV6 {
		run(ruleArgs("add", r)...)
	}
	return errors.Join(errs...)
}

// parseCurrentRules 抓当前 `ip [-6] rule list` 并解析。
// 出错时返回 (nil, err):调用方必须把错误上浮——静默降级会让 diff 退化为"全加"(不删坏规则)。
func parseCurrentRules(fam ipFamily) ([]ruleSpec, error) {
	args := []string{"rule", "list"}
	if fam == familyV6 {
		args = append([]string{"-6"}, args...)
	}
	out, err := ipShow(args...)
	if err != nil {
		return nil, err
	}
	return parseRules(out, fam), nil
}
