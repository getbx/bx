# bx

基于免费 [brook](https://github.com/txthinking/brook) 的**透明全局代理**(自研「类 ipio」,单一 Go 二进制)。

所有系统流量在网络层经 TUN 自动分流:**中国直连、其余走 brook 加密隧道**,对程序零配置。
完全自有代码(brook 仅作加密隧道黑盒子进程)。

> 设计文档:`docs/superpowers/specs/2026-06-03-bx-design.md`

## 工作原理

```
所有流量 ─▶ TUN 引擎(gVisor netstack 终结 TCP/UDP)
              ├─ fake-IP DNS(A 查询返回保留段假 IP,记录 IP↔域名)
              ├─ 分流脑:连接回到 TUN 时反查域名
              │     ├─ 命中 china 列表 / 用户直连规则 ─▶ 直连(国内 DNS 解析真实 IP)
              │     └─ 其余 ─▶ brook 隧道(把域名交给服务器远程解析,零 DNS 污染)
              ├─ kill-switch:隧道不健康时阻断代理连接(fail-closed,不泄漏真实 IP)
              └─ 全局路由劫持:策略路由 + fwmark 防环 + bypass 保 SSH/内网
```

## 特性

- **透明全局**:TUN 接管 TCP/UDP,程序无需任何代理配置
- **智能分流**:china 域名/网段直连,其余走代理;支持用户规则(域名/CIDR)
- **防 DNS 污染**:fake-IP,代理域名交 brook 远程解析
- **断线保护**:brook 子进程守护 + 健康检查 + 自动重连;kill-switch 防真实 IP 裸奔
- **状态面板**:`bx status` 看节点/延迟/连接数/分流占比/流量
- **开机自启**:`bx install` 生成 systemd 服务
- **远程安全**:`bypass` 保护管理网/SSH;`--test-timeout` 死手定时器防锁死

## 构建

单一静态二进制(无 CGO):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bx .
sudo cp bx /usr/local/bin/bx
```

运行时依赖:`brook` 二进制、china 列表(`china_domain.txt` / `china_cidr4.txt`)、root(TUN + 路由)。

## 配置 `~/.config/bx/config.yaml`

```yaml
server: "brook://..."            # brook 服务器 link(或 host:port)
killswitch: true                 # 隧道挂时阻断代理连接,不泄漏真实 IP
global: false                    # 全局模式:true=除内网/用户direct外一切走代理(亦可用 --global)
dns:
  china: 223.5.5.5               # 直连域名用的国内 DNS
  fakeip_cidr: 198.18.0.0/15     # fake-IP 段
bypass:                          # ⚠️ 绕过 tun、走原路由的网段
  - 10.0.0.0/16                 #   必须含你 SSH 进来的管理网/LAN,否则会把自己锁死!
rules:                           # 可选,优先级最高
  - direct: ["*.corp.internal", "10.0.0.0/8"]
  - proxy:  ["*.openai.com"]
lists:
  china_domain: /home/you/.brook/china_domain.txt
  china_cidr:   /home/you/.brook/china_cidr4.txt
```

> brook 服务器 IP 会自动加入 bypass(避免 brook→服务器的连接被 tun 捕获成环)。

## 命令

| 命令 | 作用 | 权限 |
|---|---|---|
| `bx up [flags]` | 启动(前台阻塞)| root |
| `bx status` | 状态面板 | 任意用户 |
| `bx down` | 停止并还原路由 | root |
| `bx install [flags]` | 安装 systemd 自启服务 | root |
| `bx uninstall` | 卸载服务 | root |

以 root 运行时家目录为 `/root`,故 `--config` / `--brook` / `--china-domain` / `--china-cidr` 建议用**绝对路径**。
`--test-timeout 90s` 可设死手定时器(到点自动还原,远程首跑保命)。

## 用法一:按需启停(共享机推荐)

只在需要时开,用完即关——全局透明但可控。建议放进 `tmux`:

```bash
# ① 开启
tmux new -s bx
sudo bx up -c /abs/config.yaml --brook /abs/brook \
  --china-domain /abs/china_domain.txt --china-cidr /abs/china_cidr4.txt
#   看到「✅ bx 已全局接管」即可;Ctrl-b d 脱离 tmux,bx 继续后台跑

# ② 使用(开着期间整机外网自动分流,无需配代理)
curl https://github.com      # 境外 → brook
curl https://www.baidu.com   # 国内 → 直连
bx status                    # 看面板

# ③ 停止
sudo bx down                 # 任意 shell;或 tmux attach -t bx 后 Ctrl-C
```

> 启动头几秒隧道健康确认前,kill-switch 会阻断境外连接(fail-closed),属正常。

**两种分流模式**:默认按 china 列表分流(中国直连);加 `--global`(或 config `global: true`)则
**除内网(bypass)和用户 `direct` 规则外,一切流量(含中国)都走 VPS**——LAN/SSH 始终受 bypass 保护。

```bash
sudo bx up --global ...   # 全局:全走 VPS,除内网
```

## 用法二:开机自启(专机)

```bash
sudo bx install -c /abs/config.yaml --brook /abs/brook \
  --china-domain /abs/china_domain.txt --china-cidr /abs/china_cidr4.txt
systemctl status bx          # 查看;开机/崩溃自动拉起
sudo bx uninstall            # 停用并删除
```

## 已知限制

- UDP 不走代理(brook 免费版 socks5 仅 TCP);DNS 由引擎就地 fake-IP 应答
- `bx reload` 热重载未实现:改配置后 `bx down && bx up` 或 `systemctl restart bx`
- 单节点(多节点/故障转移属 YAGNI 未做)
- 仅 Linux

## 测试

```bash
go test -race ./...          # 全部单元测试(netstack 引擎用 channel/pipe 端点,无需 root)
```

端到端需在真实机器上以 root 跑(TUN + 路由);`cmd/tuncheck` 是引擎冒烟工具。
