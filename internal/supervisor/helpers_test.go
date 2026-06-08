package supervisor

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("a", "b") != "a" {
		t.Error("应取第一个非空")
	}
	if firstNonEmpty("", "b") != "b" {
		t.Error("第一个空应取第二个")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("都空应为空")
	}
}
