//go:build windows

package install

// ServiceDisplayName 是**这个平台上那个服务真正的名字**,只给人看。
//
// `ServiceName` 是 systemd 的 unit 名("bx.service");Windows 的 SCM 里那个
// 服务叫 `bx`(windowsServiceName)。2026-09-15 真机实测:`bx doctor` 报
// `[OK] service installed: bx.service` —— 一个在这台机器上查不到的名字,
// 用户照着去 `Get-Service bx.service` 会一无所获。
//
// **行为一直是对的**:ServiceState 在 Windows 上忽略传进来的名字、内部用
// windowsServiceName;错的只有印出来那一个字符串。
func ServiceDisplayName() string { return windowsServiceName }
