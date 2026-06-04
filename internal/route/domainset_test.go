package route

import "testing"

func TestDomainSet(t *testing.T) {
	s := NewDomainSet([]string{"openai.com", "*.google.com", "baidu.com"})
	cases := map[string]bool{
		"openai.com":         true, // 精确
		"api.openai.com":     true, // 后缀匹配子域
		"google.com":         true, // *.google.com 也覆盖裸域
		"maps.google.com":    true,
		"notgoogle.com":      false,
		"baidu.com.evil.com": false, // 不能被 baidu.com 误匹配
	}
	for d, want := range cases {
		if got := s.Match(d); got != want {
			t.Errorf("Match(%s)=%v want %v", d, got, want)
		}
	}
}
