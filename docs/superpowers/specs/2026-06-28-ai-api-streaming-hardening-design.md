# AI-API 数据路径加固(流式回归测试 + 行为文档)设计

Status: APPROVED(2026-06-28,用户认可)。

## 背景

产品方向 ② = 让 AI API 跑得好。用户选「主动加固,无具体症状」。代码审计结论:bx 中继(`copyOneWay`)**已适配 AI 流式**——无缓冲透传(逐 chunk 读即写)、5min 空闲逐字节刷新(活跃长流不超时)、字节中继使 HTTP/2 keep-alive 天然可用、kill-switch 只拦新拨号不杀在飞流。**无具体症状 + 无法在单 VPS 环回拓扑测真实 AI 端点**(出站成环,同「干净客户端」缺口),故任何参数调优都是 premature optimization。

**故本片 = 把 AI 依赖的关键行为用回归测试钉死(防未来重构静默破坏)+ 写清 AI-API 行为/指引文档。** 真正的性能调优(空闲时长、QUIC、吞吐)留到有干净客户端可测时。

## 目标 / 非目标

**目标**:① 加一个 Mac 原生流式回归测试:`copyOneWay` 把带间隔的多 chunk **增量透传**(不缓冲/不合并),间隔内的 chunk 全部存活(逐字节空闲刷新成立)。② 写 `docs/ai-api-notes.md`:bx 对 AI-API 流量的行为(流式透传/长流/keep-alive/QUIC 注意/路由建议)+「调优前先在干净客户端测」原则。

**非目标**:任何中继参数调优(空闲时长、缓冲大小);QUIC/UDP 改动;吞吐优化——均留到可测时。不改任何生产代码路径(纯加测 + 文档)。

## 设计

### 流式回归测试(`internal/tun/engine_test.go`,package tun)

复用既有 `copyOneWay` + `net.Pipe` 测试范式(同文件已有 idle/bytes 测试)。net.Pipe 同步无缓冲:一次 `Write` 配一次 `Read`,且 Write 阻塞到被读完——故每个 chunk 1:1 映射到 dst 的一次 Read,若中继缓冲/合并则测试失败。

```go
func TestCopyOneWay_StreamsIncrementally(t *testing.T) {
	srcReader, srcWriter := net.Pipe()
	dstReader, dstWriter := net.Pipe()
	defer srcReader.Close()
	defer srcWriter.Close()
	defer dstReader.Close()
	defer dstWriter.Close()

	go copyOneWay(dstWriter, srcReader, 5*time.Second, nil)

	chunks := []string{"data: tok1\n\n", "data: tok2\n\n", "data: tok3\n\n"}
	go func() {
		for _, c := range chunks {
			_, _ = srcWriter.Write([]byte(c))
			time.Sleep(20 * time.Millisecond) // “token”间隔
		}
		_ = srcWriter.Close()
	}()

	buf := make([]byte, 4096)
	for i, want := range chunks {
		_ = dstReader.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := dstReader.Read(buf)
		if err != nil {
			t.Fatalf("chunk %d 读失败 err=%v(未增量透传?)", i, err)
		}
		if string(buf[:n]) != want {
			t.Fatalf("chunk %d got %q want %q(被缓冲/合并?)", i, string(buf[:n]), want)
		}
	}
}
```

### 文档(`docs/ai-api-notes.md`)

简洁说明:bx 对 AI-API 流量的行为与建议——
- **流式(SSE/逐 token)**:中继无缓冲、逐 chunk 透传(回归测试钉死);
- **长生成**:活跃流逐字节刷新 5min 空闲,不超时;真正闲置(无字节)连接 5min 后收尾防泄漏 goroutine/fd;
- **keep-alive / HTTP/2**:字节中继天然支持连接复用;
- **kill-switch**:只拦新拨号,在飞流不被杀(隧道真挂才断,与隧道同寿);
- **QUIC/HTTP3(UDP 443)**:默认 `udp.mode=block` → AI **网页 UI**(浏览器 QUIC)会回退 TCP;AI **API SDK**(TCP/HTTP2)不受影响;需 QUIC 可设 `udp.mode=proxy`;
- **路由**:AI 服务域名非 china → 默认走隧道;`global` 模式或 `rules.proxy` 可强制;
- **调优原则**:空闲时长/缓冲/吞吐等性能调优**先在干净客户端实测再动**,勿凭空改(单 VPS 环回拓扑测不了真实 AI 端点)。

## 测试策略

`go test ./internal/tun/ -run StreamsIncrementally`(Mac 原生,net.Pipe,免 root);全套件回归;两平台编译。文档无需测试。

## 决策记录

- 主动加固 = 回归测试钉死流式透传 + 文档,**不做凭空参数调优**(premature optimization;无症状 + 无法测)。
- 性能调优(idle/QUIC/吞吐)留到有干净客户端可测时。
- 纯加测 + 文档,零生产代码改动。

## 范围自检

单一小增量(1 个流式回归测试 + 1 篇行为文档),Mac 可测、零生产改动。适合一份极小 plan(1 任务)或直接实现。
