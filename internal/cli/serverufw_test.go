package cli

import (
	"os/exec"
	"reflect"
	"testing"
)

func TestServerUFWRules(t *testing.T) {
	cases := []struct {
		name string
		cfg  serverConfig
		want []string
	}{
		{
			name: "reality默认(附带hys2)",
			cfg:  serverConfig{Type: "reality", Port: 443, UDPLink: "hysteria2://server"},
			want: []string{"443/tcp", "443/udp"},
		},
		{
			name: "reality --tcp-only",
			cfg:  serverConfig{Type: "reality", Port: 8443},
			want: []string{"8443/tcp"},
		},
		{
			name: "hysteria2",
			cfg:  serverConfig{Type: "hysteria2", Port: 443},
			want: []string{"443/udp"},
		},
		{
			name: "brook",
			cfg:  serverConfig{Type: "brook", Listen: ":9999"},
			want: []string{"9999/tcp"},
		},
		{
			name: "port<=0 按443缺省(reality tcp-only)",
			cfg:  serverConfig{Type: "reality"},
			want: []string{"443/tcp"},
		},
		{
			name: "port<=0 按443缺省(reality+hys2)",
			cfg:  serverConfig{Type: "reality", UDPLink: "hysteria2://server"},
			want: []string{"443/tcp", "443/udp"},
		},
		{
			name: "port<=0 按443缺省(hysteria2)",
			cfg:  serverConfig{Type: "hysteria2"},
			want: []string{"443/udp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := serverUFWRules(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("serverUFWRules(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

// TestOpenUFWRulesFailsFast 验证 openUFWRules 会尝试 exec `ufw`;开发/CI 环境通常没有 ufw
// 二进制,故 exec 在查找可执行文件阶段就失败返错(从不真正跑起 ufw),与既有
// TestOpenUFWRejectsBadListen 同精神:验证失败路径返回 wrapped error,而不依赖真实 ufw 存在。
func TestOpenUFWRulesFailsFast(t *testing.T) {
	if _, err := exec.LookPath("ufw"); err == nil {
		t.Skip("环境里存在 ufw 可执行文件,跳过以避免真实执行")
	}
	if err := openUFWRules([]string{"443/tcp", "443/udp"}); err == nil {
		t.Fatal("ufw 不存在时应返回 error")
	}
}
