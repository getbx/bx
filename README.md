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
- **开机自启**:`bx setup` 装 systemd 服务,`bx up` 启动并使能
- **远程安全**:`bypass` 保护管理网/SSH;`--test-timeout` 死手定时器防锁死

## 构建

单一静态二进制(无 CGO):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bx .
sudo cp bx /usr/local/bin/bx
```

运行时依赖:root(TUN + 路由)。brook 二进制与 china 列表已**内嵌进二进制**,首次运行自解压到 `/var/lib/bx`,无需手动准备;仅当想替换为外部自带的 brook 或自定义列表时才需额外文件。

## 快速开始(傻瓜版)

bx 是单一静态二进制,brook/列表已内嵌。管理员把服务器链接转成 `blink://` 发给用户,用户三步即跑:

```bash
# (管理员)由 brook 链接生成 blink,发给用户
bx blink "brook://server?server=1.2.3.4%3A9999&password=xxx"
#   → blink://YnJvb2s6Ly...

# (用户)① 放上二进制  ② 配置(自动连通检测,不启动)  ③ 启动
sudo install -m755 bx /usr/local/bin/bx
sudo bx setup blink://YnJvb2s6Ly...      # 看到 ✅ 服务器连通
sudo bx up                                # 后台起 + 开机自启
```

日常只用两个词:`sudo bx down`(停+取消自启)、`sudo bx up`(起+自启)。
`bx status` 看面板;`sudo bx run` 前台带 log 排错;`sudo bx uninstall` 卸载。
私网/docker 自动绕过 tun,SSH 不会被锁死;无需写任何 YAML。

## 配置 `/etc/bx/config.yaml`(非 root 回退 `~/.config/bx/config.yaml`)

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
lists:                           # 可选:默认用内嵌快照并自动刷新,留空即可
  china_domain: /home/you/.brook/china_domain.txt   # 可选,外部覆盖
  china_cidr:   /home/you/.brook/china_cidr4.txt    # 可选,外部覆盖
```

> brook 服务器 IP 会自动加入 bypass(避免 brook→服务器的连接被 tun 捕获成环)。
>
> 私网/docker 段(`10/8`、`172.16/12`、`192.168/16`、`169.254/16`、`100.64/10`、`127/8`)**内建绕过 tun**,
> 无需再写进配置。两层兜底:① 启动时装 `ip rule to <段> lookup main pref 150`(< 全量进 tun 的 200),
> 让宿主机访问 docker 容器/内网的包**永不进 tun**、由内核原路由 native 投递(docker0/br-* on-link、内网 via 网关);
> ② 分流脑对到达 dialer 的私网 IP(如 fakeip 反查出的内网域名)也判直连。该 `ip rule` 随每次启动重装,
> 不存在「手动加的规则在 VPN 重连后丢失」问题。如需把某私段强制走代理,用 `rules.proxy`。
> TUN 默认地址用 TEST-NET-2 `198.51.100.1/30`,刻意避开 docker 默认地址池 `172.16/12`,防止与 compose 网段撞段。

## 命令

| 命令 | 作用 | 权限 |
|---|---|---|
| `sudo bx setup blink://...` | 首次配置:写配置+装服务+连通检测(不启动) | root |
| `sudo bx up` | 启动 + 开机自启 | root |
| `sudo bx down` | 停止 + 取消自启 | root |
| `sudo bx run [flags]` | 前台运行、实时 log(调试/服务内部) | root |
| `bx status` | 状态面板 | 任意用户 |
| `bx blink brook://...` | 生成 blink 链接(管理员) | 任意用户 |
| `sudo bx uninstall` | 卸载服务 | root |

`setup` 支持 `--config`、`--probe`、`--force`、`--strict` 标志。
`bx run` 保留 `--config`/`--global`/`--test-timeout`/`--tun*`/`--probe`/`--brook`/`--china-*` 覆盖标志,供调试时手动覆盖;`--test-timeout 90s` 可设死手定时器(到点自动还原,远程首跑保命)。

## 用法一:按需启停(共享机推荐)

只在需要时开,用完即关——全局透明但可控:

```bash
# ① 首次配置(一次性)
sudo bx setup blink://YnJvb2s6Ly...   # 写配置 + 装服务 + 连通检测

# ② 开启
sudo bx up                 # 后台启动 + 开机自启

# ③ 使用(开着期间整机外网自动分流,无需配代理)
curl https://github.com      # 境外 → brook
curl https://www.baidu.com   # 国内 → 直连
bx status                    # 看面板

# ④ 停止
sudo bx down               # 停止 + 取消自启
```

> 启动头几秒隧道健康确认前,kill-switch 会阻断境外连接(fail-closed),属正常。

**前台调试**:需要实时 log 时用 `sudo bx run -c /abs/config.yaml`(前台阻塞,Ctrl-C 退出)。

**两种分流模式**:默认按 china 列表分流(中国直连);加 `--global`(或 config `global: true`)则
**除内网(bypass)和用户 `direct` 规则外,一切流量(含中国)都走 VPS**——LAN/SSH 始终受 bypass 保护。

## 用法二:开机自启(专机)

```bash
sudo bx setup blink://YnJvb2s6Ly...   # 首次配置:写配置 + 装服务 + 连通检测
sudo bx up                             # 启动 + 开机自启;开机/崩溃自动拉起
systemctl status bx                    # 查看服务状态
sudo bx uninstall                      # 停用并删除
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
