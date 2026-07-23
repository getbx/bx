//go:build !windows

package cli

// upDoneMessage 是 `bx up` 完成后打印的收尾文案。Linux(systemd)/macOS(launchd)上
// Enable 确实原子地把开机自启一并打开,文案维持原样准确,不做任何改动。
func upDoneMessage() string {
	return "✅ bx 已启动。"
}

// downDoneMessage 同理:Disable 确实一并取消了开机自启。
func downDoneMessage() string {
	return "✅ bx 已停止并取消开机自启。"
}
