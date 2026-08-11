package leakcheck

import "time"

// 固定时刻。测试永不读 time.Now():一份带真实时钟的 fixture 会让「报告是不是
// 这一轮产出的」变成不可断言的事。
func fixedTime() time.Time {
	return time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC)
}
