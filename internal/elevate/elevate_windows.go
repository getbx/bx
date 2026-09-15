//go:build windows

package elevate

const (
	// Windows 没有 sudo。裸命令 + Note() 那一句,而不是
	// `Start-Process bx -Verb RunAs -ArgumentList up` —— 后者准确但没人粘得动,
	// 而这几处的全部意义就是被粘贴。
	Prefix = ""
	note   = "(要在管理员 PowerShell 里跑)"
)
