//go:build !darwin

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bx-bridge is only used on macOS")
	os.Exit(1)
}
