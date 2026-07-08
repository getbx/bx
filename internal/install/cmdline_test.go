package install

import (
	"reflect"
	"testing"
)

func TestCommandLineFieldsQuotedWindowsPath(t *testing.T) {
	// Windows 服务典型 BinaryPathName:exe 与 config 都含空格路径、带引号。
	got := commandLineFields(`"C:\Program Files\bx\bx.exe" run -c "C:\ProgramData\bx\config.yaml"`)
	want := []string{`C:\Program Files\bx\bx.exe`, "run", "-c", `C:\ProgramData\bx\config.yaml`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("引号命令行拆分错:\n got=%q\nwant=%q", got, want)
	}
}

func TestCommandLineFieldsUnquotedPOSIX(t *testing.T) {
	got := commandLineFields(`/usr/local/bin/bx run -c /etc/bx/config.yaml`)
	want := []string{"/usr/local/bin/bx", "run", "-c", "/etc/bx/config.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("无引号命令行拆分错: got=%q want=%q", got, want)
	}
}

func TestCommandLineFieldsCollapsesSpacesAndTrims(t *testing.T) {
	got := commandLineFields(`  bx    run   `)
	want := []string{"bx", "run"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("多空格/首尾空白处理错: got=%q want=%q", got, want)
	}
}

func TestCommandLineFieldsEmpty(t *testing.T) {
	if got := commandLineFields("   "); got != nil {
		t.Errorf("全空白应为 nil, got=%q", got)
	}
}

// serviceSubcommand 取 exe 之后第一个字段,是 up 防呆的关键(须为 "run")。
func TestServiceSubcommand(t *testing.T) {
	if got := serviceSubcommand(`"C:\Program Files\bx\bx.exe" run -c "C:\x\config.yaml"`); got != "run" {
		t.Errorf("子命令应为 run, got=%q", got)
	}
	if got := serviceSubcommand(`"C:\bx\bx.exe"`); got != "" {
		t.Errorf("无子命令应为空, got=%q", got)
	}
}
