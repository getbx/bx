# bx

一个二进制,两个功能。

**① 泄漏检测** —— `bx leakcheck`

不需要 root、不需要配置、**不需要 bx 在跑**。它打开一个只在本机监听的一次性页面,把浏览器那半
(WebRTC 出口、IPv6、时区、指纹)与本机那半(路由表、DNS 去向)对起来 —— 只有两半合起来才判得了
泄漏。**bx 关着、别的 VPN 在跑时照样能用**,它会认出那条隧道并如实说自己看不到它的配置。

它还会查一件网站查不到的事:**有没有人往你的路由表里塞了绕过隧道的路由**
(TunnelVision / CVE-2024-3661)。检测结果不留存。

**② 透明全局代理** —— `bx setup` / `bx up`

服务端和客户端都只需要同一个 `bx` 二进制:VPS 上跑 `bx server`,本机 `bx setup` 一条
`bx://` 链接即可。应用无需配置代理,bx 在网络层接管流量、自动分流、处理 DNS,
并在隧道不可用时 fail-closed,避免真实 IP 裸奔。

## 快速开始

完整步骤(一台空 VPS → 电脑整机受保护,含 macOS / Windows / Linux、验证、日常)见
**[docs/guide/install-tutorial.md](docs/guide/install-tutorial.md)**。最短路径:

```bash
# VPS 上
curl -LO https://github.com/getbx/bx/releases/latest/download/bx_linux_amd64.tar.gz
tar -xzf bx_linux_amd64.tar.gz && chmod +x bx
sudo ./bx server up --open-ufw        # 最后打印一整条 sudo bx setup … 命令

# 电脑上(macOS 用 dmg 装 Bx.app;Windows 用 bx-setup.exe;Linux 同上下载)
sudo bx setup --udp '<UDP 链接>' '<主链接>'   # 原样贴服务器打印的那条
sudo bx up
bx status                              # Status Protected、Tunnel healthy
```

macOS:打开 `bx-macos-arm64.dmg`,把 **Bx.app** 拖进 **Applications**,双击打开,它会引导安装与
设置。首次打开 macOS 会说「**无法验证开发者**」:**系统设置 → 隐私与安全性 → 仍要打开**。
看到的若是「**已损坏,应移到废纸篓**」,那是包被改动过 —— **不要放行**,重新下载。

## 文档

| 想做什么 | 看这里 |
| --- | --- |
| 从零装好 | [安装教程](docs/guide/install-tutorial.md) |
| 服务端细节(协议、分享给别人、Web UI) | [服务端](docs/guide/server.md) · [REALITY 搭建](docs/guide/reality-server-setup.md) · [多传输](docs/guide/multi-transport-guide.md) |
| 客户端细节(Linux / Windows / 让 agent 操作 bx) | [客户端](docs/guide/client.md) · [Agent Tools](docs/guide/agent-tools.md) |
| macOS:菜单栏、安装包、升级、排查、关不掉怎么办 | [macOS](docs/guide/macos.md) |
| 与 Docker / Tailscale / 其它 VPN 共存 | [共存](docs/guide/coexistence.md) |
| Steam、Apple 等应用的直连预设 | [预设](docs/guide/presets.md) |
| 全部命令 | [命令](docs/guide/commands.md) |
| 配置文件:具名出口、路由器模式 | [配置](docs/guide/configuration.md) · [路由器部署](docs/guide/router-mode-deploy-runbook.md) |
| 泄漏面与检测边界 | [泄漏面](docs/guide/leak-surfaces.md) |
| 开发、测试、已知问题 | [docs/README.md](docs/README.md) |

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

## 安全

bx 的安全属性(kill-switch/fail-closed、私网恒直连、IPv6 黑洞、WebRTC 无泄漏)、信任边界、以及残留风险(白名单去匿名化、直连 DNS 信任等)见 [SECURITY.md](SECURITY.md)。

发现漏洞请**私密**报告:仓库 **Security → Report a vulnerability**(勿开公开 issue)。
