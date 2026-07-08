//go:build windows

package cli

// Windows:客户端配置放 ProgramData(与 supervisor 的 SockPath/PidPath 同根 C:\ProgramData\bx)。
const defaultConfigPath = `C:\ProgramData\bx\config.yaml`
