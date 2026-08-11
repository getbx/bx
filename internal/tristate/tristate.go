// Package tristate 只放一个三值枚举,不 import 本仓库任何东西。
//
// 它单独成一个叶子包,理由与 internal/barriercidr 完全相同:两个互不相干的包都
// 需要它,而它原本住在一个**很重**的包里。`internal/observe` 为了做观测要 import
// install / supervisor,于是任何只想要这个枚举的包都会被拖进整条依赖链 ——
// 实测 `internal/leakcheck` 因此传递依赖了 **23 个内部包**(含 install、supervisor、
// provision、tun、dialer),全部代价只为一个三值枚举。
//
// 更糟的是那会让「这个包很纯」这句话变成一句自欺:leakcheck 里有一条守卫专门
// 断言它不碰控制面,而它只查直接 import —— 直接 import 确实只有 observe 一个,
// 传递依赖却把整个数据面和控制面都拖了进来。**守卫说的和事实不符,而守卫是绿的。**
package tristate

import "encoding/json"

// Tristate 区分「观测到是」「观测到否」「观测不到」。
//
// **零值必须是 Unknown。** 用 bool 会把「问不出来」压成 false,让未观测的项冒充
// 正常 —— 这是本仓库反复栽的那一类错误,也是这个类型存在的全部理由。
type Tristate uint8

const (
	Unknown Tristate = iota
	True
	False
)

func (t Tristate) String() string {
	switch t {
	case True:
		return "true"
	case False:
		return "false"
	default:
		return "unknown"
	}
}

// FromBool 把一个确定的布尔判定转成三态。**仅在观测成功时使用** ——
// 观测失败时该留 Unknown,而不是拿一个 false 冒充「观测到没有」。
func FromBool(value bool) Tristate {
	if value {
		return True
	}
	return False
}

// MarshalJSON 让三态在 JSON 里是 "true"/"false"/"unknown" 而非数字。
// 数字对 agent 无意义,而这个类型的全部价值在于让读它的人(与 agent)读懂
// 「没问出来」和「问了,答案是否」不是一回事。
//
// **它必须和类型住在一起**:Go 不允许给别名类型加方法,所以留在 observe 里
// 会让整个下沉方案编译不过 —— 这也正好说明它本来就属于类型,不属于观测层。
func (t Tristate) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}
