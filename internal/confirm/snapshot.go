package confirm

// Snapshot 是一次 last-known-good 状态快照的句柄。
type Snapshot interface {
	ID() string
}

// Snapshotter 抓取/还原系统状态(路由/config/unit/nft)。
// 真实实现是平台特定的(后续任务);本包只定义接口,便于纯逻辑测试。
type Snapshotter interface {
	Capture() (Snapshot, error)
	Restore(Snapshot) error
}

// ArmWithSnapshot 先抓 last-known-good,再武装死手;Capture 失败不武装、不改动。
func ArmWithSnapshot(g *Guard, s Snapshotter) (Snapshot, error) {
	snap, err := s.Capture()
	if err != nil {
		return nil, err
	}
	if err := g.Arm(func() error { return s.Restore(snap) }); err != nil {
		return nil, err
	}
	return snap, nil
}
