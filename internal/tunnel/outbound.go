package tunnel

import "github.com/getbx/bx/internal/singboxout"

// ErrOutboundUnsupported:这个传输还没有给进程内 sing-box 用的出站。与 singboxout 同一个哨兵。
var ErrOutboundUnsupported = singboxout.ErrUnsupported

// SingboxOutbound 是 singboxout.Outbound 的薄壳(手机端与桌面同一个生成器,outbound_test.go 钉住)。
func SingboxOutbound(link, tag string) (map[string]any, error) { return singboxout.Outbound(link, tag) }
