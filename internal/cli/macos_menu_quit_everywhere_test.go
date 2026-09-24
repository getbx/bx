package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rebuildMenu 的**每一个**提前 return 之前都必须挂上 Quit(known-gaps A2,2026-09-23)。
//
// TestMacMenuQuitActionPresentInEveryState 只数顶层那一个无条件的 Quit,而恢复浮层
// 在走到那一行之前就 return 了 —— 恢复进行中菜单里没有任何离场的出口。Quit 会先关
// 保护再退出,**恢复卡住时它恰恰是唯一的出路**;2026-08-04 那次路径恢复卡了 71 分钟、
// 用户全程关不掉保护,就是这个形状。判据是「每一个」而不是「至少一个」:以后再加一个
// 浮层分支,漏掉 Quit 也会在这里红。
func TestMacMenuEveryEarlyExitOfRebuildMenuStillOffersQuit(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(source), "private func rebuildMenu()")
	if !ok {
		t.Fatal("找不到 rebuildMenu 的函数体 —— 守卫读不懂现在的代码了")
	}
	var previous string
	returns := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if trimmed == "return" {
			returns++
			if !strings.Contains(previous, "addQuit(") {
				t.Errorf("rebuildMenu 有一个提前 return 之前没挂 Quit(前一行是 %q)—— 那个状态下菜单没有离场的出口", previous)
			}
		}
		previous = trimmed
	}
	if returns == 0 {
		t.Fatal("rebuildMenu 里一个提前 return 都没找到 —— 守卫读不懂现在的代码了,或者浮层分支被整个重写了")
	}
}
