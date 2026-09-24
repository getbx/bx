package install

import (
	"strings"
	"testing"
)

// 已经装好的 linux 机器(NAS、路由器)的 unit 写于这个 flag 存在之前;只有 `bx setup`
// 会写 unit,而那些机器不会再跑 setup。`bx update` 顺手补上,只改 ExecStart 那一行。
func TestUpgradeExecStartAddsTheFlagOnlyWhereItIsSafe(t *testing.T) {
	old := UnitText("/usr/local/bin/bx run -c /etc/bx/config.yaml")
	got, changed := upgradeExecStartLine(old, "start-failure-file", "/var/lib/bx/core-start-failure.json")
	if !changed {
		t.Fatal("标准的 run 行没有被补上 flag")
	}
	if !strings.Contains(got, "ExecStart=/usr/local/bin/bx run -c /etc/bx/config.yaml --start-failure-file /var/lib/bx/core-start-failure.json\n") {
		t.Fatalf("补出来的 ExecStart 不对:\n%s", got)
	}
	// 其余每一行逐字不动。
	if strings.Replace(got, " --start-failure-file /var/lib/bx/core-start-failure.json", "", 1) != old {
		t.Fatal("除了 ExecStart 那一处,unit 里别的东西也被改了")
	}
	// 已有 ⇒ 不动(幂等)。
	if again, changed := upgradeExecStartLine(got, "start-failure-file", "/var/lib/bx/core-start-failure.json"); changed || again != got {
		t.Fatal("已经带着 flag 的 unit 又被改了一遍")
	}
	// 不是 `run` 的旧 unit(递归调用 up 的那种)不碰 —— 那一种由 bx up 自己报错让用户重装。
	legacy := UnitText("/usr/local/bin/bx up -c /etc/bx/config.yaml")
	if _, changed := upgradeExecStartLine(legacy, "start-failure-file", "/x"); changed {
		t.Fatal("不是 run 的旧 unit 也被改了")
	}
}
