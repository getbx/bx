//go:build !windows

package install

// ServiceDisplayName:非 Windows 上就是 systemd 的 unit 名本身。
func ServiceDisplayName() string { return ServiceName }
