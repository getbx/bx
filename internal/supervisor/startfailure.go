package supervisor

import "errors"

// Core 启动失败的分类 —— **哨兵错误,一条字符串匹配都没有**。
//
// 起因是 2026-09-12 那次真机事故:VPS 连 ssh 与 ping 都不通,而 bx 对着
// `sudo bx up` 反复回答 core_ownership_uncertain。Core 自己从第一秒就知道
// `dial tcp <server>:443: i/o timeout`,那句话进了 root-only 的 /var/log/bx.log
// 就没了出口 —— 它缺的从来不是信息,是一个**结构化的**出口。
//
// 为什么不是按错误文本分类:判据长在文本匹配上是本仓库明确反对的形状
// (改一句措辞就静默换掉判定,而两边都不报错)。既有错误文案因此**一个字
// 不改** —— 它们进 Core 日志,是给人读的;哨兵是给机器读的,两者并存。
var (
	// ErrConfig:配置文件里的内容本身不可用(规则/CIDR/hosts/服务器链接)。
	// 与「暂时读不到配置」不同 —— 那是别的故障,不该借这个码。
	ErrConfig = errors.New("配置不可用")
	// ErrProvision:内嵌的 brook/sing-box 没能释放到 data_dir。
	ErrProvision = errors.New("准备传输二进制失败")
	// ErrTUNOpen:开 TUN 设备失败(权限、设备被占、wintun.dll 缺席……)。
	ErrTUNOpen = errors.New("打开 TUN 设备失败")
	// ErrHijack:TUN 起来了,劫持默认路由失败。
	ErrHijack = errors.New("劫持默认路由失败")

	// ErrTunnelUnhealthy:隧道没能在启动窗口内建起来。
	//
	// 它同时是这一族里「**没判出来**」那一档本身:waitTunnelHealthy 只知道
	// 「20 秒了还不健康」,产出的就是它。与 observe.Tristate、
	// leakcheck.NotChecked 同一条 —— 「问不出来」不是任何一个具体答案。
	ErrTunnelUnhealthy = errors.New("隧道没能建起来")
)

// 启动失败码。**只有码,没有自由文本** —— 它要跨进程传给 Guardian,
// 一个不含自由文本的记录按构造漏不出路径/链接/凭据。
const (
	StartFailureTunnelUndetermined = "tunnel_unhealthy_undetermined"
	StartFailureTUNOpen            = "tun_open_failed"
	StartFailureHijack             = "hijack_failed"
	StartFailureProvision          = "provision_failed"
	StartFailureConfig             = "config_unusable"
	// StartFailureOther:认不出的一律落这里,**不静默丢弃**。
	StartFailureOther = "other"
)

// startFailureSentinels 是**有序**表:先具体后笼统(隧道那一族随后被拆成三档,
// 见 tunneldiagnosis.go —— 具体那两档的错误链里同时挂着家长 ErrTunnelUnhealthy,
// 家长排到前面会让三档塌成一档)。
//
// **每一条都必须有产地**:一个没有产地的哨兵是一个永远不会出现的码,与没有
// 这个分支在输出上完全一样。由 TestEveryStartFailureSentinelHasAProductionSite
// 穷举这张表钉住 —— 加一个哨兵不接产地,它当场转红。
var startFailureSentinels = []struct {
	Err  error
	Code string
}{
	{ErrTunnelUnhealthy, StartFailureTunnelUndetermined},
	{ErrTUNOpen, StartFailureTUNOpen},
	{ErrHijack, StartFailureHijack},
	{ErrProvision, StartFailureProvision},
	{ErrConfig, StartFailureConfig},
}

// tagStartFailure 把一个哨兵挂到既有错误上,**而一个字都不改它的文案**。
//
// 为什么不是 fmt.Errorf("%w(%w)", err, sentinel):那会把哨兵的中文塞进给人读
// 的那句话里(「建 TUN: permission denied(打开 TUN 设备失败)」),同一件事说
// 两遍。既有文案进 Core 日志、是人读的,哨兵是给机器读的 —— 两者并存但互不
// 污染,这也是计划里「既有错误文案一个字不改」那条的落点。
func tagStartFailure(sentinel, err error) error {
	if err == nil {
		return nil
	}
	return taggedStartFailure{err: err, sentinel: sentinel}
}

type taggedStartFailure struct {
	err      error
	sentinel error
}

func (e taggedStartFailure) Error() string { return e.err.Error() }

// Unwrap 返回两条:原错误(链上的 %w 全都还在)与哨兵。
func (e taggedStartFailure) Unwrap() []error { return []error{e.err, e.sentinel} }

// StartFailureCode 把 Run 返回的错误分类成一个码。判据是 errors.Is,不是文本。
//
// nil 返回空串:「没有失败」与「失败但认不出来」是两件事,压成同一个值就等于
// 让一次成功的启动看起来像一次没分类的失败(或者反过来,更糟)。
func StartFailureCode(err error) string {
	if err == nil {
		return ""
	}
	for _, entry := range startFailureSentinels {
		if errors.Is(err, entry.Err) {
			return entry.Code
		}
	}
	return StartFailureOther
}
