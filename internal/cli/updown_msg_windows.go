//go:build windows

package cli

// upDoneMessage 是 `bx up` 完成后打印的收尾文案。Windows 上开机自启由服务 StartType 与
// 托盘登录自启独立治理(见 install.SetAutostart),up 本身不再顺带把它打开,故文案不能再
// 声称"设为开机自启"——改为如实说明自启不变 + 指路怎么调。
func upDoneMessage() string {
	return "✅ bx 已启动。(开机自启不变;用托盘\"开机自启\"或 bx autostart 调整)"
}

// downDoneMessage 同理:down 不再顺带取消开机自启。
func downDoneMessage() string {
	return "✅ bx 已停止。(开机自启不变)"
}

// upStepLabel 返回 up 进度行的标签。Windows 上 up 只启动,不设自启。
func upStepLabel() string {
	return "已启动"
}
