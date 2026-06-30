package srvgen

import (
	"errors"
	"strings"
	"testing"
)

func TestRealitySNIAdvice(t *testing.T) {
	// 连不上 / 不支持 TLS1.3+X25519 → 告警
	if w := realitySNIAdvice("x.com", errors.New("timeout"), 0); len(w) == 0 || !strings.Contains(w[0], "TLS1.3") {
		t.Errorf("dial 失败应告警 TLS1.3/X25519: %v", w)
	}
	// 证书链过大(microsoft ~5879)→ 告警
	if w := realitySNIAdvice("www.microsoft.com", nil, 5879); len(w) == 0 || !strings.Contains(w[0], "证书") {
		t.Errorf("过大证书应告警: %v", w)
	}
	// 推荐站(都 < 4500)→ 不告警
	for _, sz := range []int{2505, 3230, 3543, 4085} { // cloudflare/apple/google/mozilla 实测
		if w := realitySNIAdvice("ok.com", nil, sz); len(w) != 0 {
			t.Errorf("%dB(推荐站)不该告警: %v", sz, w)
		}
	}
}
