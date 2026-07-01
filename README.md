# bx

bx 是一个开箱即用的透明全局代理。服务端和客户端都只需要一个 `bx` 二进制:

- VPS 上运行 `bx server`
- 本机运行 `bx setup` / `bx up`
- 客户端只使用 `bx://` 链接

应用无需配置代理。bx 会在网络层接管流量,自动分流、处理 DNS,并在隧道不可用时 fail-closed,避免真实 IP 裸奔。

## 快速开始

### 0. 下载 bx

从 GitHub Releases 下载对应平台的压缩包:

```bash
# Linux x86_64 示例
curl -LO https://github.com/getbx/bx/releases/latest/download/bx_linux_amd64.tar.gz
tar -xzf bx_linux_amd64.tar.gz
chmod +x bx
./bx --version
```

可选校验:

```bash
curl -LO https://github.com/getbx/bx/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

### 1. VPS 安装 bx server

和客户端一样简单——**一条命令**:

```bash
sudo ./bx server up        # 装好(默认 REALITY+hysteria2、自动探测公网IP)并启动
bx server status           # 看状态
sudo bx server down        # 停
```

`bx server up` 自动:生成 x25519 密钥/UUID/证书、探测公网 IP、写配置、装系统服务、**启动**,
并打印客户端**一键命令**(`sudo bx setup <reality> --udp <hys2>`,主 reality 隐蔽 TCP + hysteria2 加速 UDP,按类分流)。
全程内嵌静态 sing-box,**无需手搭、零配置**。SNI 默认借 `www.cloudflare.com`(装机时自动体检证书大小;
别用 microsoft——证书过大会让 reality 握手失败),内置 `flow=xtls-rprx-vision`/`fp=chrome` 等 2026 推荐默认。

需要别的:`--protocol hysteria2`(纯速度档)、`--protocol brook`(简单兜底)、`--tcp-only`(reality 不带 hys2)、
`--host <域名>`(自定义)、`--port`、`--sni`。协议怎么选见 [docs/multi-transport-guide.md](docs/multi-transport-guide.md)。
多用户:`sudo bx server share <name>` / `revoke <name>`。

之后也可以随时重新生成链接:

```bash
sudo bx server link --host <VPS_IP或域名>
```

分享给其他人:

```bash
sudo bx server share alice --host <VPS_IP或域名>
sudo bx server shares
sudo bx server revoke alice
```

如果 VPS 使用 ufw,创建分享时可显式放行端口:

```bash
sudo bx server share alice --host <VPS_IP或域名> --open-ufw
```

也可以启动一个只监听本机的极简 Web UI:

```bash
sudo bx server ui --host <VPS_IP或域名>
```

然后从自己的电脑通过 SSH 隧道访问:

```bash
ssh -L 8787:127.0.0.1:8787 <VPS>
```

浏览器打开 `http://127.0.0.1:8787`。

### 2. 客户端安装 bx

```bash
sudo ./bx setup '<client-link>'
sudo bx up
```

Linux 客户端直接使用这组命令。

#### 让你的 agent 操作 bx(AI-native,可选)

`bx setup` / `bx up` 跑通后,把控制面接给你的 agent:让你的 agent 运行

    bx mcp install

并照打印的 `claude mcp add` 指令做(**只打印、不自跑**)。之后 agent 就能查状态、
换传输(brook↔REALITY 防封)、重劫持等——以**业主**身份授权、**无需 sudo**
(业主 = 运行 `sudo bx setup` 的用户)。

`setup` 会安装系统服务,`up` 会启动并接管流量,`down` 会停止保护。

> **多传输(容灾 + 加速)**:bx 支持 **brook / REALITY / hysteria2 / trojan / shadowsocks / vmess 六种引擎**平级,
> 直接甩别处的分享链接即可用(裸链接建议先 `bx blink <link>` 换壳)。但**六种不是一个层次**——按当今封锁/检测态势分档:
>
> - 🟢 **主力**:**REALITY**(TCP,最隐蔽,2026 实测 98-99% 突破)+ **hysteria2**(UDP/QUIC 速度档,建议配 salamander 混淆)。
> - 🟡 **兼容**:trojan / vmess / shadowsocks / brook——接住已有节点,但 2025 起强 DPI 下 trojan/vmess/ss 检出 80-95%,慎用于强封锁。
>
> 推荐组合 = **REALITY(TCP)+ hysteria2(UDP)+ brook 兜底**,即"按类分流 + 容灾",既安全又有速度。
> `bx setup` 贴兼容档链接会提示弱点并建议 server 端换 REALITY(不止 GFW——Claude/OpenAI/Google 等也对弱协议出口 IP 做风控)。
> 全程 fail-closed 不泄漏。详见 [docs/multi-transport-guide.md](docs/multi-transport-guide.md)。

WebRTC、DNS、IPv6、QUIC 等泄漏面和检测边界见 [docs/leak-surfaces.md](docs/leak-surfaces.md)。真实 WebRTC 检测可用 `bx webrtc-check --browser --json --expected-ip <proxy-ip>`。

macOS 用户优先使用 release 包。安装后菜单栏图标会常驻显示保护状态,并提供 Set Up、Start Protection、Restart、Turn Off、Logs、Doctor 这些必要入口。命令行仍然保留,用于自动化、远程诊断和高级维护。

#### macOS 安装包

macOS release 包会一次装好两件事并启动菜单栏 App:

- `bx` CLI:安装到 `/usr/local/bin/bx`
- 菜单栏 App:安装到 `~/Applications/Bx.app`
- 菜单栏日志:写到 `~/Library/Logs/bx/menu.log` 和 `menu.err.log`

菜单栏 App 是 macOS 的默认体验:它显示当前保护状态、延迟、DNS 接管状态和诊断入口。它保持克制,不是复杂控制面板;安装时也不会自动配置或接管网络。真正启动保护需要用户明确确认:通常在菜单栏里完成,也可以用命令行备用路径。

下载 release 后运行:

```bash
./install.sh
```

`install.sh` 会先检查 macOS、CPU 架构和必要文件,避免装错包。它只安装 CLI、安装并启动菜单栏 App;不会执行 `bx setup`、不会执行 `bx up`、不会修改 DNS 或路由。

安装后看菜单栏图标即可。如果菜单栏显示 `Setup Required`,点击 `Set Up bx...` 粘贴客户端链接即可完成配置。配置成功后菜单栏会询问是否立即开始保护。命令行备用路径是 `sudo bx setup '<client-link>' && sudo bx up`。

#### 从源码安装菜单栏 App

开发时也可以从仓库源码打包并安装到当前用户:

```bash
cd /path/to/bx
scripts/install-macos-menu.sh install
```

安装后会生成并安装:

- `~/Applications/Bx.app`
- `~/Library/LaunchAgents/com.getbx.bx.menu.plist`
- `~/Library/Logs/bx/menu.log` 和 `menu.err.log`

常用维护命令:

```bash
scripts/install-macos-menu.sh status
scripts/install-macos-menu.sh restart
scripts/install-macos-menu.sh uninstall
```

如果菜单栏显示 `Update Required`,说明 `/usr/local/bin/bx` 太旧。重新安装当前 CLI 后重启菜单栏:

```bash
sudo install -m 0755 ./bx /usr/local/bin/bx
scripts/install-macos-menu.sh restart
```

如果只想生成 `.app` 包而不安装:

```bash
scripts/package-macos-menu.sh
open dist/macos/Bx.app
```

生成可分发 macOS release 包:

```bash
scripts/package-macos-release.sh
```

产物:

```text
dist/release/bx-macos-arm64/
  bx
  Bx.app
  install.sh
  uninstall.sh
  README.txt
dist/release/bx-macos-arm64.tar.gz
dist/release/SHA256SUMS
```

发包前可验证产物:

```bash
scripts/verify-macos-release.sh
```

菜单栏 App 会调用 `/usr/local/bin/bx`,因此本机 CLI 更新后也应同步安装到该路径:

```bash
sudo install -m 0755 ./bx /usr/local/bin/bx
```

macOS DNS 状态可单独查看或手动修复:

```bash
bx dns status
sudo bx dns on
sudo bx dns off
```

macOS launchd 实机验证可先 dry-run:

```bash
scripts/darwin-launchd-smoke.sh
sudo BX_LINK='<client-link>' scripts/darwin-launchd-smoke.sh --execute
```

日常使用:

```bash
bx status
bx doctor
bx logs
bx dns status
sudo bx down
sudo bx up
```

给脚本或 AI agent 诊断时,使用 JSON 输出:

```bash
bx capabilities
bx doctor --json
sudo bx server doctor --json
sudo bx server shares --json
```

`bx capabilities` 会输出稳定的机器可读能力清单,标明每个入口是否需要 root、是否会修改系统或网络、是否读取敏感配置。上面的 JSON 诊断命令只读取状态并输出机器可解析结果,不会修改系统或网络配置。

## 命令

| 命令 | 作用 |
|---|---|
| `sudo bx server install --host <host>` | VPS 首次安装 bx server |
| `sudo bx server start` | 启动 bx server 并设为开机自启 |
| `sudo bx server stop` | 停止 bx server 并取消开机自启 |
| `sudo bx server link --host <host>` | 生成客户端链接 |
| `sudo bx server share <name> --host <host>` | 创建一个独立分享链接 |
| `sudo bx server shares` | 查看已分享的链接 |
| `sudo bx server shares --json` | 以 JSON 查看已分享的链接 |
| `sudo bx server revoke <name>` | 撤销一个分享 |
| `sudo bx server rotate --host <host>` | 轮换 server 密码并生成新的客户端链接 |
| `sudo bx server logs` | 查看服务端日志 |
| `sudo bx server ui --host <host>` | 启动只监听本机的极简 Web UI |
| `sudo bx server uninstall` | 卸载 bx server 服务 |
| `sudo bx setup <client-link>` | 客户端首次配置 |
| `sudo bx up` | 启动客户端并设为开机自启 |
| `sudo bx down` | 停止客户端并取消开机自启 |
| `bx dns status` | 查看 macOS DNS 接管状态 |
| `sudo bx dns on` | 手动将 macOS 系统 DNS 切到 bx |
| `sudo bx dns off` | 恢复 bx 保存的 macOS 原始 DNS |
| `bx status` | 查看客户端状态面板 |
| `bx capabilities` | 输出机器可读能力清单 |
| `bx doctor` | 诊断客户端配置、服务状态和链接连通性 |
| `bx doctor --json` | 输出客户端机器可读诊断 |
| `bx webrtc-check --json` | 输出 WebRTC 泄漏风险诊断 |
| `bx webrtc-check --browser --json --expected-ip <ip>` | 打开本地测试页,真实收集浏览器 ICE candidates 并判断公网 IP 是否符合预期 |
| `bx logs` | 查看客户端日志 |
| `scripts/package-macos-menu.sh` | 打包 macOS 菜单栏 App 到 `dist/macos/Bx.app` |
| `scripts/package-macos-release.sh` | 生成 macOS release 目录和 `.tar.gz` |
| `scripts/verify-macos-release.sh` | 验证 macOS release 目录、压缩包和 SHA256SUMS |
| `scripts/install-macos-menu.sh install` | 安装并启动 macOS 菜单栏 App,不启动 protection、不修改网络配置 |
| `scripts/install-macos-menu.sh status` | 查看 macOS 菜单栏 App 安装和运行状态 |
| `scripts/install-macos-menu.sh uninstall` | 移除 macOS 菜单栏 App 和登录项,不关闭 protection |
| `sudo bx run` | 前台运行,用于调试 |
| `sudo bx uninstall` | 卸载客户端服务 |
| `sudo bx server doctor` | 诊断服务端配置、监听端口和服务状态 |
| `sudo bx server doctor --json` | 输出服务端机器可读诊断 |

## 配置

客户端默认配置路径:

```text
/etc/bx/config.yaml
```

服务端默认配置路径:

```text
/etc/bx/server.yaml
```

通常不需要手写配置。`bx server install` 和 `bx setup` 会自动生成需要的文件。

客户端支持的常用配置:

```yaml
server: "bx://..."
killswitch: true
global: false
dns:
  china: 223.5.5.5
  fakeip_cidr: 198.18.0.0/15
bypass:
  - 10.0.0.0/16
rules:
  - direct: ["*.corp.internal", "10.0.0.0/8"]
  - proxy: ["*.openai.com"]
```

说明:

- `killswitch: true`:隧道不健康时阻断代理连接。
- `global: true`:除内网和用户直连规则外,所有流量都走 bx 隧道。
- `bypass`:路由层绕过 bx 的网段,适合管理网、SSH、内网。
- 私网、Docker、loopback、link-local 默认内建直连,通常无需手动配置。
- `transports: [link1, link2, ...]`(替代 `server:`):多传输自动容灾,有序优先级,主挂自动切备。
- `udp.transport: "hysteria2://..."`:按类分流——UDP/QUIC 走它加速、TCP 走主传输。各自独立 fail-closed。
- 多传输/分流详见 [docs/multi-transport-guide.md](docs/multi-transport-guide.md)。

### 路由器模式(mode: router)

把 bx 装在网关/路由器上,只代理 **LAN 客户端的转发流量**;路由器自身流量一律不碰
(源地址策略路由),因此 Tailscale、管理流量、上游不受影响。

```yaml
mode: router
killswitch: true
router:
  lan_cidrs: [192.168.8.0/24]   # 要代理的 LAN 网段;留空则自动探测 br-* 私网桥
```

- 只有「源在 `lan_cidrs` 内」的转发流量被劫进 bx;路由器发起的流量走正常路由直连。
- **Fail-closed**:LAN 流量只能经 bx 出去;bx/隧道一挂即丢弃,绝不泄露真实 IP(路由层 blackhole + 防火墙)。
- 防泄露:强制 LAN DNS 走 bx fake-IP;封 LAN IPv6 转发(防 WebRTC/ICE v6 泄露);UDP(含 STUN)走代理或 block,不直连。
- 部署前用 `bx router-plan -c /etc/bx/config.yaml` 预览将下发的 `ip` + `nft` 命令(不改系统)。
- 目前需要 OpenWrt fw4(nftables)。完整上线步骤见 `docs/router-mode-deploy-runbook.md`。

## 构建

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bx .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bx .
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o bx .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o bx .
```

运行时需要 root 权限配置 TUN 和系统路由。

## 测试

```bash
go test ./...
```

端到端测试需要在真实机器上以 root 运行。
