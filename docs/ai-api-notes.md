# bx 与 AI API 流量

bx 是透明全局代理,AI API 流量(LLM 推理、流式输出、长生成)在其数据面的行为如下。
要点:**核心中继已适配 AI 流式,无需特殊配置**。

## 行为

- **流式(SSE / 逐 token)**:中继(`internal/tun/engine.go` 的 `copyOneWay`)**无缓冲、逐 chunk 读即写** —— 上游吐多少 token 就立刻转多少,不批量积攒。该性质有回归测试钉死(`TestCopyOneWay_StreamsIncrementally`),重构不会静默破坏。
- **长生成**:空闲超时 5min,但**每次读/写都刷新** —— 只要流在活跃吐字节(哪怕 token 间有秒级停顿),就永不超时;长达数分钟的生成不受影响。真正闲置(完全无字节)的连接 5min 后才收尾,防 half-open 泄漏 goroutine/fd。
- **keep-alive / HTTP/2**:字节级中继 → 连接复用、HTTP/2 多路复用天然可用,SDK 复用连接无额外开销。
- **kill-switch**:只拦**新**拨号(隧道不健康时新连接 fail-closed,不漏真实 IP);**在飞的流不被杀**,与隧道同寿(隧道真挂才断,瞬时探测抖动由 `maxFails=3` 容忍)。

## 注意

- **QUIC / HTTP3(UDP 443)**:默认 `udp.mode=block`。AI **网页 UI**(浏览器走 QUIC)会回退到 TCP;AI **API SDK**(几乎都走 TCP/HTTP2)不受影响。若需 QUIC 直达,设 `udp.mode=proxy`(经隧道转发 UDP)。
- **路由**:AI 服务域名不在 china 列表 → **默认走隧道**。如需强制,用 `global` 模式或在 `rules.proxy` 显式列出该域名。

## 性能调优原则

空闲时长、缓冲大小、QUIC、吞吐等**性能调优,先在干净客户端实测再动** —— 不要凭空改参数(premature optimization)。当前单 VPS 环回拓扑(bx 连本机 brook 服务器)出站会成环,测不了真实 AI 端点;真实测量需一台能经隧道访问公网的干净客户端。
