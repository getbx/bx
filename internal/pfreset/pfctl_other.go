//go:build !darwin

package pfreset

import "context"

// 非 darwin 没有 pf:驱动是 nil,Run 一个字都不做。
func NewDriver(string) Driver { return nil }

func FlushStaleDarwin(context.Context, string) (bool, error) { return false, nil }
