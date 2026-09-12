package cli

import (
	"strings"
	"testing"
)

// GuardianStatus.swift 是菜单**唯一**的解码口:`/v1/status`、`/v1/up`、`/v1/down`
// 的响应体都从这里变成菜单的输入。这条守卫钉的是整份解码器的一条性质,不是某一
// 个字段:**任何一个 `init(from:)` 里都不许出现 `try?`。**
//
// 为什么是这一条:这个文件通篇的纪律是「键**缺席** ⇒ nil / 默认值(那是常态,
// 旧 Guardian、omitempty、还没长出来的字段);键**在场而读不动** ⇒ 响亮失败」。
// 前一半由 `decodeIfPresent` 自己提供,`try?` **一点都不参与**;它唯一多买到的
// 是后一半 —— 把一份读不动的报文悄悄换成那个默认值。而这些默认值在下游一律读作
// 好消息:空的 `failing_rules` 读作「一条规则都没在失败」(规则窗口的词汇表里
// 「一行没有副标题」= 查过了、健康),空数组 / nil 同理。**于是解码器会在用户
// 最该被警告的那一刻给他一句安慰,而两侧都不报错。**
//
// 2026-09-12 之前 `failingRules` 那一行正是这个形状,注释还替它辩护说「缺席 = 空,
// 不是解码失败」—— 那句话是真的,但它描述的是 `decodeIfPresent` 的性质,不是
// `try?` 的;`try?` 加的恰好是注释没提的那一半。
//
// 守的是**类**不是那一个字段:下一个人给 GuardianStatus 加字段时,照抄旁边一行
// 是最自然的动作,而这条守卫让「照抄了带 try? 的那一行」当场红。
func TestMacMenuStatusDecoderNeverSwallowsADecodeError(t *testing.T) {
	source := readMenuSwiftSource(t, "GuardianStatus.swift")
	// 注释里解释这条不变量时必然会写到 `try?` 本身(上面这段就是),而一段
	// 注释满足断言正是本仓库的守卫被绕过的方式之一;字符串字面量同理。
	code := blankSwiftStringLiterals(stripSwiftComments(source))

	// 读不懂现在的代码时响亮失败,不静默放行。
	if !strings.Contains(code, "init(from decoder: Decoder) throws") {
		t.Fatal("GuardianStatus.swift 里找不到任何手写 init(from:) —— 本守卫读不懂现在的代码了,先修守卫")
	}
	if !strings.Contains(code, "decodeIfPresent") {
		t.Fatal("GuardianStatus.swift 里找不到 decodeIfPresent —— 本守卫读不懂现在的代码了,先修守卫")
	}

	for i, line := range strings.Split(code, "\n") {
		if strings.Contains(line, "try?") {
			t.Errorf("GuardianStatus.swift:%d 用 try? 吞掉了一次解码失败:%s\n"+
				"缺席那一半 decodeIfPresent 已经给了;try? 只额外把「在场而读不动」"+
				"换成了一个下游读作好消息的默认值。", i+1, strings.TrimSpace(line))
		}
	}
}
