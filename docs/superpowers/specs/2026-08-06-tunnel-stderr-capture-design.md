# 传输子进程 stderr 收集设计

## 背景

真机事故 2026-08-06:reality 隧道连不通,日志里能拿到的只有

```
2026/08/06 18:47:06 bx 隧道健康检查超时(20s): restarts=1
```

没有任何协议层原因。分不出是握手失败、TCP 超时、被 reset,还是 sing-box 配置有问题。
连带后果:`bx down` 撞上 `barrier_install_failed`,用户在没有线索的情况下反复重装,
最后卸载换回 brook。

根因很直接:6 个起子进程的地方(`brook.go` + 5 个 sing-box 引擎)都只写

```go
cmd := exec.Command(bin, args...)
cmd.Start()
```

从不设置 `cmd.Stderr`。Go 的语义是**接到 `/dev/null`** —— 子进程的错误不是没地方看,
是被直接丢弃了。

这与 CLAUDE.md 已有的「失败必须留下可操作线索」是同一条原则,只是此前只在 Guardian
那一层落实过,传输层是个洞。

## 设计

### 一、共享的 stderr 汇聚器

`internal/tunnel` 内新增:

```go
type stderrSink struct {
    label   string   // "singbox" / "brook"
    secrets []string // 逐行抹掉的字符串
    mu      sync.Mutex
    recent  []string // 环形缓冲,保留最后 recentStderrLines 行
}
```

每读到一行做三件事,顺序固定:

1. **截断**到 `maxStderrLineLength`(512 字符)——防止子进程一行刷爆日志;
2. **抹密**:把 `secrets` 里的每个串替换成 `<redacted>`;
3. 送 `log.Printf("%s: %s", label, line)`,同时进环形缓冲。

抹密必须在写日志**之前**,不能只在取尾巴时做——日志本身就是泄露面。

### 二、必须抹掉链接

`brook connect -l <link>` 的链接**就在 argv 上**,brook 可能把它回显进自己的日志,
而链接自带凭据。sing-box 在生成配置里是 `"level": "warn"`,正常不打凭据,但不赌。

故 sink 构造时登记为 secret 的是:传输链接原文,以及 sing-box 配置里的
uuid / password / obfs-password 等凭据字段值。空串不登记(否则会把每行都替换掉)。

### 三、6 个起点统一走一个 helper

```go
func startWithStderr(cmd *exec.Cmd, label string, secrets ...string) (*execRunner, error)
```

接管道 → `cmd.Start()` → 起 goroutine 逐行读到 EOF。进程退出时管道关闭,goroutine
自然结束,无需额外生命周期管理。

### 四、失败时把尾巴附上

`Runner` 接口**不动**——已有假实现在用,加方法会全线破坏。另定可选接口:

```go
type stderrTailer interface{ RecentStderr() []string }
```

健康检查失败时类型断言取尾巴,附进错误。目标形态:

```
bx 隧道健康检查超时(20s): restarts=1
  singbox: outbound/vless[proxy]: dial tcp 166.1.190.123:443: i/o timeout
```

一行就能分辨握手失败 / 超时 / reset,而不必靠猜端口。

### 五、日志去向

混进 Core 现有的 `log.Printf`,即 Guardian 捕获的 `/var/log/bx-guard.err.log`
(用户决策,2026-08-06)。好处:不新增文件、不新增权限收紧步骤、`bx logs` 与诊断包
自动带上。代价是 guard 日志多几行——因为级别是 `warn`,量很小。

不单独落 `/var/log/bx-tunnel.log`:为了几行 warn 增加一个文件、一套权限、一处诊断包
收集,不划算。

## 本期不做

- 不改 sing-box 日志级别(`warn` 已合适)。
- 不碰任何传输逻辑、健康检查判定、重连退避。
- 不做速率限制:`warn` 级本就稀疏,加限流是没有证据的复杂度。若日后真刷屏,那时再按
  实测加。

## 已知局限

环形缓冲只保留最后若干行。子进程在很早期崩溃、之后被反复重启时,尾巴反映的是**最近
一次**尝试,不是第一次失败的原因。可接受:健康检查失败与最近一次尝试同期,正是要看的。
