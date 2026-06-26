//go:build !linux

// systemsnapshot_other.go 在非 Linux 平台提供 NewSystemSnapshotter() 的 nop 占位,
// 使 run.go(无 build tag)在 darwin/windows 等平台也能编译。
// 真实快照(路由抓取/还原)仅在 Linux 实现(systemsnapshot_linux.go)。
package supervisor

import "github.com/getbx/bx/internal/confirm"

// nopSystemSnap 是 nopSystemSnapshotter 产生的占位快照。
type nopSystemSnap struct{}

func (nopSystemSnap) ID() string { return "nop" }

// nopSystemSnapshotter 在非 Linux 平台不抓真实状态,供 run.go 编译用。
type nopSystemSnapshotter struct{}

func (nopSystemSnapshotter) Capture() (confirm.Snapshot, error) { return nopSystemSnap{}, nil }
func (nopSystemSnapshotter) Restore(confirm.Snapshot) error     { return nil }

// NewSystemSnapshotter 在非 Linux 平台返回 nop 快照器。
func NewSystemSnapshotter() confirm.Snapshotter { return nopSystemSnapshotter{} }
