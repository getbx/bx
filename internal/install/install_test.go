package install

import (
	"os"
	"path/filepath"
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

func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src-bx")
	want := []byte("#!/bin/sh\necho bx\n")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "bx") // 父目录不存在,验证 MkdirAll
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("内容不一致: %q != %q", got, want)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("权限: %o != 0755", fi.Mode().Perm())
	}
}
