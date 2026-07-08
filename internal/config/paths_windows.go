//go:build windows

package config

// Windows:运行期数据目录 C:\ProgramData\bx(与 socket/pid/config 同根)。
const DefaultDataDir = `C:\ProgramData\bx`
