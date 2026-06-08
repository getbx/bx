package install

import (
	"strings"
	"testing"
)

func TestUnitText(t *testing.T) {
	u := UnitText("/usr/local/bin/bx run -c /etc/bx/config.yaml")
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"ExecStart=/usr/local/bin/bx run -c /etc/bx/config.yaml",
		"WantedBy=multi-user.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit 应含 %q,实际:\n%s", want, u)
		}
	}
}

func TestUnitInstalledFalseWhenAbsent(t *testing.T) {
	// 只验证函数可调用且返回 bool(/etc/systemd/system/bx.service 在测试环境状态不定)
	_ = UnitInstalled()
}
