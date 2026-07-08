//go:build windows

package install

// Windows:bx.exe 装到 Program Files(装服务需管理员,此目录亦需管理员,权限一致)。
const BinPath = `C:\Program Files\bx\bx.exe`
