package provision

// brookurl.go 派生 brook 官方 release 的下载 URL(纯逻辑,可跨平台单测)。
// 无内嵌 brook 的平台(windows/其他 arch)靠它兜底下载,让 bx up 起 brook 隧道不空转。

// defaultBrookURL 按 txthinking/brook 的 release 资产命名约定拼下载地址:
//   - windows:brook_<goarch>.exe(裸 exe)
//   - 其余:brook_<goos>_<goarch>(无后缀)
//
// version 为空(未知)则返回 "",由调用方报清晰错误而非拼出坏 URL。
func defaultBrookURL(version, goos, goarch string) string {
	if version == "" {
		return ""
	}
	asset := "brook_" + goos + "_" + goarch
	if goos == "windows" {
		asset += ".exe"
	}
	return "https://github.com/txthinking/brook/releases/download/" + version + "/" + asset
}
