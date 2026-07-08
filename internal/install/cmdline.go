package install

import "strings"

// cmdline.go 是 Windows 服务命令行的**纯解析**(无 build tag,可跨平台单测)。Windows 服务的
// BinaryPathName 是一整串带引号的命令行(如 `"C:\Program Files\bx\bx.exe" run -c "C:\...\config.yaml"`);
// 建服务时要拆成 exepath+args 喂 mgr.CreateService,读回时要取子命令做 up 防呆。两处共用此解析。

// commandLineFields 把一条命令行拆成字段,尊重双引号(Windows 路径含空格如 "Program Files" 必需)。
// 引号内的空格不分隔;引号本身不进结果。Windows 路径不含 `"`,故无需处理反斜杠转义引号。
// 空串或全空白 → nil。也吃无引号的 POSIX 命令行(空格分隔),故 linux/darwin 亦可复用。
func commandLineFields(cmdline string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	has := false // 当前是否已开始一个字段(允许空引号 "" 成为空字段)
	for _, r := range cmdline {
		switch {
		case r == '"':
			inQuote = !inQuote
			has = true
		case r == ' ' && !inQuote:
			if has {
				fields = append(fields, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		fields = append(fields, cur.String())
	}
	return fields
}

// serviceSubcommand 从服务命令行取子命令(exe 之后第一个字段,如 "run")。无或不足则 ""。
func serviceSubcommand(cmdline string) string {
	f := commandLineFields(cmdline)
	if len(f) >= 2 {
		return f[1]
	}
	return ""
}
