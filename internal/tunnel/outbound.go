package tunnel

import (
	"errors"
	"fmt"
)

// ErrOutboundUnsupported:这个传输还没有给进程内 sing-box(手机端 libbox)用的出站。
var ErrOutboundUnsupported = errors.New("this transport has no in-process sing-box outbound yet")

// SingboxOutbound 给出 link 对应的 sing-box 出站(tag 由调用方定),供手机端把它拼进 libbox 配置。
// 它与桌面起 sing-box 子进程时用的出站是**同一个生成器**(outbound_test.go 钉住),所以两边
// 连的是同一台服务器、同一套握手参数。这一期只有 reality(spec §8 ②);其余传输报
// ErrOutboundUnsupported,不猜。
func SingboxOutbound(link, tag string) (map[string]any, error) {
	switch Kind(link) {
	case KindReality:
		v, err := parseVlessLink(link)
		if err != nil {
			return nil, err
		}
		return v.outbound(tag), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrOutboundUnsupported, Kind(link))
	}
}
