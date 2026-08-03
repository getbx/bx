//go:build darwin

// bx-bridge 是 /usr/local/bin/bx 的稳定入口:定位 root-owned runtime 并 exec 同版本 CLI。
package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/getbx/bx/internal/bridge"
	"github.com/getbx/bx/internal/runtimedir"
)

func main() {
	executable, err := bridge.ResolveExecutable(runtimedir.Root, bridge.RealSys())
	if err != nil {
		fmt.Fprintln(os.Stderr, bridge.RepairHint)
		fmt.Fprintf(os.Stderr, "bx-bridge: %v\n", err)
		os.Exit(1)
	}
	argv := append([]string{executable}, os.Args[1:]...)
	if err := syscall.Exec(executable, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "bx-bridge: exec %s: %v\n", executable, err)
		os.Exit(1)
	}
}
