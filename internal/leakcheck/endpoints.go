package leakcheck

// EndpointDisclosure 是页面在联网**之前**必须原样显示的第三方清单。
// 具体地址与选取理由见 Task 2。
type EndpointDisclosure struct {
	EchoV4 string `json:"echo_v4"`
	EchoV6 string `json:"echo_v6"`
	STUN   string `json:"stun"`
}
