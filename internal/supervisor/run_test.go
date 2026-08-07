package supervisor

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeStderrSource struct{ lines []string }

func (f fakeStderrSource) RecentStderr() []string { return f.lines }

// 「bx 隧道健康检查超时(20s)」本身不说明任何原因。真机事故 2026-08-06:运维只能
// 拿到这一句,分不出是握手失败、超时还是被 reset,于是反复重装、最后卸载。
// 子进程那几行(已抹密)往往就是唯一能回答「为什么」的东西。
func TestWithTunnelStderrAttachesSubprocessReason(t *testing.T) {
	base := errors.New("bx 隧道健康检查超时(20s): restarts=1")
	got := withTunnelStderr(fakeStderrSource{lines: []string{
		"outbound/vless[proxy]: dial tcp 203.0.113.9:443: i/o timeout",
	}}, base)

	if !errors.Is(got, base) {
		t.Error("必须保留原错误(用 %w 包装),否则上层的 errors.Is 判定会失效")
	}
	if !strings.Contains(got.Error(), "i/o timeout") {
		t.Errorf("子进程的原因必须出现在消息里,实际 = %q", got)
	}
}

// 只取最后几行:目的是给线索,不是把日志搬进错误消息。
func TestWithTunnelStderrKeepsOnlyTail(t *testing.T) {
	var lines []string
	for i := 0; i < healthErrStderrLines*3; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	got := withTunnelStderr(fakeStderrSource{lines: lines}, errors.New("boom")).Error()
	if strings.Contains(got, "line-0") {
		t.Errorf("不该带上最早的行,实际 = %q", got)
	}
	if !strings.Contains(got, fmt.Sprintf("line-%d", len(lines)-1)) {
		t.Errorf("必须带上最后一行,实际 = %q", got)
	}
}

// 没有子进程输出时不得改动错误 —— 诊断增强不该给正常错误加噪声。
func TestWithTunnelStderrLeavesErrorAloneWhenNoOutput(t *testing.T) {
	base := errors.New("boom")
	if got := withTunnelStderr(fakeStderrSource{}, base); got != base {
		t.Errorf("无输出时应原样返回,实际 = %v", got)
	}
}
